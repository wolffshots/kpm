// Host test of src/seed.cc (QT6-FIRMWARE6.md section 3e). Build and run:
//   g++ -std=gnu++11 -Wall -Wextra -DNKPM_SEED_NO_CTOR -I../src -o /tmp/seed_test seed_test.cc ../src/seed.cc && /tmp/seed_test [TINY]
// TINY is optional: the path of an empty, small file system (for example a 1 MB
// tmpfs) for the "user partition full" case. Run as a user that is not root to
// also test a read-only user partition.

#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

#include <string>

#include "seed.h"

static int failures = 0;

#define CHECK(cond)                                                                                                    \
  do {                                                                                                                 \
    if (!(cond)) {                                                                                                     \
      fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);                                                  \
      failures++;                                                                                                      \
    }                                                                                                                  \
  } while (0)

static void sh(std::string const &cmd) {
  if (system(cmd.c_str()) != 0) {
    abort();
  }
}

static void put(std::string const &path, std::string const &data) {
  sh("mkdir -p '" + path.substr(0, path.rfind('/')) + "'");
  FILE *f = fopen(path.c_str(), "w");
  fputs(data.c_str(), f);
  fclose(f);
}

static std::string get(std::string const &path) {
  FILE *f = fopen(path.c_str(), "r");
  if (!f) {
    return "<missing>";
  }
  char buf[256];
  size_t n = fread(buf, 1, sizeof(buf), f);
  fclose(f);
  return std::string(buf, n);
}

static bool exists(std::string const &path) { return access(path.c_str(), F_OK) == 0; }

// newSeed writes a seed folder with a bin/kpm of the content bin.
static void newSeed(std::string const &seed, std::string const &version, std::string const &bin) {
  put(seed + "/bin/kpm", bin);
  put(seed + "/packages.d/kpm.toml", "toml-1");
  put(seed + "/nm/kpm", "nm-1");
  put(seed + "/VERSION", version + " " + std::to_string(bin.size()) + "\n");
}

