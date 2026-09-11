package idempotency

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mcpx/internal/state"
)

func testStore(t *testing.T) (*Store, func()) {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db.DB())
	return store, func() { _ = db.Close() }
}

func testKey() Key {
	return Key{RemoteSessionID: "session-1", PrincipalID: "principal-1", Operation: "edit", Value: "key-1"}
}

func TestStoreReplayConflictAndPersistence(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()
	claim, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || claim.Kind != ClaimOwner {
		t.Fatalf("first claim=%+v err=%v", claim, err)
	}
	if err := store.UpdatePending(ctx, key, "sha256:first", []byte(`{"result":"prepared"}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, key, "sha256:first", StateSucceeded, []byte(`{"result":"done"}`), nil); err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(store.db).Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || second.Kind != ClaimReplay || string(second.Record.Response) != `{"result":"done"}` {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	conflict, err := store.Claim(ctx, key, "sha256:other", time.Hour)
	if err != nil || conflict.Kind != ClaimConflict {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestStoreMergesInFlightRequests(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()
	owner, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || owner.Kind != ClaimOwner {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	waiting, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || waiting.Kind != ClaimWait {
		t.Fatalf("waiting=%+v err=%v", waiting, err)
	}
	if err := store.Complete(ctx, key, "sha256:first", StateSucceeded, []byte(`{"ok":true}`), nil); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Wait(ctx, waiting, key)
	if err != nil || string(replayed.Response) != `{"ok":true}` {
		t.Fatalf("wait replay=%+v err=%v", replayed, err)
	}
}

func TestStoreAbandonRemovesOnlyPendingRecords(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()

	owner, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || owner.Kind != ClaimOwner {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	if err := store.Abandon(ctx, key, "sha256:first"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Lookup(ctx, key); err != nil || ok {
		t.Fatalf("abandoned pending record must be gone: ok=%v err=%v", ok, err)
	}

	// A different fingerprint must not delete the owned record.
	owner, err = store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || owner.Kind != ClaimOwner {
		t.Fatalf("re-owner=%+v err=%v", owner, err)
	}
	if err := store.Abandon(ctx, key, "sha256:other"); err != nil {
		t.Fatal(err)
	}
	record, ok, err := store.Lookup(ctx, key)
	if err != nil || !ok || record.State != StatePending {
		t.Fatalf("wrong-fingerprint Abandon must keep pending record: %+v ok=%v err=%v", record, ok, err)
	}

	// The in-process flight must survive a wrong-fingerprint Abandon: the
	// owner is still running, so an identical Claim waits for its completion
	// instead of reporting a foreign pending record.
	waiting, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || waiting.Kind != ClaimWait {
		t.Fatalf("wrong-fingerprint Abandon must keep the in-process flight: %+v err=%v", waiting, err)
	}
	if waiting.Done == nil {
		t.Fatal("ClaimWait must expose the owner's completion channel")
	}
	// The owner completes below; the wait channel must be released.
	if err := store.Complete(ctx, key, "sha256:first", StateSucceeded, []byte(`{"done":true}`), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiting.Done:
	default:
		t.Fatal("owner completion must release the waiting claim")
	}

	// A terminal record is protected: Abandon must not delete it.
	if err := store.Abandon(ctx, key, "sha256:first"); err != nil {
		t.Fatal(err)
	}
	replay, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || replay.Kind != ClaimReplay {
		t.Fatalf("terminal record must survive Abandon: %+v err=%v", replay, err)
	}
}

func TestStoreCompleteFailureStillReleasesWaiters(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()

	owner, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || owner.Kind != ClaimOwner {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	waiting, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || waiting.Kind != ClaimWait {
		t.Fatalf("waiting=%+v err=%v", waiting, err)
	}
	// A failing Complete (wrong fingerprint) must still release the flight so
	// waiters wake up and read the real durable state instead of hanging.
	if err := store.Complete(ctx, key, "sha256:other", StateSucceeded, []byte(`{}`), nil); err == nil {
		t.Fatal("wrong-fingerprint Complete must fail")
	}
	select {
	case <-waiting.Done:
	default:
		t.Fatal("failing Complete must still release waiting claims")
	}
	// The waiter wakes up and reads the real durable state: the record is
	// still pending because the failing Complete changed nothing.
	record, err := store.Wait(ctx, waiting, key)
	if err != nil {
		t.Fatalf("wait after failed Complete=%+v err=%v", record, err)
	}
	if record.State != StatePending {
		t.Fatalf("waiter must see the untouched pending record: %+v", record)
	}

	// A terminal record is immutable: the state precondition must reject a
	// second Complete from a stale owner.
	if err := store.Complete(ctx, key, "sha256:first", StateSucceeded, []byte(`{"ok":true}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, key, "sha256:first", StateFailed, []byte(`{}`), nil); err == nil {
		t.Fatal("succeeded record must reject a stale failed Complete")
	}
	replay, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || replay.Kind != ClaimReplay || string(replay.Record.Response) != `{"ok":true}` {
		t.Fatalf("record must keep the first terminal answer: %+v err=%v", replay, err)
	}
}

func TestStoreReclaimInDoubtAllowsSameKeyRerun(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()

	owner, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || owner.Kind != ClaimOwner {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	if err := store.UpdatePending(ctx, key, "sha256:first", []byte(`{"edit_id":"e1"}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkInDoubt(ctx, key, "sha256:first", nil); err != nil {
		t.Fatal(err)
	}

	inDoubt, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || inDoubt.Kind != ClaimInDoubt {
		t.Fatalf("in-doubt claim=%+v err=%v", inDoubt, err)
	}
	// A wrong fingerprint must never retake the record.
	wrong, err := store.ReclaimInDoubt(ctx, key, "sha256:other")
	if err != nil || wrong.Kind == ClaimOwner {
		t.Fatalf("wrong-fingerprint reclaim=%+v err=%v", wrong, err)
	}
	retake, err := store.ReclaimInDoubt(ctx, key, "sha256:first")
	if err != nil || retake.Kind != ClaimOwner {
		t.Fatalf("reclaim=%+v err=%v", retake, err)
	}
	if err := store.Complete(ctx, key, "sha256:first", StateSucceeded, []byte(`{"edit_id":"e1"}`), nil); err != nil {
		t.Fatal(err)
	}
	replay, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || replay.Kind != ClaimReplay {
		t.Fatalf("replay after rerun=%+v err=%v", replay, err)
	}
}

func TestStoreTouchPreventsLeaseTakeover(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()

	current := time.Now().UTC()
	clock := func() time.Time { return current }
	store.now = clock

	if _, err := store.Claim(ctx, key, "sha256:first", time.Hour); err != nil {
		t.Fatal(err)
	}
	// A second store instance simulates another process: it has no local
	// flight and judges the lease purely by updated_at.
	foreign := NewStore(store.db)
	foreign.now = clock

	// Heartbeat refreshes the lease at t+20s; at t+40s the record must still
	// look live (age since the heartbeat is 20s < PendingLease).
	current = current.Add(20 * time.Second)
	if err := store.Touch(ctx, key, "sha256:first"); err != nil {
		t.Fatal(err)
	}
	current = current.Add(20 * time.Second)
	if claim, err := foreign.Claim(ctx, key, "sha256:first", time.Hour); err != nil || claim.Kind != ClaimPending {
		t.Fatalf("touched record must not be taken over: %+v err=%v", claim, err)
	}

	// Without further heartbeats the lease ages past PendingLease and the
	// foreign claimant takes over.
	current = current.Add(PendingLease)
	takeover, err := foreign.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || takeover.Kind != ClaimOwner {
		t.Fatalf("expired lease must be takeable: %+v err=%v", takeover, err)
	}
}

func TestStoreExpiredTerminalRecordStopsBlockingKey(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()

	current := time.Now().UTC()
	store.now = func() time.Time { return current }

	if _, err := store.Claim(ctx, key, "sha256:first", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, key, "sha256:first", StateFailed, []byte(`{"error":true}`), nil); err != nil {
		t.Fatal(err)
	}
	current = current.Add(2 * time.Hour)
	// The failed record expired: the key must be claimable again instead of
	// replaying the stale failure forever.
	retry, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || retry.Kind != ClaimOwner {
		t.Fatalf("expired failed record must free the key: %+v err=%v", retry, err)
	}

	// A pending record is governed by the lease, not expires_at: even with
	// its expiry stamp in the past, a heartbeat-fresh pending record stays
	// pending instead of being dropped or taken over.
	pendingKey := Key{RemoteSessionID: key.RemoteSessionID, PrincipalID: key.PrincipalID, Operation: key.Operation, Value: "key-pending"}
	if _, err := store.Claim(ctx, pendingKey, "sha256:first", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := store.Touch(ctx, pendingKey, "sha256:first"); err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Second)
	foreign := NewStore(store.db)
	foreign.now = store.now
	if claim, err := foreign.Claim(ctx, pendingKey, "sha256:first", time.Hour); err != nil || claim.Kind != ClaimPending {
		t.Fatalf("fresh-lease pending record must stay pending: %+v err=%v", claim, err)
	}
}

func TestStoreAbandonedRollbackLetsWaiterRetrySameKey(t *testing.T) {
	store, closeStore := testStore(t)
	defer closeStore()
	ctx := context.Background()
	key := testKey()

	if _, err := store.Claim(ctx, key, "sha256:first", time.Hour); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || waiting.Kind != ClaimWait {
		t.Fatalf("waiting=%+v err=%v", waiting, err)
	}
	if err := store.Abandon(ctx, key, "sha256:first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Wait(ctx, waiting, key); !errors.Is(err, ErrAbandoned) {
		t.Fatalf("waiter must learn the request was rolled back: err=%v", err)
	}
	// The key is free again: an identical retry starts from a clean slate.
	retry, err := store.Claim(ctx, key, "sha256:first", time.Hour)
	if err != nil || retry.Kind != ClaimOwner {
		t.Fatalf("retry after rollback=%+v err=%v", retry, err)
	}
}
