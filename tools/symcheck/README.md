# symcheck — NickelKPM firmware symbol desk check

`symcheck` answers the question **"will `libnkpm.so` load on Kobo firmware X?"**
without touching hardware.

NickelKPM (`hook/libnkpm.so`) is a NickelHook mod injected into Nickel. At load
time it resolves ~30 mangled C++ symbols from `libnickel.so.1.0.0` via hook/dlsym
tables in `hook/src/nkpm.cc`. If a **required** symbol is absent from that
firmware's `libnickel`, the whole mod fails to load — historically only
discoverable by installing on a device. `symcheck` turns that into a desk check.

## How it works

1. Parses `hook/src/nkpm.cc` and extracts every `.sym = "_Z..."` / `.name =
   "_Z..."` entry from the `NickelKPMHook[]` and `NickelKPMDlsym[]` tables, plus
   whether each is `.optional = true`. Parsing the **source** (not the built
   `.so`) keeps the checked list from drifting — the dlsym names are just string
   literals in the binary. It fails loudly if it parses zero symbols.
2. Extracts `libnickel.so.1.0.0` from the input (see below).
3. Looks up each mangled name in libnickel's `.dynsym` (the exact table `dlsym`
   resolves against on device) and prints a per-symbol FOUND/MISSING table.

## Usage

Run from the repo root (`projects/kpm`):

```
go run ./tools/symcheck <input> [<input> ...]
```

Each `<input>` is auto-detected by content (not extension) and may be:

- a Kobo firmware update **`.zip`** (`kobo-update-X.Y.Z.zip`) — libnickel is
  pulled from the `KoboRoot.tgz` inside
- a **`KoboRoot.tgz`**
- a bare **`libnickel.so.1.0.0`** (ELF)
- an **`https://` URL** to any of the above (downloaded and cached)

Multiple inputs are checked in one run, each with its own summary.

### Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `-src <path>` | `hook/src/nkpm.cc` (also tried relative to `tools/symcheck`) | path to the NickelKPM source with the symbol tables |
| `-cache <dir>` | `<os-temp>/kpm-symcheck-cache` | where downloaded firmware is cached; re-runs against the same URL are free |

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | all **required** symbols present (missing optionals are warnings only) |
| `1` | at least one **required** symbol MISSING → mod would fail to load |
| `2` | operational error (bad input, download/extract failure) |

With multiple inputs the highest-severity code wins.

### Examples

```sh
# Check a downloaded firmware zip
go run ./tools/symcheck ~/Downloads/kobo-update-4.33.19608.zip

# Check several firmware versions at once
go run ./tools/symcheck fw/kobo-update-4.20.14601.zip fw/kobo-update-4.33.19608.zip

# Download + cache directly from a mirror
go run ./tools/symcheck -cache ./fwcache \
  https://download.kobobooks.com/firmwares/kobo7/May2022/kobo-update-4.33.19608.zip

# Check a bare libnickel you already extracted
go run ./tools/symcheck /path/to/libnickel.so.1.0.0
```

## Notes / limitations

- **Signature drift, not just presence.** A symbol counts as FOUND only if the
  *exact* mangled name matches. Nickel occasionally changes a method's
  const-ness or parameters across firmware, which changes the mangled name and
  makes `dlsym` fail even though a same-named method exists. `symcheck` reports
  the mangled name it looked for, and lists all MISSING mangled names so you can
  spot a near-match by eye. It does not attempt fuzzy matching.
- **`.dynsym` only.** Only dynamic (exported) symbols are checked, because that
  is all `dlsym`/NickelHook can resolve. Undefined/imported entries are ignored.
- Human-readable names in the table come from the entry's `.desc` comment when
  present, otherwise a tiny best-effort Itanium demangling (class::method);
  constructors show as `Class::<constructor>`. The full mangled name is always
  printed for MISSING symbols.
- Stdlib only (`debug/elf`, `archive/zip`, `archive/tar`, `compress/gzip`,
  `net/http`). Not wired into the release build — this is a dev tool.
```

Run `go test ./tools/symcheck/` to verify the parser against the real source and
the check/exit logic (including a missing-required → exit 1 negative test).
```
