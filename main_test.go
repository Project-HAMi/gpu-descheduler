package main

import (
	"errors"
	"net/http"
	"os"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

func TestWebhookReadyCheck(t *testing.T) {
	ready := make(chan struct{})
	calls := 0
	serverErr := errors.New("not listening")
	check := webhookReadyCheck(ready, func() healthz.Checker {
		calls++
		return func(*http.Request) error { return serverErr }
	})

	if err := check(nil); err == nil {
		t.Fatal("ready before the certificate is ready")
	}
	if calls != 0 {
		t.Fatal("asked for the webhook server before the certificate is ready, which starts it without one")
	}

	close(ready)
	if err := check(nil); !errors.Is(err, serverErr) {
		t.Fatalf("err = %v, want the webhook server check", err)
	}
	_ = check(nil)
	if calls != 1 {
		t.Fatalf("webhook server checker built %d times, want 1", calls)
	}
}

func TestPodNamespace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "gpu")
	if ns, err := podNamespace(); err != nil || ns != "gpu" {
		t.Fatalf("podNamespace() = %q, %v, want gpu", ns, err)
	}

	t.Setenv("POD_NAMESPACE", "")
	if _, err := os.Stat(serviceAccountNamespaceFile); err == nil {
		t.Skip("runs in a Pod")
	}
	if _, err := podNamespace(); err == nil {
		t.Fatal("no error without POD_NAMESPACE and service account")
	}
}
