package main

import (
	"os"
	"os/exec"
	"syscall"

	"kpm/internal/sshd"
)

// spawnServe runs `kpm ssh serve` in a new session (setsid), with the clean
// environment of sshEnv, stdin from /dev/null and its output to the log
// (SSH.md section 5 step 3). The returned channel closes when the child ends
// while this process still runs.
func spawnServe(f sshd.Files) (<-chan struct{}, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(f.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer devnull.Close()
	cmd := exec.Command(exe, "ssh", "serve")
	cmd.Env = sshEnv()
	cmd.Dir = "/"
	cmd.Stdin = devnull
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	return exited, nil
}

func signalPid(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
