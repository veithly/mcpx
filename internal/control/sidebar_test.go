package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSidebarPreferencesPersistAndRejectStaleWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sidebar.sqlite")
	s, db := testStore(t, path)
	items := []SidebarItem{{Kind: "workspace", ID: "alpha", Workspace: "alpha", Pinned: true, Position: 2}, {Kind: "session", ID: "one", Workspace: "alpha", Pinned: true, Position: 1}}
	if err := s.SaveSidebar(ctx, 0, items); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSidebar(ctx, 0, []SidebarItem{{Kind: "session", ID: "one", Workspace: "alpha", DeletedAt: 10}}); !errors.Is(err, ErrSidebarConflict) {
		t.Fatalf("stale update=%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, _ = testStore(t, path)
	state, err := s.Sidebar(ctx)
	if err != nil || state.Revision != 1 || len(state.Items) != 2 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	for _, item := range state.Items {
		if !item.Pinned || item.DeletedAt != 0 {
			t.Fatalf("preferences overwritten: %+v", item)
		}
	}
}
func TestSidebarRestoringWorkspaceDoesNotRestoreDeletedConversations(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.SaveSidebar(ctx, 0, []SidebarItem{{Kind: "workspace", ID: "alpha", Workspace: "alpha", DeletedAt: 1}, {Kind: "session", ID: "one", Workspace: "alpha", DeletedAt: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreWorkspace(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if deleted, _ := s.Deleted(ctx, "alpha", ""); deleted {
		t.Fatal("workspace not restored")
	}
	if deleted, _ := s.Deleted(ctx, "alpha", "one"); !deleted {
		t.Fatal("deleted conversation resurrected")
	}
	if deleted, _ := s.Deleted(ctx, "beta", "other"); deleted {
		t.Fatal("deletion leaked to another workspace")
	}
}
func TestSidebarInvalidBatchRollsBackRevision(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.SaveSidebar(ctx, 0, []SidebarItem{{Kind: "session", ID: "one", Workspace: "alpha"}, {Kind: "invalid", ID: "two", Workspace: "alpha"}}); err == nil {
		t.Fatal("bad kind accepted")
	}
	state, err := s.Sidebar(ctx)
	if err != nil || state.Revision != 0 || len(state.Items) != 0 {
		t.Fatalf("partial write leaked: %+v %v", state, err)
	}
}
