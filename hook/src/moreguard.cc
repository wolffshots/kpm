// kpm-nickelmenu-less-research-2026-11-24.md section 8 step 6. See moreguard.h.

#include <errno.h>
#include <fcntl.h>
#include <string.h>
#include <unistd.h>

#include "moreguard.h"

int nkpm_moreguard_allow(char const *marker, char const *build) {
  int fd = open(marker, O_RDONLY);
  if (fd < 0) {
    return 1;
  }
  char buf[128];
  ssize_t n = read(fd, buf, sizeof(buf) - 1);
  close(fd);
  buf[n > 0 ? n : 0] = '\0';
  if (strcmp(buf, build) == 0) {
    return 0;
  }
  unlink(marker);
  return 1;
}

int nkpm_moreguard_arm(char const *marker, char const *build) {
  int fd = open(marker, O_WRONLY | O_CREAT | O_TRUNC, 0644);
  if (fd < 0) {
    return -1;
  }
  size_t len = strlen(build);
  bool ok = write(fd, build, len) == (ssize_t)len;
  // The marker must be on disk before the row code runs: a crash in the next
  // 3 s must leave it.
  ok = fsync(fd) == 0 && ok;
  close(fd);
  return ok ? 0 : -1;
}

void nkpm_moreguard_clear(char const *marker) {
  unlink(marker);
}
