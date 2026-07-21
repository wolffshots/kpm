#pragma once

// NICKEL-UI.md §2 — path and qrc resource constants for NickelKPM.

namespace Files {
// The installed kpm binary the hook shells out to (NICKEL-UI.md §2/§4).
constexpr char const *kpm = "/mnt/onboard/.adds/kpm/bin/kpm";

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
} // namespace Files
