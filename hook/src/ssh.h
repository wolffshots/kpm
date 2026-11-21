#pragma once

// kpm-archive/docs/SSH.md section 5 and 8: the hook side of `kpm ssh`.
//
// Plain C file calls only: nkpm_ssh_stop runs in the NickelHook uninstall
// path, before Qt is usable, and both functions have a host test
// (hook/test/ssh_test.cc).

#define NKPM_SSH_CONFIG "/mnt/onboard/.adds/kpm/ssh/config.toml"
#define NKPM_SSH_PID "/mnt/onboard/.adds/kpm/ssh/sshd.pid"

// nkpm_ssh_enabled reads config.toml and returns 1 when it has the line
// "enabled = true", else 0. A missing or unreadable file is 0. It reads at
// most 4 KiB: the hook must not block Nickel init.
int nkpm_ssh_enabled(char const *config_path);

// nkpm_ssh_stop sends SIGTERM to the server of the pid file. It signals the
// pid only when /proc/<pid>/cmdline ends with "<...>/kpm ssh serve", so a
// stale pid file never kills another process. It returns 1 when it sent the
// signal.
int nkpm_ssh_stop(char const *pid_path, char const *proc_root);
