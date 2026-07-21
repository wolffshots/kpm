<#
.SYNOPSIS
    Capture a screenshot from a Kobo e-reader over SSH, copy it to this PC,
    then safely delete the remote copy.

.DESCRIPTION
    Flow (each ssh/scp step prompts for the Kobo's SSH password natively):
      1. Run fbgrab ON the Kobo, writing a PNG to a tmpfs path (/tmp/...).
      2. Verify the capture succeeded remotely (tool exit code AND a non-empty
         remote file) BEFORE transferring.
      3. scp the PNG to a local screenshots directory (timestamped filename).
      4. Verify the local file exists and is non-empty, and ONLY THEN delete the
         remote file with a targeted `rm -f <exact path>` (never a wildcard).
      5. Print the final local path.

    Screenshot method:
      The Kobo framebuffer on recent Mk8+/Mk13 firmware is NOT a plain RGB dump
      (rotation / 16-or-32bpp / stride quirks), so a naive `cat /dev/fb0` is
      unreliable. This script uses NiLuJe's fbgrab (shipped with FBInk), which
      understands those quirks and writes a correct PNG via just `fbgrab out.png`
      (it reads /dev/fb0 by default). The script probes a list of common install
      locations and the PATH, using the first fbgrab it finds.

      Note (context only): Nickel's built-in gesture screenshot writes PNGs to
      /mnt/onboard/screenshots/, but it can't be triggered cleanly over SSH, so
      it is deliberately NOT used here.

    Why scp (not ssh stdout): piping binary over ssh into a PowerShell `>`
    redirect corrupts the bytes (encoding/newline translation), so we always
    capture to a remote file and scp it down.

.PARAMETER KoboHost
    SSH target. Default: root@192.168.88.132

.PARAMETER LocalDir
    Local directory to save the PNG into (created if missing).
    Default: C:\Users\Jadon\Claude\Projects\Kobo\screenshots

.NOTES
    Password: this script NEVER stores or handles your SSH password. ssh and scp
    prompt for it natively. To avoid repeated prompts you can set up SSH key auth
    (put your public key in /mnt/onboard/.adds/koreader/settings/SSH/authorized_keys
    on the Kobo, or the root account's authorized_keys, depending on your setup).

    If fbgrab is not found, install FBInk (which bundles fbgrab) on the Kobo —
    e.g. via NiLuJe's "KoboStuff" package, or it ships inside KFMon / KOReader.

.EXAMPLE
    .\kobo-screenshot.ps1

.EXAMPLE
    .\kobo-screenshot.ps1 -KoboHost root@192.168.88.140 -LocalDir D:\shots
#>

param(
    [string]$KoboHost = "root@192.168.88.132",
    [string]$LocalDir = "C:\Users\Jadon\Claude\Projects\Kobo\screenshots"
)

$ErrorActionPreference = "Stop"

# --- Timestamp comes from THIS PC (per spec) -------------------------------
$ts         = Get-Date -Format "yyyyMMdd-HHmmss"
$fileName   = "kobo-shot-$ts.png"
$remotePath = "/tmp/$fileName"                      # tmpfs on the Kobo
$localPath  = Join-Path $LocalDir $fileName

Write-Host "Kobo host   : $KoboHost"
Write-Host "Remote path : $remotePath"
Write-Host "Local path  : $localPath"
Write-Host ""

# --- Ensure local destination exists ---------------------------------------
if (-not (Test-Path -LiteralPath $LocalDir)) {
    New-Item -ItemType Directory -Path $LocalDir -Force | Out-Null
    Write-Host "Created local directory: $LocalDir"
}

# --- Remote capture script (POSIX sh, runs on the Kobo) --------------------
# Probes candidate fbgrab paths, runs the first that exists, and verifies the
# output is a non-empty file. $1 = output path. Exit codes:
#   10 = no capture tool found   11 = capture command failed
#   12 = output missing/empty    20 = no output path given   0 = success
$remoteScript = @'
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
'@

# Strip CR so the remote busybox sh never sees \r (PowerShell here-strings use
# CRLF; a stray \r turns into a "$'\r' not found" error on the device).
$remoteScript = $remoteScript -replace "`r", ""

# Ship the script as base64 to sidestep all cross-shell quoting issues, then
# decode + run it on the device with the output path as $1.
$b64        = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($remoteScript))
$remoteCmd  = "echo $b64 | base64 -d | sh -s '$remotePath'"

# --- Step 1+2: capture + verify remotely (1st password prompt) -------------
Write-Host "== Capturing on device (enter your Kobo SSH password when prompted) =="
$captureOut = & ssh $KoboHost $remoteCmd
$captureRc  = $LASTEXITCODE

if ($captureOut) { $captureOut | ForEach-Object { Write-Host "  [device] $_" } }

if ($captureRc -ne 0) {
    switch ($captureRc) {
        10 {
            Write-Host ""
            Write-Host "ERROR: No fbgrab found on the Kobo." -ForegroundColor Red
            Write-Host "Install FBInk (which bundles fbgrab): NiLuJe's 'KoboStuff' package," -ForegroundColor Red
            Write-Host "or it ships inside KFMon / KOReader (.adds). See github.com/NiLuJe/FBInk." -ForegroundColor Red
        }
        11 { Write-Host "`nERROR: fbgrab ran but the capture command failed on the device." -ForegroundColor Red }
        12 { Write-Host "`nERROR: Capture produced an empty/missing file on the device." -ForegroundColor Red }
        20 { Write-Host "`nERROR: No output path was passed to the remote script." -ForegroundColor Red }
        default { Write-Host "`nERROR: ssh/capture failed (exit $captureRc). Check host/network/password." -ForegroundColor Red }
    }
    Write-Host "Nothing was transferred or deleted." -ForegroundColor Red
    exit 1
}

Write-Host "Capture verified on device."
Write-Host ""

# --- Step 3: transfer (2nd password prompt) --------------------------------
Write-Host "== Copying to this PC (enter your Kobo SSH password again) =="
& scp "${KoboHost}:${remotePath}" $localPath
$scpRc = $LASTEXITCODE

if ($scpRc -ne 0) {
    Write-Host ""
    Write-Host "ERROR: scp failed (exit $scpRc)." -ForegroundColor Red
    Write-Host "The screenshot is STILL ON THE KOBO at: $remotePath" -ForegroundColor Yellow
    Write-Host "It was NOT deleted, so you can retry the copy manually." -ForegroundColor Yellow
    exit 1
}

# --- Step 4: verify local file BEFORE deleting remote ----------------------
if (-not (Test-Path -LiteralPath $localPath)) {
    Write-Host "`nERROR: scp reported success but local file is missing: $localPath" -ForegroundColor Red
    Write-Host "Remote file left in place at: $remotePath" -ForegroundColor Yellow
    exit 1
}
$localSize = (Get-Item -LiteralPath $localPath).Length
if ($localSize -le 0) {
    Write-Host "`nERROR: Local file is empty: $localPath" -ForegroundColor Red
    Write-Host "Remote file left in place at: $remotePath" -ForegroundColor Yellow
    exit 1
}
Write-Host "Local file verified ($localSize bytes)."
Write-Host ""

# --- Step 4b: targeted delete of the remote file (3rd password prompt) -----
# Exact path only. No wildcards, no directories.
Write-Host "== Deleting remote copy (enter your Kobo SSH password again) =="
& ssh $KoboHost "rm -f '$remotePath'"
$rmRc = $LASTEXITCODE

if ($rmRc -ne 0) {
    Write-Host "WARNING: Could not delete the remote file (exit $rmRc)." -ForegroundColor Yellow
    Write-Host "Your screenshot is safe locally; the tmpfs copy at $remotePath" -ForegroundColor Yellow
    Write-Host "will disappear on the next device reboot anyway." -ForegroundColor Yellow
} else {
    Write-Host "Remote copy deleted."
}

# --- Step 5: report ---------------------------------------------------------
Write-Host ""
Write-Host "Done. Screenshot saved to:" -ForegroundColor Green
Write-Host "  $localPath" -ForegroundColor Green
