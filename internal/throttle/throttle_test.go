//go:build !windows

package throttle

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestBackgroundLowersPriorityAndProcs runs Background in a child process
// (niceness cannot be raised back) and reads its effect.
func TestBackgroundLowersPriorityAndProcs(t *testing.T) {
	if os.Getenv("THROTTLE_CHILD") == "1" {
		Background()
		p, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
		if err != nil {
			t.Fatal(err)
		}
		if runtimeLinux {
			p = 20 - p
		}
		os.Stdout.WriteString("nice=" + strconv.Itoa(p) + " procs=" + strconv.Itoa(runtime.GOMAXPROCS(0)) + "\n")
		return
	}
	run := func(env ...string) string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundLowersPriorityAndProcs$")
		cmd.Env = append(os.Environ(), append([]string{"THROTTLE_CHILD=1", "GOMAXPROCS="}, env...)...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("child: %v %s", err, out)
		}
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "nice=") {
				return l
			}
		}
		t.Fatalf("no result line in %q", out)
		return ""
	}
	want := "nice=" + strconv.Itoa(max(Nice, currentNice(t))) + " procs=" + strconv.Itoa(Procs())
	if got := run(); got != want {
		t.Fatalf("Background: got %q, want %q", got, want)
	}
	if got := run("MUNINN_FULL_SPEED=1"); strings.HasPrefix(got, "nice="+strconv.Itoa(Nice)+" ") && currentNice(t) < Nice {
		t.Fatalf("MUNINN_FULL_SPEED=1 still throttled: %q", got)
	}
}

func currentNice(t *testing.T) int {
	p, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeLinux {
		p = 20 - p
	}
	return p
}
