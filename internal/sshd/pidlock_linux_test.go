package sshd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPidLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshd.pid")
	if _, running := ProbePid(path); running {
		t.Fatal("running with no pid file")
	}
	l, err := LockPid(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid file = %q", b)
	}
	pid, running := ProbePid(path)
	if !running || pid != os.Getpid() {
		t.Fatalf("ProbePid = %d %v, want our pid and running", pid, running)
	}
	// A second server in this process: flock locks are per open file, so
	// this is the same test as a second process.
	if _, err := LockPid(path); !errors.Is(err, ErrRunning) {
		t.Fatalf("second LockPid: %v, want ErrRunning", err)
	}
	l.Release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Release left the pid file")
	}
	if _, running := ProbePid(path); running {
		t.Fatal("running after Release")
	}
}

// A pid file left by a crash (no lock) is not a running server, even if its pid
// is alive.
func TestPidLockStaleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshd.pid")
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep command:", err)
	}
	defer cmd.Process.Kill()
	os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	if _, running := ProbePid(path); running {
		t.Fatal("an unlocked pid file counts as running")
	}
	l, err := LockPid(path)
	if err != nil {
		t.Fatalf("LockPid over a stale file: %v", err)
	}
	l.Release()
}
