package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T, path string) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		t.Fatal(err)
	}
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return s, db
}
func newTestStore(t *testing.T) *Store {
	s, _ := testStore(t, filepath.Join(t.TempDir(), "control.sqlite"))
	return s
}
func queue(t *testing.T, s *Store, ws, session, body, key string) Request {
	t.Helper()
	item, err := s.Enqueue(context.Background(), ws, session, "steer", body, key)
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func TestRequestsPersistAndRepeatUntilAcknowledged(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	s, db := testStore(t, path)
	item := queue(t, s, "alpha", "s1", "请先跑测试，再继续", "key-1")
	for _, call := range []string{"call-1", "call-2"} {
		out, err := s.Deliver(ctx, "alpha", "s1", call)
		if err != nil || len(out) != 1 || out[0].ID != item.ID {
			t.Fatalf("delivery=%+v err=%v", out, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, _ = testStore(t, path)
	out, err := s.List(ctx, "alpha", "s1")
	if err != nil || len(out) != 1 || out[0].Status != "delivered" {
		t.Fatalf("persisted=%+v err=%v", out, err)
	}
	if err = s.Acknowledge(ctx, "alpha", "s1", []string{item.ID}); err != nil {
		t.Fatal(err)
	}
	if err = s.Acknowledge(ctx, "alpha", "s1", []string{item.ID}); err != nil {
		t.Fatalf("repeated acknowledgement must be safe: %v", err)
	}
	out, err = s.Deliver(ctx, "alpha", "s1", "call-3")
	if err != nil || len(out) != 0 {
		t.Fatalf("acknowledged request replayed: %+v %v", out, err)
	}
	out, _ = s.List(ctx, "alpha", "s1")
	if out[0].Status != "acknowledged" || out[0].AcknowledgedAt == 0 {
		t.Fatalf("status=%+v", out)
	}
}
func TestRequestIdempotencyIsContentBound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	first := queue(t, s, "alpha", "s1", "hello", "key")
	second := queue(t, s, "alpha", "s1", "hello", "key")
	if second.ID != first.ID || !second.Replayed {
		t.Fatalf("not an idempotent replay: %+v", second)
	}
	for _, pair := range [][2]string{{"s1", "changed"}, {"s2", "hello"}} {
		if _, err := s.Enqueue(ctx, "alpha", pair[0], "steer", pair[1], "key"); !errors.Is(err, ErrConflict) {
			t.Fatalf("wanted conflict: %v", err)
		}
	}
	other := queue(t, s, "beta", "s1", "hello", "key")
	if other.ID == first.ID {
		t.Fatal("workspace keys were conflated")
	}
}
func TestDeliveryAndAcknowledgementStayInScope(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	a := queue(t, s, "alpha", "s1", "private", "a")
	queue(t, s, "alpha", "s2", "other session", "b")
	queue(t, s, "beta", "s1", "other workspace", "c")
	shared := queue(t, s, "alpha", "", "workspace request", "d")
	if err := s.Acknowledge(ctx, "alpha", "s1", []string{a.ID}); err == nil {
		t.Fatal("undelivered request was acknowledged")
	}
	delivered, err := s.Deliver(ctx, "alpha", "s1", "call")
	if err != nil || len(delivered) != 2 {
		t.Fatalf("delivery=%+v err=%v", delivered, err)
	}
	if err = s.Acknowledge(ctx, "beta", "s1", []string{a.ID}); err == nil {
		t.Fatal("foreign workspace acknowledgement allowed")
	}
	if err = s.Acknowledge(ctx, "alpha", "s2", []string{shared.ID}); err == nil {
		t.Fatal("shared request acknowledged by session without a receipt")
	}
	if err = s.Acknowledge(ctx, "alpha", "s1", []string{a.ID, shared.ID}); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.Deliver(ctx, "alpha", "s2", "next")
	if err != nil || len(remaining) != 1 || remaining[0].Body != "other session" {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
	}
}
func TestAcknowledgementBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	item := queue(t, s, "alpha", "s1", "hello", "key")
	if _, err := s.Deliver(ctx, "alpha", "s1", "call"); err != nil {
		t.Fatal(err)
	}
	if err := s.Acknowledge(ctx, "alpha", "s1", []string{item.ID, "invalid"}); err == nil {
		t.Fatal("invalid ID accepted")
	}
	out, err := s.Deliver(ctx, "alpha", "s1", "retry")
	if err != nil || len(out) != 1 {
		t.Fatalf("partial acknowledgement leaked: %+v %v", out, err)
	}
}
func TestControlPolicyDefaultsAndExactApprovalDigest(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if mode, err := s.Mode(ctx, "alpha"); err != nil || mode != Approval {
		t.Fatalf("mode=%s err=%v", mode, err)
	}
	if err := s.SetMode(ctx, "alpha", FullAccess); err != nil {
		t.Fatal(err)
	}
	if mode, _ := s.Mode(ctx, "beta"); mode != Approval {
		t.Fatal("workspace access leaked")
	}
	if err := s.SetMode(ctx, "alpha", "unknown"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if err := s.Decide(ctx, "pending-a", "digest-a", "approved"); err != nil {
		t.Fatal(err)
	}
	if decision, _ := s.Decision(ctx, "pending-a", "digest-b"); decision != "" {
		t.Fatal("approval applied to different command digest")
	}
	if err := s.Decide(ctx, "pending-a", "digest-b", "denied"); err == nil {
		t.Fatal("pending ID rebound to different digest")
	}
	if err := s.Decide(ctx, "pending-a", "digest-a", "denied"); err != nil {
		t.Fatal(err)
	}
	if decision, _ := s.Decision(ctx, "pending-a", "digest-a"); decision != "denied" {
		t.Fatal("latest operator decision missing")
	}
}
func TestRequestLimits(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, body := range []string{" ", strings.Repeat("中", 2667), string([]byte{0xff})} {
		if _, err := s.Enqueue(ctx, "alpha", "s1", "steer", body, "bad"); err == nil {
			t.Fatal("invalid body accepted")
		}
	}
	for i := 0; i < 200; i++ {
		queue(t, s, "alpha", "s1", "request", fmt.Sprintf("key-%03d", i))
	}
	if _, err := s.Enqueue(ctx, "alpha", "s1", "steer", "overflow", "overflow"); err == nil {
		t.Fatal("unbounded pending queue")
	}
	out, err := s.Deliver(ctx, "alpha", "s1", "call")
	if err != nil || len(out) != DeliveryLimit {
		t.Fatalf("delivery limit=%d err=%v", len(out), err)
	}
}
func TestConcurrentEnqueueKeepsOneLogicalRequest(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := s.Enqueue(ctx, "alpha", "s1", "steer", "same body", "same-key")
			if err != nil {
				errs <- err
			} else {
				ids <- item.ID
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Error(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("logical requests=%d", len(seen))
	}
}
