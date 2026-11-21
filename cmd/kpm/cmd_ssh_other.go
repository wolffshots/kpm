//go:build !linux

package main

import (
	"errors"
	"syscall"

	"kpm/internal/sshd"
)

// The ssh server runs on the device (Linux) only.

func spawnServe(f sshd.Files) (<-chan struct{}, error) {
	return nil, errors.New("the ssh server runs on the device only")
}

func signalPid(pid int, sig syscall.Signal) error {
	return errors.New("the ssh server runs on the device only")
}
