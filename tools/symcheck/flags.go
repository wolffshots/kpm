package main

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
)

// defaultSrc is the nkpm.cc location assumed when run from the repo root.
const defaultSrc = "hook/src/nkpm.cc"

const usage = `symcheck — desk-check NickelKPM firmware compatibility

Usage:
  symcheck [flags] <input> [<input> ...]

Each <input> is auto-detected and may be:
  - a Kobo firmware update .zip   (kobo-update-X.Y.Z.zip)
  - a KoboRoot.tgz
  - a bare libnickel.so.1.0.0     (ELF)
  - an https:// URL to any of the above (downloaded and cached)

Flags:
  -src   <path>   path to hook/src/nkpm.cc (default: hook/src/nkpm.cc,
                  also tried relative to tools/symcheck)
  -cache <dir>    directory for downloaded firmware (default: OS temp)

Exit codes: 0 all required symbols present, 1 a required symbol MISSING,
2 operational error.`

// parseFlags binds flags and returns the positional inputs.
func parseFlags(srcPath, cacheDir *string) []string {
	flag.StringVar(srcPath, "src", defaultSrc, "path to hook/src/nkpm.cc")
	flag.StringVar(cacheDir, "cache", filepath.Join(os.TempDir(), "kpm-symcheck-cache"), "download cache dir")
	flag.Parse()
	return flag.Args()
}

// sliceReaderAt adapts a byte slice to io.ReaderAt for debug/elf.
func sliceReaderAt(b []byte) io.ReaderAt {
	return bytes.NewReader(b)
}
