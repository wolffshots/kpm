#!/bin/sh
# progress-kpm.sh: a kpm for `nkpm-sim --exercise-progress`. It answers
# `registry refresh`, `check` and `update --all` with slow progress lines
# (protocol v1, kpm-progress.md) and a short payload. Each other command runs
# the real kpm in $REAL_KPM.
#
# The hook must set KPM_PROGRESS=1. FAKE_OLD=1 acts as a kpm from before the
# protocol: no progress lines, the same payloads.
#
# The steps model the Refresh of the 0.16.0-rc2 device test (finding 5): two
# registries, a check of 7 packages, and a carry-over download of kpm. check
# plans its 3 steps (check, download, bundle) before its first line, and
# registry refresh reports them in "next" when the hook sets
# KPM_PROGRESS_NEXT=check. The view must show "of 5" from the first line.

NEXT=""
if [ "$KPM_PROGRESS_NEXT" = check ]; then
  NEXT=',"next":3'
fi

p() {
  if [ "$KPM_PROGRESS" = 1 ] && [ "$FAKE_OLD" != 1 ]; then
    printf 'PROGRESS {"v":1,"step":%s,"steps":%s,"text":"%s","done":%s,"total":%s,"unit":"%s"%s}\n' "$1" "$2" "$3" "$4" "$5" "$6" "$7"
  fi
}

case "$1 $2" in
"registry refresh")
  p 1 2 "Updating package list kobo-registry" 0 0 "" "$NEXT"
  sleep 2.5
  p 2 2 "Updating package list fictionaries" 0 0 "" "$NEXT"
  sleep 2.5
  echo 'BEGIN_JSON{"refreshed":[],"failed":[]}'
  ;;
"check --json")
  p 1 3 "Checking for updates" 0 0 ""
  for i in 1 2 3 4 5 6 7; do
    sleep 0.5
    p 1 3 "Checking for updates" "$i" 7 items
  done
  # The carry-over refresh after the check (the B case): 4.4 MB in 10 parts,
  # faster than the hook may repaint.
  p 2 3 "Getting kpm for firmware 6" 0 0 ""
  for i in 0 1 2 3 4 5 6 7 8 9 10; do
    sleep 0.5
    p 2 3 "Downloading kpm v0.15.0 for firmware 6" $((i * 461373)) 4613734 bytes
  done
  p 3 3 "Building the firmware 6 bundle" 0 0 ""
  sleep 2.5
  echo 'BEGIN_JSON{"packages":[],"checked":"2026-11-25T00:00:00Z"}'
  ;;
"update --all")
  p 1 2 "Checking kpm" 0 0 ""
  sleep 0.5
  for i in 0 2 4 6 8 10; do
    p 1 2 "Downloading kpm v0.15.0" $((i * 461373)) 4613734 bytes
    sleep 0.5
  done
  p 2 2 "Staging KoboRoot.tgz" 0 0 ""
  sleep 1
  echo 'BEGIN_JSON{"changed":[],"failed":[],"staged":false,"reboot_required":false}'
  ;;
*)
  exec "$REAL_KPM" "$@"
  ;;
esac
