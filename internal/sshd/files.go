// Package sshd is the SSH server of `kpm ssh` (kpm-archive/docs/SSH.md). It
// serves root logins with public keys from authorized_keys, a shell with a PTY,
// exec commands without a PTY, and the sftp subsystem. All state is in one
// folder on the user partition, .adds/kpm/ssh (SSH.md section 3).
package sshd

import "path/filepath"

// Files names the files of the state folder (SSH.md section 3).
type Files struct {
	Dir string
}

// AuthorizedKeys is the OpenSSH authorized_keys file. The user edits it over USB.
func (f Files) AuthorizedKeys() string { return filepath.Join(f.Dir, "authorized_keys") }

// HostKey is the ed25519 host key. kpm makes it once and never replaces it.
func (f Files) HostKey() string { return filepath.Join(f.Dir, "ssh_host_ed25519_key") }

// Config is config.toml (enabled, port, listen).
func (f Files) Config() string { return filepath.Join(f.Dir, "config.toml") }

// PidFile holds the pid of the running server. The server holds a lock on it.
func (f Files) PidFile() string { return filepath.Join(f.Dir, "sshd.pid") }

// LogFile is the server log. It is capped at 256 KiB with one old file.
func (f Files) LogFile() string { return filepath.Join(f.Dir, "sshd.log") }
