// Package webhookcert keeps the webhook serving certificate without
// cert-manager. It stores a self-signed CA and a certificate for the webhook
// Service in a Secret, writes the certificate to the webhook server's
// directory, puts the CA into the caBundle of the MutatingWebhookConfiguration
// and renews both before they expire.
package webhookcert

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Keys of the Secret. The certificate and key files in the cert directory
// use the same names.
const (
	CACertKey = "ca.crt"
	CAKeyKey  = "ca.key"
	CertKey   = "tls.crt"
	KeyKey    = "tls.key"
)

const (
	caValidity    = 10 * 365 * 24 * time.Hour
	certValidity  = 365 * 24 * time.Hour
	renewBefore   = 90 * 24 * time.Hour
	checkInterval = 10 * time.Minute
	retryInterval = 5 * time.Second
)

var log = ctrl.Log.WithName("webhook-cert")

// Manager keeps the certificate. It runs in every replica, not only the
// leader, so a new Pod can get ready while the old one holds the lease.
type Manager struct {
	client        kubernetes.Interface
	namespace     string
	secret        string
	service       string
	webhookConfig string
	certDir       string
	now           func() time.Time

	ready     chan struct{}
	readyOnce sync.Once
}

// New returns a Manager for the certificate of service in namespace, stored
// in the Secret named secret, trusted by the MutatingWebhookConfiguration
// named webhookConfig, and written to certDir.
func New(client kubernetes.Interface, namespace, secret, service, webhookConfig, certDir string) *Manager {
	return &Manager{
		client:        client,
		namespace:     namespace,
		secret:        secret,
		service:       service,
		webhookConfig: webhookConfig,
		certDir:       certDir,
		now:           time.Now,
		ready:         make(chan struct{}),
	}
}

// Ready is closed once the certificate is in place and trusted.
func (m *Manager) Ready() <-chan struct{} { return m.ready }

// NeedLeaderElection makes the Manager run in every replica.
func (m *Manager) NeedLeaderElection() bool { return false }

