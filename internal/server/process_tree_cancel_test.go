package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcpx/internal/winproc"
)

func TestReviewProcessTreeFixtureChild(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--review-process-tree" {
			continue
		}
		role, ready, late := os.Args[i+1], os.Args[i+2], os.Args[i+3]
		if role == "grandchild" {
			if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
				os.Exit(2)
			}
			time.Sleep(1500 * time.Millisecond)
			if err := os.WriteFile(late, []byte("late-grandchild"), 0600); err != nil {
				os.Exit(3)
			}
			os.Exit(0)
		}
		executable, err := os.Executable()
		if err != nil {
			os.Exit(4)
		}
		child := exec.Command(executable, "-test.run=^TestReviewProcessTreeFixtureChild$", "--", "--review-process-tree", "grandchild", ready, late)
		winproc.ConfigureNoWindow(child)
		if role == "inherited" {
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
		}
		if role == "parent-exits" {
			if err := child.Start(); err != nil {
				os.Exit(6)
			}
			os.Exit(0)
		}
		if err := child.Run(); err != nil {
			os.Exit(5)
		}
		os.Exit(0)
	}
}

func TestReviewCancelStopsGrandchildEffects(t *testing.T) {
	for _, mode := range []string{"redirected", "inherited"} {
		t.Run(mode, func(t *testing.T) {
			rt, remote, root, call := programmingFixture(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ready, late := filepath.Join(root, "ready"), filepath.Join(root, "late")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			command := quote(executable) + " -test.run=^TestReviewProcessTreeFixtureChild$ -- --review-process-tree " + mode + " " + quote(ready) + " " + quote(late)
			result := call("exec_command", map[string]any{"cmd": command, "shell": "/bin/sh", "login": false, "tty": true, "yield_time_ms": 250})
			if result.IsError {
				t.Fatal(result)
			}
			deadline := time.Now().Add(time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("grandchild did not start")
				}
				time.Sleep(5 * time.Millisecond)
			}
			rt.processMu.Lock()
			client := rt.processClients[remote]
			rt.processMu.Unlock()
			if client == nil {
				t.Fatal("missing upstream client")
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1600 * time.Millisecond)
			if _, err := os.Stat(late); !os.IsNotExist(err) {
				t.Fatal("Native process close left grandchild effects")
			}
		})
	}
}
