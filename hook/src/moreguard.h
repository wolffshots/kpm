#pragma once

// kpm-nickelmenu-less-research-2026-11-24.md section 8 step 6: the crash guard
// of the More page row.
//
// The NickelHook failsafe window ends 15 s after boot, before most More taps.
// So the row has its own guard. Before the first insert of a boot, the hook
// writes a marker file with its build id. It deletes the marker when the row
// has been in the More page for 3 s. If Nickel crashes in that time, the marker
// stays, and the next boot of the same build shows no row. A different build id
// deletes the stale marker, so a fixed build tries again.
//
// Plain C file calls only, so a host test can build it without Qt.

#define NKPM_MORE_MARKER "/mnt/onboard/.adds/kpm/.more-row-pending"

// nkpm_moreguard_allow returns 1 when the row may show, and 0 when a marker of
// this build blocks it. A marker of another build is deleted (return 1).
int nkpm_moreguard_allow(char const *marker, char const *build);

// nkpm_moreguard_arm writes the marker with the build id and syncs it to disk.
// It returns 0, or -1 on an error.
int nkpm_moreguard_arm(char const *marker, char const *build);

// nkpm_moreguard_clear deletes the marker.
void nkpm_moreguard_clear(char const *marker);
