#!/usr/bin/env bash
#
# kobo-screenshot.sh
#
# Capture a screenshot from a Kobo e-reader over SSH, copy it to this PC
# (git-bash on Windows), then safely delete the remote copy.
#
# Flow (each ssh/scp step prompts for the Kobo's SSH password natively):
#   1. Run fbgrab ON the Kobo, writing a PNG to a tmpfs path (/tmp/...).
#   2. Verify the capture succeeded remotely (tool exit code AND a non-empty
#      remote file) BEFORE transferring.
#   3. scp the PNG to a local screenshots directory (timestamped filename).
#   4. Verify the local file exists and is non-empty, then ONLY THEN delete the
#      remote file with a targeted `rm -f <exact path>` (never a wildcard).
#   5. Print the final local path.
#
# Screenshot method:
#   Recent Kobo firmware (Mk8+/Mk13) does NOT expose a plain RGB framebuffer
#   (rotation / 16-or-32bpp / stride quirks), so `cat /dev/fb0` is unreliable.
#   This script uses NiLuJe's fbgrab (bundled with FBInk), which handles those
#   quirks and writes a correct PNG via just `fbgrab out.png` (reads /dev/fb0 by
#   default). We probe common install locations and the PATH for it.
#
#   Context only: Nickel's gesture screenshot writes PNGs to
#   /mnt/onboard/screenshots/, but it can't be triggered cleanly over SSH, so it
#   is deliberately NOT used here.
#
#   Why scp (not ssh stdout): piping binary over ssh into a shell redirect can
#   corrupt bytes; we always capture to a remote file and scp it down.
#
# Params (positional):
#   $1  KoboHost  (default: root@192.168.88.132)
#   $2  LocalDir  (default: C:/Users/Jadon/Claude/Projects/Kobo/screenshots)
#
# Password: this script NEVER stores or handles your SSH password. ssh/scp
# prompt for it natively. To avoid repeated prompts, set up SSH key auth
# (add your public key to the Kobo's authorized_keys).
#
# If fbgrab is not found, install FBInk (which bundles fbgrab) on the Kobo:
# NiLuJe's "KoboStuff" package, or it ships inside KFMon / KOReader (.adds).
# See github.com/NiLuJe/FBInk.
#
# Usage:
#   ./kobo-screenshot.sh
#   ./kobo-screenshot.sh root@192.168.88.140 /d/shots

set -u

KOBO_HOST="${1:-root@192.168.88.132}"
LOCAL_DIR="${2:-C:/Users/Jadon/Claude/Projects/Kobo/screenshots}"

# --- Timestamp comes from THIS PC (per spec) -------------------------------
TS="$(date +%Y%m%d-%H%M%S)"
FILE_NAME="kobo-shot-${TS}.png"
REMOTE_PATH="/tmp/${FILE_NAME}"          # tmpfs on the Kobo
LOCAL_PATH="${LOCAL_DIR}/${FILE_NAME}"

echo "Kobo host   : ${KOBO_HOST}"
echo "Remote path : ${REMOTE_PATH}"
echo "Local path  : ${LOCAL_PATH}"
echo

# --- Ensure local destination exists ---------------------------------------
mkdir -p "${LOCAL_DIR}"

# --- Remote capture script (POSIX sh, runs on the Kobo) --------------------
# Probes candidate fbgrab paths, runs the first that exists, verifies output.
# $1 = output path. Exit codes:
#   10 = no tool found  11 = capture failed  12 = empty/missing
#   20 = no output path  0 = success
read -r -d '' REMOTE_SCRIPT <<'EOS' || true
set -e
OUT="$1"
if [ -z "$OUT" ]; then echo "NO_OUT"; exit 20; fi
TOOL=""
for c in \
    /usr/local/bin/fbgrab \
    /usr/bin/fbgrab \
    /mnt/onboard/.adds/kfmon/bin/fbgrab \
    /mnt/onboard/.adds/koreader/fbgrab \
    /mnt/onboard/.adds/nm/fbgrab \
    fbgrab ; do
    if command -v "$c" >/dev/null 2>&1; then TOOL="$c"; break; fi
