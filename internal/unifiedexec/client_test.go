//go:build !windows

package unifiedexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// Fixtures are this Go test binary, never Node, Codex or an API.
func TestExecFixture(t *testing.T) {
	if os.Getenv("MCPX_EXEC_FIXTURE") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(90)
	}
	switch args[1] {
	case "eof":
		b, err := io.ReadAll(os.Stdin)
		if err != nil || len(b) != 0 {
			os.Exit(91)
		}
		fmt.Print("stdin-closed")
	case "wait":
		fmt.Print("ready\n")
		time.Sleep(10 * time.Minute)
	case "echo":
		fmt.Print("ready\n")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Printf("got:%s", line)
	case "split":
		_, _ = os.Stdout.Write([]byte{0xe4, 0xb8})
		time.Sleep(600 * time.Millisecond)
		_, _ = os.Stdout.Write([]byte{0xad})
	case "cancel":
		f, _ := os.OpenFile(args[2], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		_, _ = f.WriteString("once\n")
		_ = f.Close()
		fmt.Print("before\n")
		time.Sleep(600 * time.Millisecond)
		fmt.Print("after\n")
	case "output":
		fmt.Print("HEAD\n")
		for i := 0; i < 40000; i++ {
			fmt.Print("中文输出0123456789\n")
		}
		fmt.Print("TAIL\n")
	case "sequence":
		for i := 0; i < 40; i++ {
			fmt.Printf("record-%03d\n", i)
			time.Sleep(15 * time.Millisecond)
		}
	case "tree", "orphan":
		child := exec.Command(os.Args[0], "-test.run=^TestExecFixture$", "--", "branch", args[2])
		child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(92)
		}
		for {
			if _, err := os.Stat(args[2]); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		fmt.Printf("child:%d\n", child.Process.Pid)
		if args[1] == "orphan" {
			os.Exit(0)
		}
		time.Sleep(10 * time.Minute)
	case "branch":
		signal.Ignore(syscall.SIGHUP) // Require explicit tree cleanup, not automatic PTY hangup.
		child := exec.Command(os.Args[0], "-test.run=^TestExecFixture$", "--", "wait")
		child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(93)
		}
		_ = os.WriteFile(args[2], []byte(fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)), 0600)
		time.Sleep(10 * time.Minute)
	default:
		os.Exit(94)
	}
	os.Exit(0)
}

