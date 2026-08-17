package cluster

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// A streaming read holds the FSM store lock for as long as the stream lasts,
// and a stalled client can extend that indefinitely. A snapshot install landing
// behind such a reader must not park a writer on the store lock: Go's RWMutex
// stops admitting readers the moment a writer waits, and applyCommand is one of
// those readers, so the node would stop applying committed entries entirely.
func TestRestoreDoesNotFreezeApplyBehindLongReader(t *testing.T) {
	source := fsmtestNew(t)
	fsmtestPut(t, source, 1, "a", "1")
	snapshot := fsmtestSnapshot(t, source)

	target := fsmtestNew(t)
	target.quiesceTimeout = 150 * time.Millisecond
	target.quiescePoll = time.Millisecond
	fsmtestPut(t, target, 1, "live", "v")

	// A reader that never lets go, standing in for a stalled Range client.
	_, release := target.NewTX()
	defer release()

	restored := make(chan error, 1)
	go func() { restored <- target.Restore(io.NopCloser(bytes.NewReader(snapshot))) }()

	// The apply loop must keep running while the restore is trying to land.
	applied := make(chan struct{})
	go func() {
		fsmtestPut(t, target, 2, "applied-during-restore", "v")
		close(applied)
	}()

	select {
	case <-applied:
	case <-time.After(5 * time.Second):
		t.Fatal("apply froze behind a snapshot install waiting on a long-lived reader")
	}

	select {
	case err := <-restored:
		if !errors.Is(err, ErrRestoreBusy) {
			t.Fatalf("Restore err = %v, want ErrRestoreBusy", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Restore never gave up on the held store lock")
	}

	// Deferring the install must leave the node serving its own data.
	fsmtestWant(t, target, "live", "v")
	fsmtestWant(t, target, "applied-during-restore", "v")
}

// Once the long-lived reader lets go, a retried install succeeds. Raft retries
// on its own, so a deferred restore must not be a terminal state.
func TestRestoreSucceedsAfterReaderReleases(t *testing.T) {
	source := fsmtestNew(t)
	fsmtestPut(t, source, 1, "fresh", "v")
	snapshot := fsmtestSnapshot(t, source)

	target := fsmtestNew(t)
	target.quiesceTimeout = 150 * time.Millisecond
	target.quiescePoll = time.Millisecond
	fsmtestPut(t, target, 1, "stale", "v")

	_, release := target.NewTX()
	if err := target.Restore(io.NopCloser(bytes.NewReader(snapshot))); !errors.Is(err, ErrRestoreBusy) {
		release()
		t.Fatalf("Restore err = %v, want ErrRestoreBusy", err)
	}
	release()

	if err := target.Restore(io.NopCloser(bytes.NewReader(snapshot))); err != nil {
		t.Fatalf("retried Restore: %v", err)
	}
	fsmtestWant(t, target, "fresh", "v")
	fsmtestWantMissing(t, target, "stale")
}