done
if [ -z "$TOOL" ]; then echo "NO_TOOL"; exit 10; fi
echo "TOOL=$TOOL"
if ! "$TOOL" "$OUT" >/dev/null 2>&1; then echo "CAPTURE_FAILED"; rm -f "$OUT"; exit 11; fi
if [ ! -s "$OUT" ]; then echo "EMPTY"; rm -f "$OUT"; exit 12; fi
SZ=$(wc -c < "$OUT" 2>/dev/null || echo 0)
echo "OK SIZE=$SZ"
exit 0
EOS

# Ship as base64 to sidestep cross-shell quoting; decode + run on device with
# the output path as $1. (busybox base64 -d and sh -s are available on Kobo.)
B64="$(printf '%s' "${REMOTE_SCRIPT}" | base64 | tr -d '\n')"
REMOTE_CMD="echo ${B64} | base64 -d | sh -s '${REMOTE_PATH}'"

# --- Step 1+2: capture + verify remotely (1st password prompt) -------------
echo "== Capturing on device (enter your Kobo SSH password when prompted) =="
CAPTURE_OUT="$(ssh "${KOBO_HOST}" "${REMOTE_CMD}")"
CAPTURE_RC=$?

if [ -n "${CAPTURE_OUT}" ]; then
    while IFS= read -r line; do echo "  [device] ${line}"; done <<< "${CAPTURE_OUT}"
fi

if [ "${CAPTURE_RC}" -ne 0 ]; then
    case "${CAPTURE_RC}" in
        10)
            echo
            echo "ERROR: No fbgrab found on the Kobo." >&2
            echo "Install FBInk (bundles fbgrab): NiLuJe's 'KoboStuff' package, or it" >&2
            echo "ships inside KFMon / KOReader (.adds). See github.com/NiLuJe/FBInk." >&2
            ;;
        11) echo -e "\nERROR: fbgrab ran but the capture command failed on the device." >&2 ;;
        12) echo -e "\nERROR: Capture produced an empty/missing file on the device." >&2 ;;
        20) echo -e "\nERROR: No output path was passed to the remote script." >&2 ;;
        *)  echo -e "\nERROR: ssh/capture failed (exit ${CAPTURE_RC}). Check host/network/password." >&2 ;;
    esac
    echo "Nothing was transferred or deleted." >&2
    exit 1
fi

echo "Capture verified on device."
echo

# --- Step 3: transfer (2nd password prompt) --------------------------------
echo "== Copying to this PC (enter your Kobo SSH password again) =="
scp "${KOBO_HOST}:${REMOTE_PATH}" "${LOCAL_PATH}"
SCP_RC=$?

if [ "${SCP_RC}" -ne 0 ]; then
    echo
    echo "ERROR: scp failed (exit ${SCP_RC})." >&2
    echo "The screenshot is STILL ON THE KOBO at: ${REMOTE_PATH}" >&2
    echo "It was NOT deleted, so you can retry the copy manually." >&2
    exit 1
fi

# --- Step 4: verify local file BEFORE deleting remote ----------------------
if [ ! -s "${LOCAL_PATH}" ]; then
    echo
    echo "ERROR: Local file missing or empty: ${LOCAL_PATH}" >&2
    echo "Remote file left in place at: ${REMOTE_PATH}" >&2
    exit 1
fi
LOCAL_SIZE="$(wc -c < "${LOCAL_PATH}" | tr -d ' ')"
echo "Local file verified (${LOCAL_SIZE} bytes)."
echo

# --- Step 4b: targeted delete of the remote file (3rd password prompt) -----
# Exact path only. No wildcards, no directories.
echo "== Deleting remote copy (enter your Kobo SSH password again) =="
ssh "${KOBO_HOST}" "rm -f '${REMOTE_PATH}'"
RM_RC=$?

if [ "${RM_RC}" -ne 0 ]; then
    echo "WARNING: Could not delete the remote file (exit ${RM_RC})." >&2
    echo "Your screenshot is safe locally; the tmpfs copy at ${REMOTE_PATH}" >&2
    echo "will disappear on the next device reboot anyway." >&2
else
    echo "Remote copy deleted."
fi

# --- Step 5: report ---------------------------------------------------------
echo
echo "Done. Screenshot saved to:"
echo "  ${LOCAL_PATH}"
