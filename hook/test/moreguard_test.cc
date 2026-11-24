// Host test of src/moreguard.cc (kpm-nickelmenu-less-research-2026-11-24.md
// section 8 step 6). Build and run:
//   g++ -std=gnu++11 -Wall -Wextra -I../src -o /tmp/moreguard_test moreguard_test.cc ../src/moreguard.cc && /tmp/moreguard_test

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <string>

#include "moreguard.h"

static int failures = 0;

#define CHECK(cond)                                                                                                    \
  do {                                                                                                                 \
    if (!(cond)) {                                                                                                     \
      fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);                                                  \
      failures++;                                                                                                      \
    }                                                                                                                  \
  } while (0)

static bool exists(std::string const &path) {
  return access(path.c_str(), F_OK) == 0;
}

int main() {
  char dir[] = "/tmp/moreguard_test.XXXXXX";
  if (!mkdtemp(dir)) {
    perror("mkdtemp");
    return 2;
  }
  std::string const marker = std::string(dir) + "/.more-row-pending";
  char const *m = marker.c_str();

  // No marker: the row shows.
  CHECK(nkpm_moreguard_allow(m, "build-a") == 1);

  // Armed, then a crash (no clear): the same build shows no row, and the
  // marker stays for the next boot.
  CHECK(nkpm_moreguard_arm(m, "build-a") == 0);
  CHECK(exists(marker));
  CHECK(nkpm_moreguard_allow(m, "build-a") == 0);
  CHECK(nkpm_moreguard_allow(m, "build-a") == 0);
  CHECK(exists(marker));

  // A new build deletes the stale marker and shows the row.
  CHECK(nkpm_moreguard_allow(m, "build-b") == 1);
  CHECK(!exists(marker));

  // Armed and cleared after 3 s: the next boot shows the row.
  CHECK(nkpm_moreguard_arm(m, "build-b") == 0);
  nkpm_moreguard_clear(m);
  CHECK(!exists(marker));
  CHECK(nkpm_moreguard_allow(m, "build-b") == 1);

  // A prefix of the build id is another build.
  CHECK(nkpm_moreguard_arm(m, "build") == 0);
  CHECK(nkpm_moreguard_allow(m, "build-b") == 1);
  CHECK(!exists(marker));

  // An empty marker (a crash during the write) is another build.
  FILE *f = fopen(m, "w");
  fclose(f);
  CHECK(nkpm_moreguard_allow(m, "build-b") == 1);

  // No folder: arm fails, allow shows the row.
  std::string const gone = std::string(dir) + "/missing/.more-row-pending";
  CHECK(nkpm_moreguard_arm(gone.c_str(), "build-b") == -1);
  CHECK(nkpm_moreguard_allow(gone.c_str(), "build-b") == 1);

  rmdir(dir);
  if (failures) {
    fprintf(stderr, "%d failure(s)\n", failures);
    return 1;
  }
  printf("PASS moreguard_test\n");
  return 0;
}
