// kpm-archive/docs/SSH.md section 5 and 8. See ssh.h.

#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "ssh.h"

// read_small reads at most cap-1 bytes of path into buf, NUL-terminated. It
// returns the count, or -1 on an error.
static int read_small(char const *path, char *buf, int cap) {
  int fd = open(path, O_RDONLY | O_CLOEXEC);
  if (fd < 0) {
    return -1;
  }
  int n = 0;
  while (n < cap - 1) {
    ssize_t r = read(fd, buf + n, cap - 1 - n);
    if (r <= 0) {
      break;
    }
    n += (int)r;
  }
  close(fd);
  buf[n] = '\0';
  return n;
}

// trim removes leading and trailing blanks in place and returns the start.
static char *trim(char *s) {
  while (*s == ' ' || *s == '\t') {
    s++;
  }
  char *e = s + strlen(s);
  while (e > s && (e[-1] == ' ' || e[-1] == '\t' || e[-1] == '\r')) {
    *--e = '\0';
  }
  return s;
}

int nkpm_ssh_enabled(char const *config_path) {
  char buf[4096];
  if (read_small(config_path, buf, sizeof(buf)) <= 0) {
    return 0;
  }
  int enabled = 0;
  for (char *line = buf; line != nullptr;) {
    char *next = strchr(line, '\n');
    if (next != nullptr) {
      *next++ = '\0';
    }
    char *hash = strchr(line, '#');
    if (hash != nullptr) {
      *hash = '\0'; // a comment, or the rest of a line after the value
    }
    char *eq = strchr(line, '=');
    if (eq != nullptr) {
      *eq = '\0';
      if (strcmp(trim(line), "enabled") == 0) {
        enabled = strcmp(trim(eq + 1), "true") == 0; // the last key wins, as in TOML tools
      }
    }
    line = next;
  }
  return enabled;
}

int nkpm_ssh_stop(char const *pid_path, char const *proc_root) {
  char buf[64];
  if (read_small(pid_path, buf, sizeof(buf)) <= 0) {
    return 0;
  }
  char *end = nullptr;
  long pid = strtol(buf, &end, 10);
  if (pid <= 1 || end == buf) {
    return 0;
  }
  char path[256];
  snprintf(path, sizeof(path), "%s/%ld/cmdline", proc_root, pid);
  char cmd[512];
  int n = read_small(path, cmd, sizeof(cmd));
  if (n <= 0) {
    return 0;
  }
  // cmdline is NUL-separated and ends with "<...>/kpm", "ssh", "serve". A
  // launcher before kpm is allowed: qemu-user in the kfw emulator shows
  // "qemu-arm-static" first.
  static char const tail[] = "kpm\0ssh\0serve"; // 14 bytes with the final NUL
  int start = n - (int)sizeof(tail);
  if (start < 0 || memcmp(cmd + start, tail, sizeof(tail)) != 0) {
    return 0;
  }
  if (start > 0 && cmd[start - 1] != '/' && cmd[start - 1] != '\0') {
    return 0; // the argument is "xkpm", not "kpm"
  }
  return kill((pid_t)pid, SIGTERM) == 0;
}
