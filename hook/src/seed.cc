// QT6-FIRMWARE6.md section 3e: copy the kpm files from the seed folder of the
// dual Kobo.tgz to the user partition. See seed.h.

#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <syslog.h>
#include <unistd.h>

#include "seed.h"

namespace {

struct SeedFile {
  char const *src; // relative to the seed folder
  char const *dst; // relative to the user partition
  mode_t mode;
};

// The files that a KoboRoot.tgz writes to the user partition (build/package.go).
SeedFile const seedFiles[] = {
    {"bin/kpm", ".adds/kpm/bin/kpm", 0755},
    {"packages.d/kpm.toml", ".adds/kpm/packages.d/kpm.toml", 0644},
    {"nm/kpm", ".adds/nm/kpm", 0644},
};

char const *const receiptPath = ".adds/kpm/.seed-version";

// readSmall reads a short text file into buf and trims trailing whitespace.
bool readSmall(char const *path, char *buf, size_t cap) {
  int fd = open(path, O_RDONLY | O_CLOEXEC);
  if (fd < 0) {
    return false;
  }
  ssize_t n = read(fd, buf, cap - 1);
  close(fd);
  if (n < 0) {
    return false;
  }
  while (n > 0 && (buf[n - 1] == '\n' || buf[n - 1] == '\r' || buf[n - 1] == ' ')) {
    n--;
  }
  buf[n] = '\0';
  return n > 0;
}

// mkdirParents creates each parent folder of path.
bool mkdirParents(char const *path) {
  char tmp[512];
  if (snprintf(tmp, sizeof(tmp), "%s", path) >= (int)sizeof(tmp)) {
    return false;
  }
  for (char *p = tmp + 1; *p; p++) {
    if (*p != '/') {
      continue;
    }
    *p = '\0';
    if (mkdir(tmp, 0755) != 0 && errno != EEXIST) {
      return false;
    }
    *p = '/';
  }
  return true;
}

// writeAll writes len bytes to fd.
bool writeAll(int fd, char const *data, size_t len) {
  while (len > 0) {
    ssize_t n = write(fd, data, len);
    if (n < 0) {
      if (errno == EINTR) {
        continue;
      }
      return false;
    }
    data += n;
    len -= (size_t)n;
  }
  return true;
}

// replaceFile writes dst through dst.seed-tmp and a rename. A running kpm keeps
// its old inode. Either srcPath or data gives the content.
bool replaceFile(char const *srcPath, char const *data, char const *dst, mode_t mode) {
  char tmp[512];
  if (snprintf(tmp, sizeof(tmp), "%s.seed-tmp", dst) >= (int)sizeof(tmp)) {
    return false;
  }
  int out = open(tmp, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0644);
  if (out < 0) {
    return false;
  }
  bool ok = true;
  if (srcPath) {
    int in = open(srcPath, O_RDONLY | O_CLOEXEC);
    ok = in >= 0;
    char buf[65536];
    while (ok) {
      ssize_t n = read(in, buf, sizeof(buf));
      if (n < 0 && errno == EINTR) {
        continue;
      }
      if (n <= 0) {
        ok = n == 0;
        break;
      }
      ok = writeAll(out, buf, (size_t)n);
    }
    if (in >= 0) {
      close(in);
    }
  } else {
    ok = writeAll(out, data, strlen(data));
  }
  ok = ok && fsync(out) == 0;
  ok = close(out) == 0 && ok;
  // The user partition is FAT. chmod can fail there and the mount options set
  // the mode, so a chmod error is not a copy error.
  if (ok) {
    chmod(tmp, mode);
  }
  ok = ok && rename(tmp, dst) == 0;
  if (!ok) {
    unlink(tmp);
  }
  return ok;
}

// fileSize returns the size of path, or -1. It uses lseek, not stat: the 32-bit
// stat fails with EOVERFLOW on a file system with 64-bit inode numbers.
off_t fileSize(char const *path) {
  int fd = open(path, O_RDONLY | O_CLOEXEC);
  if (fd < 0) {
    return -1;
  }
  off_t n = lseek(fd, 0, SEEK_END);
  close(fd);
  return n;
}

// seedCopy copies the seed files and writes the receipt.
int seedCopy(char const *seed_dir, char const *onboard, char const *version, char const *line) {
  char path[512], dst[512];
  for (SeedFile const &f : seedFiles) {
    snprintf(path, sizeof(path), "%s/%s", seed_dir, f.src);
    snprintf(dst, sizeof(dst), "%s/%s", onboard, f.dst);
    if (!mkdirParents(dst) || !replaceFile(path, nullptr, dst, f.mode)) {
      syslog(LOG_ERR, "(NickelKPM) seed: copy %s to %s failed: %s", path, dst, strerror(errno));
      return -1;
    }
  }

  // The receipt goes last, so a failed copy is tried again at the next start.
  snprintf(path, sizeof(path), "%s/%s", onboard, receiptPath);
  if (!replaceFile(nullptr, line, path, 0644)) {
    syslog(LOG_ERR, "(NickelKPM) seed: write %s failed: %s", path, strerror(errno));
    return -1;
  }
  syslog(LOG_INFO, "(NickelKPM) seed: copied kpm %s to %s", version, onboard);
  return 1;
}

} // namespace

