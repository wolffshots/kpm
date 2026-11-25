// Host test of src/launch.cc. Build and run:
//   g++ -std=gnu++11 -Wall -Wextra -I../src -o /tmp/launch_test launch_test.cc ../src/launch.cc && /tmp/launch_test

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <string>

#include "launch.h"

static int failures = 0;

#define CHECK(cond)                                                                                                    \
  do {                                                                                                                 \
    if (!(cond)) {                                                                                                     \
      fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);                                                  \
      failures++;                                                                                                      \
    }                                                                                                                  \
  } while (0)

static void put(std::string const &path, std::string const &data) {
  FILE *f = fopen(path.c_str(), "w");
  fwrite(data.data(), 1, data.size(), f);
  fclose(f);
}

static std::string get(std::string const &path) {
  FILE *f = fopen(path.c_str(), "r");
  if (f == nullptr) {
    return "<missing>";
  }
  std::string s;
  char buf[4096];
  size_t n;
  while ((n = fread(buf, 1, sizeof(buf), f)) > 0) {
    s.append(buf, n);
  }
  fclose(f);
  return s;
}

static char const kEntries[] = "# kpm - Kobo package manager\n"
                               "menu_item :main :kpm - Update all :nickel_wifi :autoconnect\n"
                               "  chain_success :cmd_spawn :quiet:/mnt/onboard/.adds/kpm/bin/kpm update --all\n"
                               "\n"
                               "menu_item :main :kpm - Package manager :cmd_spawn :/mnt/onboard/.adds/kpm/bin/kpm ui";

static char const kOff[] = "# kpm - Kobo package manager\n"
                           "#kpm-off menu_item :main :kpm - Update all :nickel_wifi :autoconnect\n"
                           "#kpm-off   chain_success :cmd_spawn :quiet:/mnt/onboard/.adds/kpm/bin/kpm update --all\n"
                           "\n"
                           "#kpm-off menu_item :main :kpm - Package manager :cmd_spawn :/mnt/onboard/.adds/kpm/bin/kpm ui";

int main() {
  char dir[] = "/tmp/launch_test.XXXXXX";
  if (mkdtemp(dir) == nullptr) {
    return 2;
  }
  std::string const d(dir);
  std::string const cfg = d + "/ui.toml", nm = d + "/kpm", hide = d + "/kpm-hide";

  // Read: defaults, then each value.
  struct nkpm_launch s = {9, 9};
  nkpm_launch_read(cfg.c_str(), &s);
  CHECK(s.more_row == 1 && s.hide_nm_button == 0);
  put(cfg, "open_from = \"nickelmenu\" # a comment\n  hide_nickelmenu_button=true\n");
  nkpm_launch_read(cfg.c_str(), &s);
  CHECK(s.more_row == 0 && s.hide_nm_button == 1);
  put(cfg, "# open_from = \"nickelmenu\"\nopen_from_x = \"nickelmenu\"\nopen_from = \"other\"\nhide_nickelmenu_button = yes\n");
  nkpm_launch_read(cfg.c_str(), &s);
  CHECK(s.more_row == 1 && s.hide_nm_button == 0);

  // Write, then read back.
  struct nkpm_launch w = {0, 1};
  CHECK(nkpm_launch_write(cfg.c_str(), &w) == 0);
  nkpm_launch_read(cfg.c_str(), &s);
  CHECK(s.more_row == 0 && s.hide_nm_button == 1);
  CHECK(access((cfg + ".tmp").c_str(), F_OK) != 0);
  w = {1, 0};
  CHECK(nkpm_launch_write(cfg.c_str(), &w) == 0);
  nkpm_launch_read(cfg.c_str(), &s);
  CHECK(s.more_row == 1 && s.hide_nm_button == 0);

  // Decide: each rule of launch.h.
  struct nkpm_launch const moreHide = {1, 1}, moreShow = {1, 0}, nmHide = {0, 1};
  struct nkpm_launch_state st = nkpm_launch_decide(&moreHide, 1, 1);
  CHECK(st.use_row == 1 && st.entries == 0 && st.hide == 1);
  st = nkpm_launch_decide(&moreShow, 1, 1);
  CHECK(st.use_row == 1 && st.entries == 0 && st.hide == 0);
  st = nkpm_launch_decide(&nmHide, 1, 1); // NickelMenu: no row, entries, never hidden
  CHECK(st.use_row == 0 && st.entries == 1 && st.hide == 0);
  st = nkpm_launch_decide(&nmHide, 0, 1); // no NickelMenu: the row shows anyway
  CHECK(st.use_row == 1 && st.entries == 0 && st.hide == 0);
  st = nkpm_launch_decide(&moreHide, 1, 0); // row stopped: entries back, button shown
  CHECK(st.use_row == 0 && st.entries == 1 && st.hide == 0);

  // Apply: comment out, again (no change), restore, and the hide file.
  put(nm, kEntries);
  CHECK(nkpm_launch_apply(nm.c_str(), hide.c_str(), 0, 1) == 0);
  CHECK(get(nm) == kOff);
  CHECK(get(hide).find("\nexperimental:menu_main_15505_enabled:0\n") != std::string::npos);
  CHECK(nkpm_launch_apply(nm.c_str(), hide.c_str(), 0, 1) == 0);
  CHECK(get(nm) == kOff);
  CHECK(nkpm_launch_apply(nm.c_str(), hide.c_str(), 1, 0) == 0);
  CHECK(get(nm) == kEntries);
  CHECK(get(hide) == "<missing>");
  CHECK(nkpm_launch_apply(nm.c_str(), hide.c_str(), 1, 0) == 0); // no hide file to delete: no error

  // A new package version writes the file with active entries: apply turns
  // them off again and keeps the lines that are already off.
  put(nm, std::string(kOff) + "\nmenu_item :main :kpm - Status :cmd_output :9000 :/x\n");
  CHECK(nkpm_launch_apply(nm.c_str(), hide.c_str(), 0, 0) == 0);
  CHECK(get(nm) == std::string(kOff) + "\n#kpm-off menu_item :main :kpm - Status :cmd_output :9000 :/x\n");

  // No nm file (kpm installed without it): apply does not create one.
  unlink(nm.c_str());
  CHECK(nkpm_launch_apply(nm.c_str(), hide.c_str(), 0, 0) == 0);
  CHECK(get(nm) == "<missing>");

  // NickelMenu detection, also while the NickelHook failsafe has the build
  // renamed (one folder up in the NickelHook of kpm, in place in others).
  std::string const img = d + "/imageformats";
  if (system(("mkdir -p '" + img + "'").c_str()) != 0) {
    return 2;
  }
  CHECK(nkpm_nm_installed(img.c_str()) == 0);
  put(d + "/libnm.so.failsafe", "x");
  CHECK(nkpm_nm_installed(img.c_str()) == 1);
  unlink((d + "/libnm.so.failsafe").c_str());
  put(img + "/libnm-qt6.so.failsafe", "x");
  CHECK(nkpm_nm_installed(img.c_str()) == 1);
  unlink((img + "/libnm-qt6.so.failsafe").c_str());
  CHECK(nkpm_nm_installed(img.c_str()) == 0);
  put(img + "/libnm-qt6.so", "x");
  CHECK(nkpm_nm_installed(img.c_str()) == 1);

  if (system(("rm -rf '" + d + "'").c_str()) != 0) {
    failures++;
  }
  if (failures != 0) {
    fprintf(stderr, "%d failure(s)\n", failures);
    return 1;
  }
  printf("launch_test: ok\n");
  return 0;
}
