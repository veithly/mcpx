package server

import (
	"os"
	"os/exec"
	"strconv"
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

