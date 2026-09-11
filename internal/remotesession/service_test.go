package remotesession

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcpx/internal/auth"
	"mcpx/internal/state"
)

func testService(t *testing.T) (*Service, *state.Store) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store.DB()), store
}

func testPrincipal(id string) auth.Principal {
	return auth.Principal{ID: id, Kind: "test", SubjectHash: id + "-hash"}
}

func TestCreateListGetAndIdempotency(t *testing.T) {
	service, _ := testService(t)
	owner := testPrincipal("owner")
	in := CreateInput{
		WorkspaceName: "mcpx", WorkspacePath: t.TempDir(), Label: "session",
		ClientRequestID: "create-1", ClientName: "client-a", ClientVersion: "1",
	}
	first, err := service.Create(context.Background(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first.Session.ID); err != nil {
		t.Fatalf("remote session id is not UUID: %q (%v)", first.Session.ID, err)
	}
	second, err := service.Create(context.Background(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Session.ID != second.Session.ID {
		t.Fatalf("idempotent result changed: %+v %+v", first, second)
	}
	if first.ResumeToken == "" || second.ResumeToken != "" || !second.ResumeTokenAlreadyIssued {
		t.Fatalf("one-time token contract violated: first=%+v second=%+v", first, second)
	}
	var cached string
	if err := service.db.QueryRow(`SELECT response_json FROM idempotency_records
		WHERE principal_id = ? AND client_request_id = ?`, owner.ID, in.ClientRequestID).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cached, first.ResumeToken) {
		t.Fatal("resume token was persisted in idempotency record")
	}
	list, err := service.List(context.Background(), owner, ListInput{Workspace: "mcpx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].ID != first.Session.ID {
		t.Fatalf("list: %+v", list)
	}
	got, err := service.Get(context.Background(), owner, first.Session.ID)
	if err != nil || got.Role != "owner" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
}

func TestHandoffAttachIsOneShotAndACLFiltered(t *testing.T) {
	service, _ := testService(t)
	owner := testPrincipal("owner")
	other := testPrincipal("other")
	created, err := service.Create(context.Background(), owner, CreateInput{WorkspaceName: "mcpx", WorkspacePath: t.TempDir(), Label: "handoff"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), other, created.Session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unauthorized get: %v", err)
	}
	handoff, err := service.Handoff(context.Background(), owner, created.Session.ID, "editor", "continue", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := service.Attach(context.Background(), other, handoff.HandoffToken, "client-b", "2")
	if err != nil {
		t.Fatal(err)
	}
	if attached.ID != created.Session.ID || attached.Role != "editor" {
		t.Fatalf("attached: %+v", attached)
	}
	if _, err := service.Attach(context.Background(), testPrincipal("third"), handoff.HandoffToken, "client-c", "1"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token should be consumed: %v", err)
	}
	list, err := service.List(context.Background(), other, ListInput{})
	if err != nil || len(list.Sessions) != 1 {
		t.Fatalf("attached list: %+v err=%v", list, err)
	}
}

func TestUpdateUsesOptimisticVersion(t *testing.T) {
	service, _ := testService(t)
	owner := testPrincipal("owner")
	created, err := service.Create(context.Background(), owner, CreateInput{WorkspaceName: "mcpx", WorkspacePath: t.TempDir(), Label: "before"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(context.Background(), owner, created.Session.ID, "after", "", "idle", created.Session.Version)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Label != "after" || updated.Status != "idle" || updated.Version != 2 {
		t.Fatalf("updated: %+v", updated)
	}
	if _, err := service.Update(context.Background(), owner, created.Session.ID, "stale", "", "", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestUpdateConflictCarriesCurrentVersionAndRetryHint(t *testing.T) {
	service, _ := testService(t)
	owner := testPrincipal("owner")
	created, err := service.Create(context.Background(), owner, CreateInput{WorkspaceName: "mcpx", WorkspacePath: t.TempDir(), Label: "before"})
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent writer advances the version behind the caller's back.
	if _, err := service.Update(context.Background(), owner, created.Session.ID, "racer", "", "idle", created.Session.Version); err != nil {
		t.Fatal(err)
	}
	_, err = service.Update(context.Background(), owner, created.Session.ID, "stale", "", "closed", created.Session.Version)
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want VersionConflictError, got %v", err)
	}
	if conflict.CurrentVersion != created.Session.Version+1 {
		t.Fatalf("current version=%d, want %d", conflict.CurrentVersion, created.Session.Version+1)
	}
	// The sentinel mapping in the tool layer keeps working.
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error must unwrap to ErrConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "re-read") || !strings.Contains(err.Error(), "current version is 2") {
		t.Fatalf("conflict message lacks retry guidance: %v", err)
	}
}

func TestSessionSurvivesStoreReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpx.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := testPrincipal("owner")
	created, err := NewService(store.DB()).Create(context.Background(), owner, CreateInput{WorkspaceName: "mcpx", WorkspacePath: dir, Label: "persist"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := NewService(reopened.DB()).Get(context.Background(), owner, created.Session.ID)
	if err != nil || got.Label != "persist" {
		t.Fatalf("reopened: %+v err=%v", got, err)
	}
}

func TestSetEnvironmentSnapshotUsesOptimisticVersion(t *testing.T) {
	service, _ := testService(t)
	owner := testPrincipal("owner")
	created, err := service.Create(context.Background(), owner, CreateInput{WorkspaceName: "mcpx", WorkspacePath: t.TempDir(), Label: "env"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.setEnvironmentSnapshotVersion(ctx, owner, created.Session.ID, "snap-a", created.Session.Version); err != nil {
		t.Fatal(err)
	}
	bound, err := service.Get(ctx, owner, created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.EnvironmentSnapshotID != "snap-a" || bound.Version != created.Session.Version+1 {
		t.Fatalf("bound=%+v", bound)
	}
	// A concurrent writer advances the version behind the caller's back.
	if _, err := service.Update(ctx, owner, created.Session.ID, "raced", "", "", bound.Version); err != nil {
		t.Fatal(err)
	}
	err = service.setEnvironmentSnapshotVersion(ctx, owner, created.Session.ID, "snap-b", bound.Version)
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale snapshot binding must yield VersionConflictError, got %v", err)
	}
	if conflict.CurrentVersion != bound.Version+1 {
		t.Fatalf("conflict version=%d, want %d", conflict.CurrentVersion, bound.Version+1)
	}
	// The failed binding must not clobber the winning snapshot.
	fresh, err := service.Get(ctx, owner, created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.EnvironmentSnapshotID != "snap-a" {
		t.Fatalf("stale write clobbered the snapshot binding: %+v", fresh)
	}
}

func TestSetEnvironmentSnapshotConcurrentWithUpdateHasNoFalseConflict(t *testing.T) {
	service, _ := testService(t)
	owner := testPrincipal("owner")
	created, err := service.Create(context.Background(), owner, CreateInput{WorkspaceName: "mcpx", WorkspacePath: t.TempDir(), Label: "concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const rounds = 15
	var wg sync.WaitGroup
	wg.Add(2)
	updateSuccesses := make(chan int, 1)
	snapshotSuccesses := make(chan int, 1)
	go func() {
		defer wg.Done()
		updateSuccesses <- driveVersionedWrites(t, rounds, func() error {
			current, err := service.Get(ctx, owner, created.Session.ID)
			if err != nil {
				return err
			}
			_, err = service.Update(ctx, owner, created.Session.ID, "label", "", "idle", current.Version)
			return err
		})
	}()
	go func() {
		defer wg.Done()
		snapshotSuccesses <- driveVersionedWrites(t, rounds, func() error {
			return service.SetEnvironmentSnapshot(ctx, owner, created.Session.ID, "snap-concurrent")
		})
	}()
	wg.Wait()
	updates, snapshots := <-updateSuccesses, <-snapshotSuccesses

	final, err := service.Get(ctx, owner, created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Every successful write bumps the version exactly once; a lost or
	// phantom bump would surface as a mismatch here.
	if want := created.Session.Version + updates + snapshots; final.Version != want {
		t.Fatalf("final version=%d, want 1+%d updates+%d snapshots=%d", final.Version, updates, snapshots, want)
	}
	if final.EnvironmentSnapshotID != "snap-concurrent" {
		t.Fatalf("snapshot binding lost under concurrency: %+v", final)
	}
}

// driveVersionedWrites retries optimistic conflicts so only genuine failures
// bubble up; it returns how many operations eventually succeeded.
func driveVersionedWrites(t *testing.T, rounds int, op func() error) int {
	t.Helper()
	successes := 0
	for i := 0; i < rounds; i++ {
		for attempt := 0; ; attempt++ {
			err := op()
			if err == nil {
				successes++
				break
			}
			if attempt > 200 {
				t.Fatalf("write never converged: %v", err)
			}
			var conflict *VersionConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("concurrent write produced a non-conflict error: %v", err)
			}
		}
	}
	return successes
}
