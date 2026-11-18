#pragma once

// QT6-FIRMWARE6.md section 3e: the seed folder of the dual Kobo.tgz.
//
// A Kobo.tgz can only write under /usr/local/Kobo. It carries the kpm files for
// the user partition in /usr/local/Kobo/kpm-seed/. The hook copies them to
// /mnt/onboard when the seed VERSION differs from the receipt
// /mnt/onboard/.adds/kpm/.seed-version. Plain C file calls only: the copy runs
// in a constructor before NickelHook and before Qt is usable.

#define NKPM_SEED_DIR "/usr/local/Kobo/kpm-seed"
#define NKPM_ONBOARD "/mnt/onboard"

// nkpm_seed copies the seed files into onboard. It returns 1 when it copied the
// files, 0 when it had nothing to do, and -1 on an error. On an error it writes
// no receipt, so the next start tries again.
int nkpm_seed(char const *seed_dir, char const *onboard);

// nkpm_seed_remove deletes the seed folder. The UI uninstall calls it.
void nkpm_seed_remove(char const *seed_dir);
