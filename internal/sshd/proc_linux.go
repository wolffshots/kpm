package sshd

import (
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/creack/pty"
)

// startPTY starts cmd on a new PTY. pty.Start makes the child a session leader
// with the PTY as its controlling terminal.
func startPTY(cmd *exec.Cmd, rows, cols uint16) (*os.File, error) {
	return pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
}

// setPTYSize applies a window change.
func setPTYSize(f *os.File, rows, cols uint16) {
	pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols})
}

// newProcessGroup puts a command without a PTY into its own process group, so
// hangup reaches its children too.
func newProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// hangup sends SIGHUP to the process group of pid.
func hangup(pid int) {
	if pid > 0 {
		syscall.Kill(-pid, syscall.SIGHUP)
	}
}

// EnsureDevpts mounts devpts on /dev/pts when nothing is mounted there. A PTY
// needs it. The stock Kobo boot does not always mount it (NiLuJe usbnet mounts
// it for its telnet and Dropbear). It returns a note for the log, or "".
func EnsureDevpts() string {
	if b, err := os.ReadFile("/proc/mounts"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[1] == "/dev/pts" && f[2] == "devpts" {
				return ""
			}
		}
	}
	if err := os.MkdirAll("/dev/pts", 0o755); err != nil {
		return "devpts: " + err.Error()
	}
	if err := syscall.Mount("devpts", "/dev/pts", "devpts", 0, "gid=5,mode=620"); err != nil {
		return "devpts: mount: " + err.Error()
	}
	return "devpts: mounted on /dev/pts"
}
