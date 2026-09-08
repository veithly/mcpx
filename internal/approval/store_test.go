package approval

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcpx/internal/auth"
	"mcpx/internal/remotesession"
	"mcpx/internal/state"
)

func TestConsumeIsExplicit(t *testing.T) {
	s := NewStore()
	id, err := s.Put(Pending{RemoteSessionID: "rs1", Tool: "terminal_exec", Command: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(id); !ok {
		t.Fatal("approval should remain pending until consume")
	}
	if _, ok := s.Consume(id); !ok {
		t.Fatal("consume should remove pending approval")
	}
	if _, ok := s.Get(id); ok {
		t.Fatal("consumed approval should not remain pending")
	}
}

func TestPutTake(t *testing.T) {
	s := NewStore()
	id, err := s.Put(Pending{RemoteSessionID: "rs1", Tool: "terminal_exec", Command: "x"})
	if err != nil || id == "" {
		t.Fatalf("put approval: id=%q err=%v", id, err)
	}
	p, ok := s.Take(id)
	if !ok || p.Command != "x" {
		t.Fatalf("%v %+v", ok, p)
	}
	if _, ok := s.Take(id); ok {
		t.Fatal("should be gone")
	}
}

func TestRemoteSessionApprovalSurvivesRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "mcpx.db")
	store, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: "approval-principal", Kind: "test", SubjectHash: "approval-subject"}
	created, err := remotesession.NewService(store.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "project", WorkspacePath: t.TempDir(), Label: "approval test",
	})
	if err != nil {
		t.Fatal(err)
	}
	approvalID, err := NewPersistentStore(store.DB()).Put(Pending{
		RemoteSessionID: created.Session.ID, PrincipalID: principal.ID, Tool: "command_execute", Summary: "run durable command",
		Command: "go test ./...", Purpose: "verify workspace", Scope: "workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persistent := NewPersistentStore(reopened.DB())
	listed := persistent.ListRemoteSession(created.Session.ID)
	if len(listed) != 1 || listed[0].ID != approvalID {
		t.Fatalf("listed=%+v", listed)
	}
	pending, ok := persistent.Take(approvalID)
	if !ok || pending.Summary != "run durable command" {
		t.Fatalf("take ok=%v pending=%+v", ok, pending)
	}
	if _, ok := persistent.Take(approvalID); ok {
		t.Fatal("approval must be one-shot")
	}
}

func TestPutDeduplicatesPendingContent(t *testing.T) {
	s := NewStore()
	base := Pending{Tool: "command_execute", Command: "git status", Purpose: "inspect", Scope: "workspace", RemoteSessionID: "rs1", PrincipalID: "p1"}
	id1, err := s.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("same pending content must reuse approval_id: %s != %s", id1, id2)
	}
	// 相同命令不同 purpose 仍复用：模型重试时可能改述 purpose
	idRephrased, err := s.Put(Pending{Tool: "command_execute", Command: "git status", Purpose: "rephrased intent", Scope: "workspace", RemoteSessionID: "rs1", PrincipalID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if idRephrased != id1 {
		t.Fatalf("same command with rephrased purpose must reuse approval_id: %s != %s", idRephrased, id1)
	}
	// 不同命令不聚合
	id3, err := s.Put(Pending{Tool: "command_execute", Command: "git diff", Purpose: "inspect", Scope: "workspace", RemoteSessionID: "rs1", PrincipalID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id1 {
		t.Fatal("different command must create a new approval")
	}
	// 不同会话不聚合
	id4, err := s.Put(Pending{Tool: "command_execute", Command: "git status", Purpose: "inspect", Scope: "workspace", RemoteSessionID: "rs2", PrincipalID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if id4 == id1 {
		t.Fatal("different remote session must create a new approval")
	}
	// consume（deny）后重试新建
	s.Consume(id1)
	id5, err := s.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	if id5 == id1 {
		t.Fatal("consumed approval must allow a fresh one")
	}
}

func TestPersistentPutDeduplicatesAcrossRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "mcpx.db")
	store, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: "approval-principal", Kind: "test", SubjectHash: "approval-subject"}
	created, err := remotesession.NewService(store.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "project", WorkspacePath: t.TempDir(), Label: "approval test",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := Pending{Tool: "command_execute", Command: "git status", Purpose: "inspect", Scope: "workspace",
		RemoteSessionID: created.Session.ID, PrincipalID: principal.ID}
	persistent := NewPersistentStore(store.DB())
	id1, err := persistent.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := persistent.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("same pending content must reuse approval_id: %s != %s", id1, id2)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// content_key 已落库：重启后对同内容 Put 仍返回同一 ID
	id3, err := NewPersistentStore(reopened.DB()).Put(base)
	if err != nil {
		t.Fatal(err)
	}
	if id3 != id1 {
		t.Fatalf("content_key must survive restart: %s != %s", id3, id1)
	}
}

func TestPutDeduplicationExpiresWithTTL(t *testing.T) {
	s := NewStore()
	base := Pending{Tool: "command_execute", Command: "git status", Purpose: "inspect", Scope: "workspace", RemoteSessionID: "rs1", PrincipalID: "p1"}
	id1, err := s.Put(base)
	if err != nil {
		t.Fatal(err)
	}
	expired := base
	expired.CreatedAt = time.Now().UTC().Add(-PendingTTL - time.Minute)
	expired.ID = ""
	id2, err := s.Put(expired)
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Fatal("expired approval must not deduplicate")
	}
}

func TestPersistentDeduplicationConsume(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "mcpx.db")
	store, err := state.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := auth.Principal{ID: "approval-principal", Kind: "test", SubjectHash: "approval-subject"}
	created, err := remotesession.NewService(store.DB()).Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "project", WorkspacePath: t.TempDir(), Label: "approval test",
	})
	if err != nil {
		t.Fatal(err)
	}
	persistent := NewPersistentStore(store.DB())

	// 同内容 command 两次 Put 同 ID；Consume 后第三次 Put 返回新 ID
	cmd := Pending{Tool: "command_execute", Command: "git status", Purpose: "inspect", Scope: "workspace",
		RemoteSessionID: created.Session.ID, PrincipalID: principal.ID}
	cmdID1, err := persistent.Put(cmd)
	if err != nil {
		t.Fatal(err)
	}
	cmdID2, err := persistent.Put(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if cmdID1 != cmdID2 {
		t.Fatalf("same command must reuse approval_id: %s != %s", cmdID1, cmdID2)
	}
	if _, ok := persistent.Consume(cmdID1); !ok {
		t.Fatal("consume should succeed")
	}
	cmdID3, err := persistent.Put(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if cmdID3 == cmdID1 {
		t.Fatal("consumed approval must allow a fresh one")
	}

	pending := persistent.ListRemoteSession(created.Session.ID)
	if len(pending) != 1 || pending[0].ID != cmdID3 {
		t.Fatalf("pending approvals = %+v, want only the fresh command approval", pending)
	}
}