func testClient(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	env := []string{"PATH=/no-external-tools", "SHELL=/bin/sh", "HOME=" + dir, "MCPX_EXEC_FIXTURE=1", "GORACE=atexit_sleep_ms=0"}
	c, err := New(context.Background(), Options{WorkDir: dir, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func fixture(t *testing.T, mode string, args ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	words := append([]string{exe, "-test.run=^TestExecFixture$", "--", mode}, args...)
	for i, w := range words {
		words[i] = "'" + strings.ReplaceAll(w, "'", "'\"'\"'") + "'"
	}
	return "exec " + strings.Join(words, " ")
}

func run(t *testing.T, c *Client, cmd string, tty bool, yield int) ExecResult {
	t.Helper()
	login := false
	r, err := c.Exec(context.Background(), ExecRequest{Cmd: cmd, Login: &login, TTY: tty, YieldTimeMS: yield})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNativeNoCodexAndStrictContract(t *testing.T) {
	c := testClient(t)
	r := run(t, c, "printf native; printf stderr >&2; exit 7", false, 1000)
	if r.ExitCode == nil || *r.ExitCode != 7 || r.SessionID != nil || r.Output != "nativestderr" {
		t.Fatalf("%+v", r)
	}
	r = run(t, c, fixture(t, "eof"), false, 1000)
	if r.Output != "stdin-closed" || r.ExitCode == nil {
		t.Fatalf("%+v", r)
	}
	for _, s := range []string{"null", "{\"cmd\":\"x\",\"binary\":\"codex\"}", "{\"cmd\":\"x\"} {}"} {
		var req ExecRequest
		if json.Unmarshal([]byte(s), &req) == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	for _, req := range []ExecRequest{{Cmd: " "}, {Cmd: "x", YieldTimeMS: -1}, {Cmd: "x", MaxOutputTokens: -1}, {Cmd: "x", Shell: "fish"}, {Cmd: "x", WorkDir: "missing"}} {
		if _, err := c.Exec(context.Background(), req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
	if c.HasRunning() || len(c.sessions) != 0 {
		t.Fatal("failed starts leaked sessions")
	}
}

func TestNativeSplitUTF8AcrossPollAndCancellation(t *testing.T) {
	c := testClient(t)
	login := false
	r, err := c.Exec(context.Background(), ExecRequest{Cmd: fixture(t, "split"), Login: &login, YieldTimeMS: 250, MaxOutputTokens: 1})
	if err != nil || r.SessionID == nil || r.Output != "" {
		t.Fatalf("first: %+v %v", r, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	r2, err := c.Write(ctx, WriteRequest{SessionID: *r.SessionID, MaxOutputTokens: 1})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || r2.Output != "" || r2.SessionID == nil {
		t.Fatalf("cancel: %+v %v", r2, err)
	}
	r3, err := c.Write(context.Background(), WriteRequest{SessionID: *r.SessionID, MaxOutputTokens: 1})
	if err != nil || r3.Output != "中" || r3.ExitCode == nil || !utf8.ValidString(r3.Output) {
		t.Fatalf("last: %+v %v", r3, err)
	}
}

func TestNativeCancelRetainsOutputAndNeverReplays(t *testing.T) {
	c := testClient(t)
	path := filepath.Join(c.workDir, "runs")
	login := false
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	r, err := c.Exec(ctx, ExecRequest{Cmd: fixture(t, "cancel", path), Login: &login})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || r.SessionID == nil || r.Output != "before\n" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = c.Write(context.Background(), WriteRequest{SessionID: *r.SessionID})
	if err != nil || r.Output != "after\n" || r.ExitCode == nil {
		t.Fatalf("%+v %v", r, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "once\n" {
		t.Fatalf("command replayed: %q", b)
	}
}

func TestNativePTYAndConcurrentPollWrite(t *testing.T) {
	c := testClient(t)
	r := run(t, c, "[ -t 0 ] && [ -t 1 ] && [ -t 2 ] && printf tty", true, 1000)
	if r.ExitCode == nil || *r.ExitCode != 0 || r.Output != "tty" {
		t.Fatalf("no real terminal: %+v", r)
	}
	r = run(t, c, fixture(t, "echo"), true, 250)
	if r.SessionID == nil {
		t.Fatalf("%+v", r)
	}
	id := *r.SessionID
	type answer struct {
		result ExecResult
		err    error
	}
	ch := make(chan answer, 1)
	c.mu.Lock()
	s := c.sessions[id]
	c.mu.Unlock()
	go func() {
		r, e := c.Write(context.Background(), WriteRequest{SessionID: id, YieldTimeMS: 300000})
		ch <- answer{r, e}
	}()
	deadline := time.Now().Add(time.Second)
	for len(s.readGate) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	wr, err := c.Write(context.Background(), WriteRequest{SessionID: id, Chars: "hello\n", YieldTimeMS: 250})
	if time.Since(start) > time.Second {
		t.Fatal("poll blocked stdin")
	}
	var polled answer
	select {
	case polled = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("poll never saw stdin")
	}
	combined := wr.Output + polled.result.Output
	if strings.Count(combined, "got:hello") != 1 {
		t.Fatalf("outputs %q; write=%v poll=%v", combined, err, polled.err)
	}
}

func TestNativePTYInterruptAndPipeRejectsInput(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(strconv.FormatBool(tty), func(t *testing.T) {
			c := testClient(t)
			r := run(t, c, fixture(t, "wait"), tty, 250)
			if r.SessionID == nil {
				t.Fatal(r)
			}
			r, err := c.Write(context.Background(), WriteRequest{SessionID: *r.SessionID, Chars: "\x03", YieldTimeMS: 1000})
			if !tty {
				if err == nil || !strings.Contains(err.Error(), "stdin is closed") {
					t.Fatal(err)
				}
				return
			}
			if err != nil || r.ExitCode == nil || *r.ExitCode != 130 {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestNativeOutputBudget(t *testing.T) {
	c := testClient(t)
	login := false
	r, err := c.Exec(context.Background(), ExecRequest{Cmd: fixture(t, "output"), Login: &login, MaxOutputTokens: 32})
	if err != nil || r.ExitCode == nil || r.OriginalTokenCount == nil || *r.OriginalTokenCount < 100000 || len(r.Output) > 300 || !strings.HasPrefix(r.Output, "HEAD") || !strings.HasSuffix(r.Output, "TAIL\n") || !utf8.ValidString(r.Output) {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestNativeSessionOwnershipConcurrentExecAndCleanup(t *testing.T) {
	c, other := testClient(t), testClient(t)
	r := run(t, c, fixture(t, "wait"), false, 250)
	if r.SessionID == nil || !c.HasRunning() || len(c.ActiveSessions()) != 1 {
		t.Fatal(r)
	}
	if _, err := other.Write(context.Background(), WriteRequest{SessionID: *r.SessionID}); err == nil {
		t.Fatal("cross-client handle accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			login := false
			r, e := other.Exec(context.Background(), ExecRequest{Cmd: fmt.Sprintf("printf task%d", i), Login: &login})
			if e != nil || r.Output != fmt.Sprintf("task%d", i) {
				t.Errorf("%+v %v", r, e)
			}
		}(i)
	}
	wg.Wait()
	if other.HasRunning() || len(other.sessions) != 0 {
		t.Fatal("sessions leaked")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.HasRunning() || len(c.sessions) != 0 {
		t.Fatal("close leaked sessions")
	}
	if _, err := c.Exec(context.Background(), ExecRequest{Cmd: "true"}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestNativeCloseAndParentExitKillGrandchildren(t *testing.T) {
	for _, mode := range []string{"tree", "orphan"} {
		for _, tty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-tty-%t", mode, tty), func(t *testing.T) {
				c := testClient(t)
				pids := filepath.Join(c.workDir, "pids")
				r := run(t, c, fixture(t, mode, pids), tty, 500)
				if mode == "tree" && r.SessionID == nil {
					t.Fatalf("%+v", r)
				}
				start := time.Now()
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				if time.Since(start) > 2*time.Second {
					t.Fatal("Close blocked on inherited pipe")
				}
				data, err := os.ReadFile(pids)
				if err != nil {
					t.Fatal(err)
				}
				for _, field := range strings.Fields(string(data)) {
					pid, _ := strconv.Atoi(field)
					deadline := time.Now().Add(2 * time.Second)
					for processAlive(pid) && time.Now().Before(deadline) {
						time.Sleep(10 * time.Millisecond)
					}
					if processAlive(pid) {
						t.Errorf("descendant %d survived %s", pid, mode)
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
		}
	}
}

func processAlive(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return false
	}
	// Linux CI may not reap orphan zombies immediately; zombies cannot run.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if end := strings.LastIndex(string(data), ")"); end >= 0 && strings.HasPrefix(string(data)[end+1:], " Z") {
			return false
		}
	}
	return true
}

func TestNativeConcurrentPollNoDuplicateOutput(t *testing.T) {
	c := testClient(t)
	r := run(t, c, fixture(t, "sequence"), false, 250)
	if r.SessionID == nil {
		t.Fatal(r)
	}
	id := *r.SessionID
	outputs := make(chan string, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _ := c.Write(context.Background(), WriteRequest{SessionID: id})
			outputs <- v.Output
		}()
	}
	wg.Wait()
	close(outputs)
	all := r.Output
	for output := range outputs {
		all += output
	}
	for i := 0; i < 40; i++ {
		if strings.Count(all, fmt.Sprintf("record-%03d\n", i)) != 1 {
			t.Fatalf("record %d duplicated/lost: %q", i, all)
		}
	}
}

func TestNativeEnvShellWorkdirAndLogin(t *testing.T) {
	c := testClient(t)
	if c.shell != "/bin/sh" {
		t.Fatal(c.shell)
	}
	if err := os.Mkdir(filepath.Join(c.workDir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	login := false
	r, err := c.Exec(context.Background(), ExecRequest{Cmd: "printf '%s|%s' \"$PATH\" \"$PWD\"", WorkDir: "sub", Login: &login})
	if err != nil || !strings.HasPrefix(r.Output, "/no-external-tools|") || !strings.HasSuffix(r.Output, "/sub") {
		t.Fatalf("%+v %v", r, err)
	}
	if err := os.WriteFile(filepath.Join(c.workDir, ".bash_profile"), []byte("export MCPX_LOGIN_MARK=loaded\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []bool{false, true} {
		r, err = c.Exec(context.Background(), ExecRequest{Cmd: "printf '%s' \"$MCPX_LOGIN_MARK\"", Shell: "/bin/bash", Login: &flag})
		expect := ""
		if flag {
			expect = "loaded"
		}
		if err != nil || r.Output != expect {
			t.Fatalf("login=%v %+v %v", flag, r, err)
		}
	}
}

func TestNativeCloseUnblocksPollAndPendingInput(t *testing.T) {
	c := testClient(t)
	r := run(t, c, fixture(t, "wait"), true, 250)
	if r.SessionID == nil {
		t.Fatal(r)
	}
	id := *r.SessionID
	c.mu.Lock()
	s := c.sessions[id]
	c.mu.Unlock()
	polled := make(chan struct{})
	written := make(chan struct{})
	go func() {
		defer close(polled)
		_, _ = c.Write(context.Background(), WriteRequest{SessionID: id, YieldTimeMS: 300000})
	}()
	deadline := time.Now().Add(time.Second)
	for len(s.readGate) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	go func() {
		defer close(written)
		_, _ = c.Write(context.Background(), WriteRequest{SessionID: id, Chars: strings.Repeat("input\n", 200000), YieldTimeMS: 30000})
	}()
	deadline = time.Now().Add(time.Second)
	for len(s.writeGate) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan struct{}{polled, written} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Close left blocked I/O")
		}
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Close waited for a poll or stdin deadline")
	}
}

func TestHeadTailMemoryCapAndUnicode(t *testing.T) {
	b := newOutputBuffer(outputBytesCap / 4)
	b.append([]byte("HEAD\n"))
	chunk := []byte(strings.Repeat("中文", 8192))
	for i := 0; i < 100; i++ {
		b.append(chunk)
	}
	b.append([]byte("TAIL\n"))
	if len(b.head)+len(b.tail) > outputBytesCap {
		t.Fatal("unbounded retained output")
	}
	output, count := b.result()
	if !utf8.ValidString(output) || strings.ContainsRune(output, '�') || !strings.HasPrefix(output, "HEAD\n") || !strings.HasSuffix(output, "TAIL\n") || count == nil {
		t.Fatal("invalid head/tail truncation")
	}
	small := newOutputBuffer(10)
	small.merge(b)
	output, count = small.result()
	if !utf8.ValidString(output) || strings.ContainsRune(output, '�') || count == nil || *count != (b.total+3)/4 {
		t.Fatalf("merge lost byte count/rune: %q %v", output, count)
	}
}
