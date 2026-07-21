#!/usr/bin/env bash
# hook/sim/run.sh — build everything, seed an offline sandbox, and launch the
# desktop simulator against a host-built kpm binary. Run from anywhere.
#
#   ./run.sh                         interactive window (WSLg on Win11)
#   ./run.sh --screenshot out        offscreen: write out/browse.png + detail.png
#   ./run.sh --exercise-uninstall samplemod   offscreen: drive the uninstall flow
#   ./run.sh --size 600x800          override the Kobo-portrait window size
#
# Env: SANDBOX (default /tmp/kpm-sim-sandbox), RESEED=0 to keep an existing
# sandbox. Go is found as `go` (WSL) or `go.exe` (Windows) for cross-building the
# linux kpm + seed binaries.
set -euo pipefail

SIM="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SIM/../.." && pwd)"
BUILD="$SIM/build"
SANDBOX="${SANDBOX:-/tmp/kpm-sim-sandbox}"
SYSROOT="$SANDBOX/sysroot"
export KPM_SYSROOT="$SYSROOT"
export KPM_ROOT="$SYSROOT/mnt/onboard"

mkdir -p "$BUILD"

# ---- locate Go (WSL native, else Windows go.exe) ------------------------
GO=""
if command -v go >/dev/null 2>&1; then
  GO="wsl"
elif command -v cmd.exe >/dev/null 2>&1 && command -v go.exe >/dev/null 2>&1; then
  GO="win"
fi

build_go() {
  local out="$1" pkg="$2"
  case "$GO" in
    wsl)
      ( cd "$REPO" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$out" "$pkg" ) ;;
    win)
      # go.exe is a Windows process and WSL does not forward GOOS/GOARCH across the
      # interop boundary (a bare `GOOS=linux go.exe build` yields a Windows PE). Set
      # the target inside a Windows shell so the Windows Go cross-compiles to linux.
      ( cd "$REPO" && cmd.exe /c "set GOOS=linux&&set GOARCH=amd64&&set CGO_ENABLED=0&&go build -o $out $pkg" ) ;;
    *)
      if [ ! -x "$out" ]; then
        echo "run.sh: no Go toolchain and $out missing; cross-build it on the host first" >&2
        exit 1
      fi ;;
  esac
}

echo "== building linux kpm + seed (Go: ${GO:-prebuilt}) =="
build_go "hook/sim/build/kpm"  "./cmd/kpm"
build_go "hook/sim/build/seed" "./hook/sim/fixture"

echo "== building nkpm-sim (Qt5) =="
make -s -C "$SIM"

# ---- seed the sandbox ---------------------------------------------------
if [ "${RESEED:-1}" = "1" ]; then
  rm -rf "$SANDBOX"
fi
mkdir -p "$SYSROOT"
"$BUILD/seed"

# ---- run ----------------------------------------------------------------
export NKPM_KPM="$BUILD/kpm"

# Offscreen modes need the offscreen QPA platform; an interactive run uses WSLg.
case " $* " in
  *" --screenshot "*|*" --exercise-uninstall "*) export QT_QPA_PLATFORM=offscreen ;;
esac

echo "== launching sim (NKPM_KPM=$NKPM_KPM) =="
exec "$SIM/nkpm-sim" "$@"
