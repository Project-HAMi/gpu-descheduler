package cache

import (
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

func TestCleanupOfExpiredReqIDsReturns(t *testing.T) {
	consumedReqMu.Lock()
	consumedReqIDs["expired-req"] = ConsumedReqInfo{ReqID: "expired-req", ConsumedAt: time.Now().Add(-2 * ConsumedReqIDTTL)}
	consumedReqMu.Unlock()
	t.Cleanup(func() { UnmarkReqIDConsumed("expired-req") })

	withTimeout(t, "CleanExpiredConsumedReqIDs", func() {
		if n := CleanExpiredConsumedReqIDs(); n != 1 {
			t.Errorf("cleaned %d", n)
		}
	})
	withTimeout(t, "IsReqIDConsumed after cleanup", func() {
		if IsReqIDConsumed("expired-req") {
			t.Error("expired ReqID still present")
		}
	})
}
