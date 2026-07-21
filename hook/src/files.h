#pragma once

// NICKEL-UI.md §2 — path and qrc resource constants for NickelKPM.

namespace Files {
#ifdef NKPM_SIM
// Desktop simulator (hook/sim, Tier-3 off-device testing): the kpm binary path
// cannot be shadowed via -I because src/*.cc include "files.h" as a quote-include
// that always resolves to this file first. So the sim build defines NKPM_SIM and
// the binary is taken from $NKPM_KPM (falling back to "kpm" on PATH). Compile-time
// inert on device: NKPM_SIM is never defined by hook/Makefile.
#include <cstdlib>
inline char const *kpm_sim_path() {
  char const *e = ::getenv("NKPM_KPM");
  return (e && *e) ? e : "kpm";
}
static char const *const kpm = kpm_sim_path();
#else
// The installed kpm binary the hook shells out to (NICKEL-UI.md §2/§4).
constexpr char const *kpm = "/mnt/onboard/.adds/kpm/bin/kpm";
#endif

// Resource icon (nkpm.qrc). The More-menu entry row is icon-less; this is only
// a bundled asset (NICKEL-UI.md §3).
constexpr char const *icon = ":/nkpm/icon.png";

// Wi-Fi status glyph reused from Nickel's own resources for the connect spinner
// (mirrors NickelHardcover's cli.cc).
constexpr char const *wifi = ":/images/statusbar/wifi05.png";

// Nickel's built-in paging arrows, reused for the PagedStack footer (mirrors
// NickelHardcover's pagedstack.cc).
constexpr char const *arrow_backward = ":/images/reading/global_backward.png";
constexpr char const *arrow_forward = ":/images/reading/global_forward.png";

// The NickelHook uninstall flag (documented in README): creating this file
// removes the UI hook only; kpm's binary/config/state are untouched. Declared
// in nh_info in nkpm.cc — repeated here for reference only.
constexpr char const *uninstall_flag = "/mnt/onboard/kpm_ui_uninstall";

// UI launch trigger (NICKELMENU-LAUNCH.md): the hook watches this tmpfs file and
// opens the browser when it changes; `kpm ui` (fired by the NickelMenu
// "kpm - Package manager" item) writes to it. Kept in sync with cmd_ui.go.
constexpr char const *ui_trigger = "/tmp/nkpm-open";
} // namespace Files