// Start checks the certificate until ctx ends, and retries soon after a failure.
func (m *Manager) Start(ctx context.Context) error {
	for {
		wait := checkInterval
		if err := m.sync(ctx); err != nil {
			log.Error(err, "Cannot set up the webhook certificate, retrying", "after", retryInterval)
			wait = retryInterval
		} else {
			m.readyOnce.Do(func() {
				log.Info("Webhook certificate is ready", "secret", m.namespace+"/"+m.secret)
				close(m.ready)
			})
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

func (m *Manager) dnsNames() []string {
	host := m.service + "." + m.namespace + ".svc"
	return []string{host, m.service, m.service + "." + m.namespace, host + ".cluster.local"}
}

func (m *Manager) sync(ctx context.Context) error {
	secret, err := m.client.CoreV1().Secrets(m.namespace).Get(ctx, m.secret, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get Secret %s/%s: %w", m.namespace, m.secret, err)
	}
	data, changed, err := m.renew(secret.Data)
	if err != nil {
		return err
	}
	if changed {
		secret.Data = data
		if _, err := m.client.CoreV1().Secrets(m.namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update Secret %s/%s: %w", m.namespace, m.secret, err)
		}
		log.Info("Issued webhook certificate", "secret", m.namespace+"/"+m.secret, "dnsNames", m.dnsNames())
	}
	// The key goes first, so the webhook server never pairs a new key with
	// the old certificate for longer than the write of one file.
	for _, key := range []string{KeyKey, CertKey} {
		if err := writeFile(filepath.Join(m.certDir, key), data[key]); err != nil {
			return err
		}
	}
	return m.injectCA(ctx, data[CACertKey])
}

// renew returns the Secret data with a CA and a certificate that stay valid
// for longer than renewBefore, and whether it differs from data.
func (m *Manager) renew(data map[string][]byte) (map[string][]byte, bool, error) {
	soon := m.now().Add(renewBefore)
	caCert, caKey, err := parseCA(data[CACertKey], data[CAKeyKey])
	if len(data[CACertKey]) == 0 {
		err = errors.New("no CA in the Secret yet")
	} else if err == nil && soon.After(caCert.NotAfter) {
		err = errors.New("CA expires soon")
	}
	if err != nil {
		log.Info("Creating a new webhook CA", "reason", err.Error())
		caCert, caKey, data, err = m.newCA()
		if err != nil {
			return nil, false, err
		}
	} else if err := m.verify(data[CertKey], data[KeyKey], caCert, soon); err == nil {
		return data, false, nil
	} else {
		log.Info("Creating a new webhook certificate", "reason", err.Error())
	}
	certPEM, keyPEM, err := m.newCert(caCert, caKey)
	if err != nil {
		return nil, false, err
	}
	return map[string][]byte{
		CACertKey: data[CACertKey],
		CAKeyKey:  data[CAKeyKey],
		CertKey:   certPEM,
		KeyKey:    keyPEM,
	}, true, nil
}

// verify checks that the certificate matches its key, chains to the CA, and
// is still valid for every DNS name at the time soon.
func (m *Manager) verify(certPEM, keyPEM []byte, ca *x509.Certificate, soon time.Time) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, name := range m.dnsNames() {
		if _, err := pair.Leaf.Verify(x509.VerifyOptions{
			DNSName:     name,
			Roots:       roots,
			CurrentTime: soon,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) newCA() (*x509.Certificate, crypto.Signer, map[string][]byte, error) {
	now := m.now()
	template := &x509.Certificate{
		Subject:               pkix.Name{CommonName: m.service + "-ca", Organization: []string{"HAMi"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certPEM, keyPEM, key, err := issue(template, nil, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create CA: %w", err)
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, nil, nil, err
	}
	return cert, key, map[string][]byte{CACertKey: certPEM, CAKeyKey: keyPEM}, nil
}

func (m *Manager) newCert(ca *x509.Certificate, caKey crypto.Signer) ([]byte, []byte, error) {
	now := m.now()
	names := m.dnsNames()
	notAfter := now.Add(certValidity)
	if notAfter.After(ca.NotAfter) {
		notAfter = ca.NotAfter
	}
	template := &x509.Certificate{
		Subject:     pkix.Name{CommonName: names[0]},
		DNSNames:    names,
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certPEM, keyPEM, _, err := issue(template, ca, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create webhook certificate: %w", err)
	}
	return certPEM, keyPEM, nil
}

// issue creates a key and a certificate for it, signed by parent, or
// self-signed when parent is nil.
func issue(template, parent *x509.Certificate, parentKey crypto.Signer) ([]byte, []byte, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, err
	}
	template.SerialNumber = serial
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), parentKey)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), key, nil
}

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, crypto.Signer, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	if !pair.Leaf.IsCA {
		return nil, nil, errors.New("CA certificate is not a CA")
	}
	key, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("CA key cannot sign")
	}
	return pair.Leaf, key, nil
}

// writeFile replaces path with data unless it already holds it.
func writeFile(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Manager) injectCA(ctx context.Context, caPEM []byte) error {
	hooks := m.client.AdmissionregistrationV1().MutatingWebhookConfigurations()
	cfg, err := hooks.Get(ctx, m.webhookConfig, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get MutatingWebhookConfiguration %s: %w", m.webhookConfig, err)
	}
	changed := false
	for i := range cfg.Webhooks {
		if !bytes.Equal(cfg.Webhooks[i].ClientConfig.CABundle, caPEM) {
			cfg.Webhooks[i].ClientConfig.CABundle = caPEM
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := hooks.Update(ctx, cfg, metav1.UpdateOptions{FieldManager: "gpu-descheduler"}); err != nil {
		return fmt.Errorf("update MutatingWebhookConfiguration %s: %w", m.webhookConfig, err)
	}
	log.Info("Injected the webhook CA", "webhookConfig", m.webhookConfig)
	return nil
}
