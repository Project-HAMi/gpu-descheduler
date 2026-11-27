package webhookcert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	ns      = "gpu"
	secret  = "desched-cert"
	service = "desched"
	hook    = "desched"
)

func setup(t *testing.T, data map[string][]byte) (*Manager, *fake.Clientset) {
	t.Helper()
	client := fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: secret}, Data: data},
		&admissionv1.MutatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: hook},
			Webhooks:   []admissionv1.MutatingWebhook{{Name: "a.hami.io"}, {Name: "b.hami.io"}},
		},
	)
	return New(client, ns, secret, service, hook, t.TempDir()), client
}

func getSecret(t *testing.T, client *fake.Clientset) map[string][]byte {
	t.Helper()
	s, err := client.CoreV1().Secrets(ns).Get(context.Background(), secret, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s.Data
}

func updates(client *fake.Clientset) int {
	n := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == "update" {
			n++
		}
	}
	return n
}

// served returns the certificate a TLS client gets from the cert dir.
func served(t *testing.T, m *Manager) *x509.Certificate {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(filepath.Join(m.certDir, CertKey), filepath.Join(m.certDir, KeyKey))
	if err != nil {
		t.Fatalf("cert dir holds no usable key pair: %v", err)
	}
	return pair.Leaf
}

// checkTrusted asserts that the API server, trusting the caBundle, accepts
// the served certificate under every name it may dial at the time at.
func checkTrusted(t *testing.T, m *Manager, client *fake.Clientset, at time.Time) {
	t.Helper()
	cfg, err := client.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(context.Background(), hook, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	leaf := served(t, m)
	for _, w := range cfg.Webhooks {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(w.ClientConfig.CABundle) {
			t.Fatalf("webhook %s has no caBundle", w.Name)
		}
		for _, name := range []string{"desched.gpu.svc", "desched", "desched.gpu", "desched.gpu.svc.cluster.local"} {
			if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots, CurrentTime: at}); err != nil {
				t.Errorf("webhook %s does not trust the served certificate for %s: %v", w.Name, name, err)
			}
		}
	}
}