int main(int argc, char **argv) {
  char tmpl[] = "/tmp/nkpm-seed-XXXXXX";
  std::string root = mkdtemp(tmpl);
  std::string seed = root + "/kpm-seed", onboard = root + "/onboard";
  std::string ver = seed + "/VERSION", guard = seed + "/VERSION.seeding";
  std::string kpm = onboard + "/.adds/kpm/bin/kpm", receipt = onboard + "/.adds/kpm/.seed-version";

  // No seed folder: nothing to do.
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 0);

  newSeed(seed, "0.13.0", "binary-1");

  // User partition not mounted (no .kobo): nothing to do, nothing written.
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 0);
  CHECK(!exists(onboard + "/.adds"));

  // First start: all files and the receipt. VERSION is back, no guard left.
  put(onboard + "/.kobo/version", "x");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 1);
  CHECK(get(kpm) == "binary-1");
  CHECK(get(onboard + "/.adds/kpm/packages.d/kpm.toml") == "toml-1");
  CHECK(get(onboard + "/.adds/nm/kpm") == "nm-1");
  CHECK(get(receipt) == "0.13.0 8\n");
  CHECK(exists(ver) && !exists(guard));
  struct stat st;
  CHECK(stat(kpm.c_str(), &st) == 0 && (st.st_mode & 0777) == 0755);
  CHECK(!exists(kpm + ".seed-tmp"));

  // Same version: the receipt stops a second copy.
  put(onboard + "/.adds/nm/kpm", "user-edit");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 0);
  CHECK(get(onboard + "/.adds/nm/kpm") == "user-edit");

  // New version: all files again.
  newSeed(seed, "0.13.1", "binary-22");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 1);
  CHECK(get(kpm) == "binary-22");
  CHECK(get(onboard + "/.adds/nm/kpm") == "nm-1");
  CHECK(get(receipt) == "0.13.1 9\n");

  // A partial extract: bin/kpm is shorter than VERSION says. The good kpm stays.
  newSeed(seed, "0.13.2", "binary-333");
  put(seed + "/bin/kpm", "bin");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == -1);
  CHECK(get(kpm) == "binary-22");
  CHECK(get(receipt) == "0.13.1 9\n");
  CHECK(exists(ver) && !exists(guard));

  // A VERSION without a size is refused.
  newSeed(seed, "0.13.2", "binary-333");
  put(ver, "0.13.2\n");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == -1);
  CHECK(get(kpm) == "binary-22");
  put(ver, "0.13.2 12x\n");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == -1);

  // A failed copy (source missing) writes no receipt and puts VERSION back, so
  // the next start tries again.
  newSeed(seed, "0.13.2", "binary-333");
  unlink((seed + "/nm/kpm").c_str());
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == -1);
  CHECK(get(receipt) == "0.13.1 9\n");
  CHECK(exists(ver) && !exists(guard));

  // .adds/kpm/bin is a file, not a folder: an error, no crash, VERSION back.
  newSeed(seed, "0.13.2", "binary-333");
  sh("rm -rf '" + onboard + "/.adds/kpm/bin' && echo x > '" + onboard + "/.adds/kpm/bin'");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == -1);
  CHECK(get(receipt) == "0.13.1 9\n");
  CHECK(exists(ver) && !exists(guard));
  sh("rm -f '" + onboard + "/.adds/kpm/bin'");

  // Read-only user partition (only without root, which ignores the modes).
  if (geteuid() != 0) {
    sh("chmod -R a-w '" + onboard + "'");
    CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == -1);
    CHECK(exists(ver) && !exists(guard));
    sh("chmod -R u+w '" + onboard + "'");
  } else {
    printf("seed_test: root, read-only case skipped\n");
  }

  // Crash guard. nm/kpm is a FIFO, so the copy blocks in open. The child is
  // killed there, as a crash or a power loss would stop it.
  newSeed(seed, "0.13.3", "binary-4444");
  unlink((seed + "/nm/kpm").c_str());
  CHECK(mkfifo((seed + "/nm/kpm").c_str(), 0644) == 0);
  pid_t pid = fork();
  if (pid == 0) {
    _exit(nkpm_seed(seed.c_str(), onboard.c_str()) == 1 ? 0 : 1);
  }
  for (int i = 0; i < 500 && !exists(onboard + "/.adds/nm/kpm.seed-tmp"); i++) {
    usleep(10000);
  }
  CHECK(!exists(ver) && exists(guard));
  kill(pid, SIGKILL);
  waitpid(pid, nullptr, 0);
  // The next start does nothing: no copy, no crash loop.
  unlink((seed + "/nm/kpm").c_str());
  put(seed + "/nm/kpm", "nm-1");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 0);
  CHECK(get(receipt) == "0.13.1 9\n");
  // A new install of Kobo.tgz writes VERSION again: the seed runs.
  newSeed(seed, "0.13.3", "binary-4444");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 1);
  CHECK(get(kpm) == "binary-4444");
  CHECK(exists(ver) && !exists(guard));

  // User partition full: an error, no partial kpm, no temp file, VERSION back.
  if (argc > 1) {
    std::string tiny = argv[1];
    put(tiny + "/.kobo/version", "x");
    put(tiny + "/.adds/kpm/bin/kpm", "old");
    newSeed(seed, "0.13.4", std::string(4 << 20, 'k'));
    CHECK(nkpm_seed(seed.c_str(), tiny.c_str()) == -1);
    CHECK(get(tiny + "/.adds/kpm/bin/kpm") == "old");
    CHECK(!exists(tiny + "/.adds/kpm/bin/kpm.seed-tmp"));
    CHECK(!exists(tiny + "/.adds/kpm/.seed-version"));
    CHECK(exists(ver) && !exists(guard));
  } else {
    printf("seed_test: no TINY file system, full-partition case skipped\n");
  }

  // Remove deletes the seed folder, also with a guard file left.
  put(guard, "x");
  nkpm_seed_remove(seed.c_str());
  CHECK(!exists(seed));

  sh("rm -rf '" + root + "'");
  if (failures) {
    fprintf(stderr, "%d failure(s)\n", failures);
    return 1;
  }
  printf("seed_test: OK\n");
  return 0;
}