int nkpm_seed(char const *seed_dir, char const *onboard) {
  char path[512], guard[512], line[64], version[64], receipt[64];

  snprintf(path, sizeof(path), "%s/VERSION", seed_dir);
  snprintf(guard, sizeof(guard), "%s/VERSION.seeding", seed_dir);
  if (!readSmall(path, line, sizeof(line))) {
    if (access(guard, F_OK) == 0) {
      syslog(LOG_ERR, "(NickelKPM) seed: an earlier seed did not finish, so the seed is off. "
                      "Install the kpm Kobo.tgz again to try again.");
    }
    return 0; // no seed folder: a KoboRoot.tgz install
  }

  // VERSION is "<version> <size of bin/kpm>" (build/package.go). A bin/kpm of
  // another size is a partial extract: do not copy it over a good kpm.
  char *sp = strchr(line, ' ');
  char *end = nullptr;
  long want = sp ? strtol(sp + 1, &end, 10) : -1;
  if (!sp || end == sp + 1 || *end != '\0' || want <= 0) {
    syslog(LOG_ERR, "(NickelKPM) seed: bad %s: '%s'", path, line);
    return -1;
  }
  snprintf(version, sizeof(version), "%.*s", (int)(sp - line), line);

  // A missing .kobo folder means that the user partition is not mounted. Do not
  // write into the mount point on the root file system. Use open, not stat: the
  // 32-bit stat fails with EOVERFLOW on a file system with 64-bit inode numbers.
  snprintf(path, sizeof(path), "%s/.kobo", onboard);
  int dirfd = open(path, O_RDONLY | O_DIRECTORY | O_CLOEXEC);
  if (dirfd < 0) {
    return 0;
  }
  close(dirfd);

  snprintf(path, sizeof(path), "%s/%s", onboard, receiptPath);
  if (readSmall(path, receipt, sizeof(receipt)) && strcmp(receipt, line) == 0) {
    return 0;
  }

  snprintf(path, sizeof(path), "%s/bin/kpm", seed_dir);
  off_t got = fileSize(path);
  if (got != (off_t)want) {
    syslog(LOG_ERR, "(NickelKPM) seed: %s has %ld bytes, want %ld. Install the kpm Kobo.tgz again.", path, (long)got,
           want);
    return -1;
  }

  // Crash guard. The seed runs before the NickelHook failsafe, so a crash here
  // would repeat at each boot. VERSION.seeding exists only while the copy runs.
  // After a crash, a hang or a power loss, VERSION is gone and the seed does
  // nothing until the user installs Kobo.tgz again. The UI hook still loads.
  snprintf(path, sizeof(path), "%s/VERSION", seed_dir);
  if (rename(path, guard) != 0) {
    syslog(LOG_ERR, "(NickelKPM) seed: rename %s failed: %s", path, strerror(errno));
    return -1;
  }
  int seedfd = open(seed_dir, O_RDONLY | O_DIRECTORY | O_CLOEXEC);
  if (seedfd >= 0) {
    fsync(seedfd);
    close(seedfd);
  }

  char out[sizeof(line) + 1];
  snprintf(out, sizeof(out), "%s\n", line);
  int ret = seedCopy(seed_dir, onboard, version, out);

  // Success or a handled error: put VERSION back. After an error the next
  // start tries again, as before the guard.
  if (rename(guard, path) != 0) {
    syslog(LOG_ERR, "(NickelKPM) seed: rename %s failed: %s", guard, strerror(errno));
  }
  return ret;
}

void nkpm_seed_remove(char const *seed_dir) {
  char path[512];
  for (SeedFile const &f : seedFiles) {
    snprintf(path, sizeof(path), "%s/%s", seed_dir, f.src);
    unlink(path);
  }
  char const *const rest[] = {"VERSION", "VERSION.seeding", "bin", "packages.d", "nm", ""};
  for (char const *r : rest) {
    snprintf(path, sizeof(path), "%s/%s", seed_dir, r);
    if (unlink(path) != 0) {
      rmdir(path);
    }
  }
}

#ifndef NKPM_SEED_NO_CTOR
// Priority 200 runs before nh_init, the NickelHook constructor without a
// priority (dual-qt-research.md, Uninstall and updates).
__attribute__((constructor(200))) static void nkpm_seed_ctor() { nkpm_seed(NKPM_SEED_DIR, NKPM_ONBOARD); }
#endif
