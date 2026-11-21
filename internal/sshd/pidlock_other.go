//go:build !linux

package sshd

import "errors"

// The ssh server runs on the device (Linux) only. These stubs let the rest of
// kpm build and test on other hosts.

// ErrRunning is returned by LockPid when another server holds the pid file.
var ErrRunning = errors.New("the ssh server is already running")

// PidLock is not available off Linux.
type PidLock struct{}

// LockPid always fails off Linux.
func LockPid(path string) (*PidLock, error) { return nil, errors.ErrUnsupported }

// Release does nothing off Linux.
func (l *PidLock) Release() {}

// ProbePid always reports no server off Linux.
func ProbePid(path string) (pid int, running bool) { return 0, false }

// PidAlive always reports false off Linux.
func PidAlive(pid int) bool { return false }
