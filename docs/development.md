# Build and test kpm

## Go binary and packages

```sh
go vet ./...
go test ./...
go run ./build
```

- `go test ./...` runs the unit tests on any host: forge fixtures with `httptest`, tar merge, URL parsing and state reconcile.
- `go run ./build` cross-compiles the static `linux/arm` binary (`GOARM=7`, `CGO_ENABLED=0`). It writes `dist/kpm`, `dist/KoboRoot.tgz` and `dist/Kobo.tgz`. It assembles the archives in Go, so Windows needs no `tar` or WSL. It then checks the member list and the modes of each archive.
- `KPM_VERSION=<v> go run ./build` sets the version in the binary.
- `dist/KoboRoot.tgz` needs the Qt 5 hook, `hook/libnkpm.so`. `KPM_UI_SO` gives another path for it.
- `dist/Kobo.tgz` is the package for firmware 4.x and 6.x. It needs both hook builds, `hook/libnkpm.so` and `hook/libnkpm-qt6.so`.

## The NickelKPM hook

The hook is C++. Build it with the NickelTC cross-toolchain in a container. From the repository root:

```sh
git submodule update --init --recursive          # fetch hook/NickelHook (once)
podman run --rm -v "$PWD":"$PWD" -w "$PWD/hook" ghcr.io/pgaskin/nickeltc:1.0 make
```

That makes `hook/libnkpm.so` (Qt 5, firmware 4.x). The Qt 6 build for firmware 6.x uses another image and makes `hook/libnkpm-qt6.so`:

```sh
podman run --rm -v "$PWD":"$PWD" -w "$PWD/hook" ghcr.io/nicoverbruggen/kobuild:qt6-v0.5.3 make NH_QT_MAJOR=6
```

On Windows the volume mount is `-v "C:/.../kpm:/work" -w /work/hook`. Under Git Bash, set `MSYS_NO_PATHCONV=1`.

`hook/sim/README.md` describes a desktop simulator of the UI.

## Symbol check

`go run ./tools/symcheck <firmware>` checks that each Nickel symbol of the hook resolves against the `libnickel.so.1.0.0` of a firmware. It accepts a firmware zip, a `KoboRoot.tgz`, a bare `libnickel.so*` or an https URL. See `tools/symcheck/README.md`.

Run it against the newest firmware before a release. If a required symbol is missing, the UI does not load on that firmware.

[kfw](https://github.com/wolffshots/kfw) does the same check for many firmwares at once, for Qt 5 and Qt 6 (`kfw check -qt 5,6 hook/src`). `kfw test` checks `Kobo.tgz` and `KoboRoot.tgz`, and `kfw vm` boots Nickel with the package in an ARM emulator.

## Machine-readable output

These commands accept `--json`: `status`, `list`, `search`, `check`, `install`, `update`, `uninstall`, `sync`, `unstage`, `registry list`, `registry refresh`, `config list`, `config show`, `config set`, `config init`, `doctor`, `adopt-self`, `ssh status` and `version`.

Human and progress output streams as usual. The last stdout line is the marker `BEGIN_JSON` followed by one compact JSON object. The UI reads this object. Scripts can use it too.

## Environment variables

- `KPM_ROOT` replaces the `/mnt/onboard` root. Tests and dev runs off the device use it.
- `KPM_SYSROOT` replaces the rootfs `/`, so that `uninstall` deletes files inside a sandbox during tests.

## Lock

Only one mutating kpm command runs at a time. It takes a lock at `.adds/kpm/lock`. A second mutating command fails with "another kpm instance is running". kpm breaks a lock that is older than 10 minutes.

Read-only commands do not take the lock and never write state: `list`, `status`, `log`, `version`, `search`, `doctor`, `registry list`, `config list` and `config show`.
