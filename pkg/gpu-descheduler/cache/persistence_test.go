package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withTimeout(t *testing.T, name string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return: deadlock", name)
	}
}

func TestConsumedReqIDsArePersisted(t *testing.T) {
	old := GetPersistenceDir()
	dir := t.TempDir()
	SetPersistenceDir(dir)
	t.Cleanup(func() { SetPersistenceDir(old) })

	MarkReqIDConsumed("persist-req", "default", "p", "default", "pod", "Observing")
	t.Cleanup(func() { UnmarkReqIDConsumed("persist-req") })
	withTimeout(t, "SaveConsumedReqIDsToFile", func() {
		if err := SaveConsumedReqIDsToFile(); err != nil {
			t.Error(err)
		}
	})
	data, err := os.ReadFile(filepath.Join(dir, PersistenceFileName))
	if err != nil || !strings.Contains(string(data), "persist-req") {
		t.Fatalf("state file %q, err %v", data, err)
	}
}

func TestCleanupOfExpiredReqIDsReturns(t *testing.T) {
	old := GetPersistenceDir()
	SetPersistenceDir(t.TempDir())
	t.Cleanup(func() { SetPersistenceDir(old) })

	consumedReqMu.Lock()
	consumedReqIDs["expired-req"] = ConsumedReqInfo{ReqID: "expired-req", ConsumedAt: time.Now().Add(-2 * ConsumedReqIDTTL)}
	consumedReqMu.Unlock()
	t.Cleanup(func() { UnmarkReqIDConsumed("expired-req") })

	withTimeout(t, "CleanExpiredConsumedReqIDs", func() {
		if n, err := CleanExpiredConsumedReqIDs(); n != 1 || err != nil {
			t.Errorf("cleaned %d, err %v", n, err)
		}
	})
	withTimeout(t, "IsReqIDConsumed after cleanup", func() {
		if IsReqIDConsumed("expired-req") {
			t.Error("expired ReqID still present")
		}
	})
}
