//go:build !linux

package sshd

import (
	"errors"
	"os"
	"os/exec"
)

// The session processes run on the device (Linux) only. These stubs let kpm
// build on other hosts.

func startPTY(cmd *exec.Cmd, rows, cols uint16) (*os.File, error) { return nil, errors.ErrUnsupported }

func setPTYSize(f *os.File, rows, cols uint16) {}

func newProcessGroup(cmd *exec.Cmd) {}

func hangup(pid int) {}

// EnsureDevpts does nothing off Linux.
func EnsureDevpts() string { return "" }