func TestSyncIssuesAndInjects(t *testing.T) {
	m, client := setup(t, nil)
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	data := getSecret(t, client)
	for _, k := range []string{CACertKey, CAKeyKey, CertKey, KeyKey} {
		if len(data[k]) == 0 {
			t.Errorf("Secret misses %s", k)
		}
	}
	checkTrusted(t, m, client, time.Now())

	info, err := os.Stat(filepath.Join(m.certDir, KeyKey))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestSyncKeepsValidCertificate(t *testing.T) {
	m, client := setup(t, nil)
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := getSecret(t, client)
	n := updates(client)

	// A restarted Pod gets an empty cert dir and the same Secret.
	m.certDir = t.TempDir()
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := updates(client); got != n {
		t.Errorf("%d updates for a valid certificate, want none", got-n)
	}
	if string(getSecret(t, client)[CertKey]) != string(before[CertKey]) {
		t.Error("valid certificate replaced")
	}
	written, err := os.ReadFile(filepath.Join(m.certDir, CertKey))
	if err != nil || string(written) != string(before[CertKey]) {
		t.Errorf("cert dir does not hold the Secret's certificate: %v", err)
	}
	checkTrusted(t, m, client, time.Now())
}

func TestSyncRenewsCertificateBeforeExpiry(t *testing.T) {
	m, client := setup(t, nil)
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := getSecret(t, client)
	old := served(t, m)

	later := old.NotAfter.Add(-30 * 24 * time.Hour)
	m.now = func() time.Time { return later }
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := getSecret(t, client)
	if string(after[CACertKey]) != string(before[CACertKey]) {
		t.Error("CA replaced although it is valid for years")
	}
	if !served(t, m).NotAfter.After(old.NotAfter) {
		t.Error("certificate not renewed 30 days before it expires")
	}
	checkTrusted(t, m, client, later)
}

func TestSyncRenewsCABeforeExpiry(t *testing.T) {
	m, client := setup(t, nil)
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	ca, err := parseCert(getSecret(t, client)[CACertKey])
	if err != nil {
		t.Fatal(err)
	}

	later := ca.NotAfter.Add(-30 * 24 * time.Hour)
	m.now = func() time.Time { return later }
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	newCA, err := parseCert(getSecret(t, client)[CACertKey])
	if err != nil {
		t.Fatal(err)
	}
	if !newCA.NotAfter.After(ca.NotAfter) {
		t.Error("CA not renewed 30 days before it expires")
	}
	checkTrusted(t, m, client, later)
}

func TestSyncReplacesForeignData(t *testing.T) {
	for name, data := range map[string]map[string][]byte{
		// What kube-webhook-certgen left in the Secret.
		"certgen": {"ca": []byte("x"), "cert": []byte("y"), "key": []byte("z")},
		"garbage": {CACertKey: []byte("x"), CAKeyKey: []byte("y"), CertKey: []byte("z"), KeyKey: []byte("w")},
	} {
		t.Run(name, func(t *testing.T) {
			m, client := setup(t, data)
			if err := m.sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			checkTrusted(t, m, client, time.Now())
		})
	}
}

func TestSyncReissuesForRenamedService(t *testing.T) {
	m, _ := setup(t, nil)
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.service = "renamed"
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := served(t, m).VerifyHostname("renamed.gpu.svc"); err != nil {
		t.Errorf("certificate not reissued for the new Service: %v", err)
	}
}

func TestSyncRestoresCABundle(t *testing.T) {
	m, client := setup(t, nil)
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	hooks := client.AdmissionregistrationV1().MutatingWebhookConfigurations()
	cfg, _ := hooks.Get(context.Background(), hook, metav1.GetOptions{})
	cfg.Webhooks[1].ClientConfig.CABundle = nil
	if _, err := hooks.Update(context.Background(), cfg, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkTrusted(t, m, client, time.Now())
}

func TestSyncFailsWithoutObjects(t *testing.T) {
	m, client := setup(t, nil)
	if err := client.CoreV1().Secrets(ns).Delete(context.Background(), secret, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.sync(context.Background()); !apierrors.IsNotFound(err) {
		t.Errorf("err = %v, want NotFound for the missing Secret", err)
	}

	m, client = setup(t, nil)
	if err := client.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(context.Background(), hook, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.sync(context.Background()); !apierrors.IsNotFound(err) {
		t.Errorf("err = %v, want NotFound for the missing webhook configuration", err)
	}
}

func TestSyncFailsOnConflict(t *testing.T) {
	m, client := setup(t, nil)
	client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, secret, nil)
	})
	if err := m.sync(context.Background()); !apierrors.IsConflict(err) {
		t.Fatalf("err = %v, want the conflict, so another replica's certificate is read on retry", err)
	}
	if _, err := os.Stat(filepath.Join(m.certDir, CertKey)); err == nil {
		t.Error("wrote a certificate the Secret does not hold")
	}
}

func TestStartGetsReadyAfterRetry(t *testing.T) {
	m, client := setup(t, nil)
	hooks := client.AdmissionregistrationV1().MutatingWebhookConfigurations()
	cfg, _ := hooks.Get(context.Background(), hook, metav1.GetOptions{})
	if err := hooks.Delete(context.Background(), hook, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- m.Start(ctx) }()

	select {
	case <-m.Ready():
		t.Fatal("ready without a trusted CA")
	case <-time.After(100 * time.Millisecond):
	}
	// Helm creates the webhook configuration after the Pod started.
	if _, err := hooks.Create(context.Background(), cfg, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.Ready():
	case <-time.After(3 * retryInterval):
		t.Fatal("not ready after the webhook configuration appeared")
	}
	checkTrusted(t, m, client, time.Now())

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m.NeedLeaderElection() {
		t.Error("runs only in the leader, so a rolling update never gets the new Pod ready")
	}
}
