package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"mcpx/internal/control"
)

// Opt-in manual browser QA of the embedded production React bundle. The helper
// creates only temporary projects/database and never uses the operator's home.
func TestConsoleBrowserQA(t *testing.T) {
	if os.Getenv("MCPX_BROWSER_QA") != "1" {
		t.Skip("set MCPX_BROWSER_QA=1 for isolated browser verification")
	}
	rt := newWorkspaceRuntime(t, "alpha", "beta", "gamma")
	ctx := context.Background()
	create := func(ws, label string) string {
		id := consoleRemote(t, rt, ws)
		if _, err := rt.state.DB().Exec(`UPDATE remote_sessions SET label=?,description=? WHERE id=?`, label, "浏览器验收临时数据，不是生产任务", id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	active := create("alpha", "正在执行的验收任务")
	pinned := create("alpha", "置顶参考会话")
	create("alpha", "可排序会话 A")
	create("alpha", "可排序会话 B")
	create("alpha", "可删除会话")
	create("beta", "Beta 历史会话")
	if err := rt.control.SaveSidebar(ctx, 0, []control.SidebarItem{{Kind: "session", ID: pinned, Workspace: "alpha", Pinned: true}}); err != nil {
		t.Fatal(err)
	}
	ws, _ := rt.reg.Get("alpha")
	task, err := rt.tasks.StartRemote(ctx, active, "alpha", ws.Path, "sleep 480")
	if err != nil {
		t.Fatal(err)
	}
	defer task.Kill()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.Handle(consoleRoot, rt.consoleHandler())
	mux.HandleFunc("POST /__qa_finish", func(w http.ResponseWriter, r *http.Request) {
		select {
		case done <- struct{}{}:
		default:
		}
		w.WriteHeader(204)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(listener)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	fmt.Printf("MCPX_BROWSER_QA_URL=http://%s%s\n", listener.Addr(), consoleRoot)
	select {
	case <-done:
	case <-time.After(8 * time.Minute):
		t.Log("browser QA fixture expired")
	}
}
