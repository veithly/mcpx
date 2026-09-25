package terminal

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestDaemonPATHReachesChildExecutables(t *testing.T) {
	if runtime.GOOS != "darwin" || (!executableFile("/opt/homebrew/bin/node") && !executableFile("/usr/local/bin/node")) {
		t.Skip("requires a macOS package-manager Node installation")
	}
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	for _, executable := range []string{"node", "/usr/bin/env"} {
		t.Run(executable, func(t *testing.T) {
			args := []string{"-e", `const {spawnSync}=require('child_process');const r=spawnSync('node',['-e',"process.stdout.write('child-ok')"],{encoding:'utf8'});process.stdout.write(r.stdout||'');process.stderr.write(r.error?String(r.error):r.stderr||'');process.exit(r.status??1)`}
			if executable == "/usr/bin/env" {
				args = append([]string{"node"}, args...)
			}
			task, err := NewTaskManager().StartRemoteProcessWithObservationContext("req", "call", "execute", "session", "demo", t.TempDir(), "child PATH probe", ProcessSpec{Executable: executable, Args: args, WallLimit: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if !task.Wait(ctx) {
				t.Fatal("process did not finish")
			}
			out, _ := task.LogsFor("stdout", 0)
			stderr, _ := task.LogsFor("stderr", 0)
			if out != "child-ok" {
				t.Fatalf("child executable unavailable: stdout=%q stderr=%q status=%v", out, stderr, task.StatusView())
			}
		})
	}
}
