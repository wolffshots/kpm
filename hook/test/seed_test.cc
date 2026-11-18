// Host test of src/seed.cc (QT6-FIRMWARE6.md section 3e). Build and run:
//   g++ -std=gnu++11 -Wall -Wextra -DNKPM_SEED_NO_CTOR -I../src -o /tmp/seed_test seed_test.cc ../src/seed.cc && /tmp/seed_test

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
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

static void put(std::string const &path, std::string const &data) {
  std::string dir = path.substr(0, path.rfind('/'));
  std::string cmd = "mkdir -p '" + dir + "'";
  if (system(cmd.c_str()) != 0) {
    abort();
  }
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

int main() {
  char tmpl[] = "/tmp/nkpm-seed-XXXXXX";
  std::string root = mkdtemp(tmpl);
  std::string seed = root + "/kpm-seed", onboard = root + "/onboard";

  // No seed folder: nothing to do.
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 0);

  put(seed + "/VERSION", "0.13.0\n");
  put(seed + "/bin/kpm", "binary-1");
  put(seed + "/packages.d/kpm.toml", "toml-1");
  put(seed + "/nm/kpm", "nm-1");

  // User partition not mounted (no .kobo): nothing to do, nothing written.
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 0);
  CHECK(!exists(onboard + "/.adds"));

  // First start: all files and the receipt.
  put(onboard + "/.kobo/version", "x");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 1);
  CHECK(get(onboard + "/.adds/kpm/bin/kpm") == "binary-1");
  CHECK(get(onboard + "/.adds/kpm/packages.d/kpm.toml") == "toml-1");
  CHECK(get(onboard + "/.adds/nm/kpm") == "nm-1");
  CHECK(get(onboard + "/.adds/kpm/.seed-version") == "0.13.0\n");
  struct stat st;
  CHECK(stat((onboard + "/.adds/kpm/bin/kpm").c_str(), &st) == 0 && (st.st_mode & 0777) == 0755);
  CHECK(!exists(onboard + "/.adds/kpm/bin/kpm.seed-tmp"));

  // Same version: the receipt stops a second copy.
  put(onboard + "/.adds/nm/kpm", "user-edit");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 0);
  CHECK(get(onboard + "/.adds/nm/kpm") == "user-edit");

  // New version: all files again.
  put(seed + "/VERSION", "0.13.1");
  put(seed + "/bin/kpm", "binary-2");
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == 1);
  CHECK(get(onboard + "/.adds/kpm/bin/kpm") == "binary-2");
  CHECK(get(onboard + "/.adds/nm/kpm") == "nm-1");
  CHECK(get(onboard + "/.adds/kpm/.seed-version") == "0.13.1\n");

  // A failed copy writes no receipt.
  put(seed + "/VERSION", "0.13.2");
  unlink((seed + "/nm/kpm").c_str());
  CHECK(nkpm_seed(seed.c_str(), onboard.c_str()) == -1);
  CHECK(get(onboard + "/.adds/kpm/.seed-version") == "0.13.1\n");

  // Remove deletes the seed folder.
  put(seed + "/nm/kpm", "nm-1");
  nkpm_seed_remove(seed.c_str());
  CHECK(!exists(seed));

  std::string cmd = "rm -rf '" + root + "'";
  if (system(cmd.c_str()) != 0) {
    failures++;
  }
  if (failures) {
    fprintf(stderr, "%d failure(s)\n", failures);
    return 1;
  }
  printf("seed_test: OK\n");
  return 0;
}
