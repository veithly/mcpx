package control

import (
	"context"
	"testing"
)

func TestSidebarDeletionCancelsPendingRequestsWithoutFakingAcknowledgement(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	one := queue(t, s, "alpha", "one", "session request", "one")
	global := queue(t, s, "alpha", "", "workspace request", "global")
	queue(t, s, "beta", "two", "other project request", "other")
	if _, err := s.Deliver(ctx, "alpha", "one", "call"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSidebar(ctx, 0, []SidebarItem{{Kind: "session", ID: "one", Workspace: "alpha", DeletedAt: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, "alpha", "one", "steer", "late request", "late"); err == nil {
		t.Fatal("request admitted after conversation deletion")
	}
	if err := s.Acknowledge(ctx, "alpha", "one", []string{one.ID}); err == nil {
		t.Fatal("cancelled request accepted as acknowledged")
	}
	requests, err := s.List(ctx, "alpha", "one")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range requests {
		if r.ID == one.ID && (r.Status != "cancelled" || r.AcknowledgedAt != 0) {
			t.Fatalf("cancellation faked acknowledgement: %+v", r)
		}
	}
	remaining, err := s.Deliver(ctx, "alpha", "another", "call")
	if err != nil || len(remaining) != 1 || remaining[0].ID != global.ID {
		t.Fatalf("conversation removal cancelled workspace-level request: %+v %v", remaining, err)
	}
	if err := s.SaveSidebar(ctx, 1, []SidebarItem{{Kind: "workspace", ID: "alpha", Workspace: "alpha", DeletedAt: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, "alpha", "", "steer", "late global", "late-global"); err == nil {
		t.Fatal("request admitted after workspace deletion")
	}
	if err := s.RestoreWorkspace(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	remaining, err = s.Deliver(ctx, "alpha", "new-session", "later")
	if err != nil || len(remaining) != 0 {
		t.Fatalf("restoring project replayed old requests: %+v %v", remaining, err)
	}
	replay, err := s.Enqueue(ctx, "alpha", "", "steer", "workspace request", "global")
	if err != nil || replay.Status != "cancelled" || !replay.Replayed {
		t.Fatalf("cancelled idempotent request recreated: %+v %v", replay, err)
	}
	remaining, err = s.Deliver(ctx, "beta", "two", "other-call")
	if err != nil || len(remaining) != 1 {
		t.Fatal("deletion affected another project")
	}
}
