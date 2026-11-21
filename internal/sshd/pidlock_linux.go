package sshd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrRunning is returned by LockPid when another server holds the pid file.
var ErrRunning = errors.New("the ssh server is already running")

// PidLock is the pid file of the running server, with an flock on it (SSH.md
// section 5 step 4). The lock goes away with the process, so a crash leaves no
// stale lock.
type PidLock struct {
	path string
	f    *os.File
}

// lockUnsupported reports a filesystem without flock (for example a 9p mount).
// The FAT user partition of a device has flock.
func lockUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOLCK) || errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.EINVAL)
}

// LockPid takes the pid file and writes the pid of this process into it. It
// returns ErrRunning when another server holds it.
func LockPid(path string) (*PidLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !lockUnsupported(err) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, ErrRunning
			}
			return nil, err
		}
		// No flock here: fall back to the pid in the file.
		if pid := readPid(path); pid > 0 && pid != os.Getpid() && pidAlive(pid) {
			f.Close()
			return nil, ErrRunning
		}
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0); err != nil {
		f.Close()
		return nil, err
	}
	f.Sync()
	return &PidLock{path: path, f: f}, nil
}

// Release deletes the pid file and drops the lock.
func (l *PidLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	os.Remove(l.path)
	l.f.Close()
	l.f = nil
}

// ProbePid reports the pid in the pid file and whether a server holds it.
func ProbePid(path string) (pid int, running bool) {
	pid = readPid(path)
	f, err := os.Open(path)
	if err != nil {
		return pid, false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	switch {
	case err == nil:
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return pid, false
	case errors.Is(err, syscall.EWOULDBLOCK):
		return pid, pid > 0
	case lockUnsupported(err):
		return pid, pid > 0 && pidAlive(pid)
	}
	return pid, false
}

// PidAlive reports whether a process with this pid exists.
func PidAlive(pid int) bool { return pidAlive(pid) }

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readPid(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
