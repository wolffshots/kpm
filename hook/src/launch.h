#pragma once

// Where kpm opens from: the "Package manager" row on the More page
// (morerow.cc) or the kpm entries in NickelMenu (.adds/nm/kpm). The Settings tab
// (settingspage.cc) changes it. See the README section "Graphical UI".
//
// Plain C file calls only: nkpm_uninstall runs the apply step before Qt is
// usable, and hook/test/launch_test.cc builds this file without Qt.

#define NKPM_UI_CONFIG "/mnt/onboard/.adds/kpm/ui.toml"
#define NKPM_NM_FILE "/mnt/onboard/.adds/nm/kpm"
#define NKPM_NM_HIDE "/mnt/onboard/.adds/nm/kpm-hide"
#define NKPM_IMAGEFORMATS "/usr/local/Kobo/imageformats"

// nkpm_launch holds the two settings of ui.toml.
struct nkpm_launch {
  int more_row;       // 1: open kpm from the More page row. 0: from NickelMenu.
  int hide_nm_button; // 1: hide the NickelMenu button in the bottom bar.
};

// nkpm_launch_state is what applies on this device now.
struct nkpm_launch_state {
  int use_row; // the row shows on the More page
  int entries; // the kpm entries in .adds/nm/kpm are active
  int hide;    // .adds/nm/kpm-hide exists
};

// nkpm_launch_read reads ui.toml. A missing file, a missing key or a value it
// does not know gives the default: the More row, the NickelMenu button shown.
void nkpm_launch_read(char const *path, struct nkpm_launch *out);

// nkpm_launch_write writes ui.toml through a temporary file and a rename. It
// returns 0, or -1 on an error.
int nkpm_launch_write(char const *path, struct nkpm_launch const *s);

// nkpm_launch_decide applies the rules that keep one way to open kpm:
//   - The row shows when it is available and the setting is the More row.
//     It also shows when NickelMenu is not installed, whatever the setting.
//   - The kpm NickelMenu entries are active when the row does not show.
//   - The NickelMenu button hides only when the row shows, the setting is the
//     More row, and the hide setting is on.
// row_available is 0 when the crash guard or a missing Help row stops the row.
struct nkpm_launch_state nkpm_launch_decide(struct nkpm_launch const *s, int nm_installed, int row_available);

// nkpm_launch_apply makes the two NickelMenu files match a state. With entries
// 0, it comments out each line of nm_file that is not a comment, with the
// prefix "#kpm-off ". With entries 1, it removes that prefix. The file stays,
// so the kpm package keeps all its files. hide 1 writes hide_file with the
// line "experimental:menu_main_15505_enabled:0", hide 0 deletes it. A file
// that already matches is not written. It returns 0, or -1 on an error.
int nkpm_launch_apply(char const *nm_file, char const *hide_file, int entries, int hide);

// nkpm_nm_installed returns 1 when dir holds a NickelMenu build (libnm.so or
// libnm-qt6.so), else 0. While NickelMenu starts, the NickelHook failsafe
// renames its build to "<name>.failsafe", in dir or in the folder above dir.
// Those names count too, because kpm init can run in that time.
int nkpm_nm_installed(char const *dir);
