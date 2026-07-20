// Command build cross-compiles the kpm ARM binary and assembles KoboRoot.tgz
// entirely in Go (no tar.exe / WSL needed on Windows), then self-checks the
// archive's exact member list and modes. Run with: go run ./build
package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// tgz member paths (with the ./ prefix rcS expects) and their modes.
const (
	memBin  = "./mnt/onboard/.adds/kpm/bin/kpm"
	memToml = "./mnt/onboard/.adds/kpm/packages.d/kpm.toml"
	memNm   = "./mnt/onboard/.adds/nm/kpm"
)

// selfToml is kpm's own package definition shipped in the tgz. It ships with
// pin = "" on purpose: kpm's pin lives in state.json to survive self-update
// overwrite (§10). source is empty until kpm's release repo exists: an empty
// source marks self-update "unconfigured" — check/update skip it silently and
// list/status report it, never an error (F7). Set source once the repo exists,
// e.g. source = "codeberg.org/<owner>/kpm".
const selfToml = `name = "kpm"
# source: set to host/owner/repo once kpm's release repo exists to enable self-update.
source = ""
forge = "forgejo"
asset = "KoboRoot.tgz"
pin = ""
`

// tgzFile is one archive member.
type tgzFile struct {
	name string
	mode int64
	data []byte
}

func main() {
	version := os.Getenv("KPM_VERSION")
	if version == "" {
		version = "0.4.1"
	}

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	distDir := filepath.Join(root, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		fatal(err)
	}
	binOut := filepath.Join(distDir, "kpm")

	// 1. Cross-compile the static ARM binary.
	fmt.Printf("building linux/arm/7 kpm %s...\n", version)
	if err := buildBinary(root, binOut, version); err != nil {
		fatal(fmt.Errorf("build binary: %w", err))
	}

	// 2. Assemble KoboRoot.tgz.
	binData, err := os.ReadFile(binOut)
	if err != nil {
		fatal(err)
	}
	nmData, err := os.ReadFile(filepath.Join(root, "res", "nm-config"))
	if err != nil {
		fatal(fmt.Errorf("read res/nm-config: %w", err))
	}
	// A2 self-check: a menu_item label must never contain a ':'.
	if err := verifyNmLabels(nmData); err != nil {
		fatal(fmt.Errorf("res/nm-config: %w", err))
	}
	members := []tgzFile{
		{memBin, 0o755, binData},
		{memToml, 0o644, []byte(selfToml)},
		{memNm, 0o644, nmData},
	}
	tgzOut := filepath.Join(distDir, "KoboRoot.tgz")
	if err := writeTgz(tgzOut, members); err != nil {
		fatal(fmt.Errorf("write tgz: %w", err))
	}

	// 3. Self-check: exact member list and modes.
	if err := verifyTgz(tgzOut); err != nil {
		fatal(fmt.Errorf("self-check: %w", err))
	}

	fmt.Printf("wrote %s\nwrote %s\n", binOut, tgzOut)
	fmt.Println("self-check OK: members and modes match PLAN.md §10, nm labels colon-free")
}

// nmActions are the NickelMenu actions whose argument legitimately contains a
// ':' (so their menu_item line has one extra colon-delimited field).
var nmActionsWithColonArg = map[string]bool{"cmd_output": true}

// verifyNmLabels asserts that no menu_item line's label field contains a ':'.
// NickelMenu splits a menu_item line into menu_item:location:label:action:arg
// (5 fields); a colon in the label mis-splits the line and breaks the whole
// config (A2). cmd_output's arg (timeout:cmd) legitimately adds one field.
func verifyNmLabels(data []byte) error {
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "menu_item") {
			continue
		}
		fields := strings.Split(line, ":")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		if len(fields) < 5 {
			return fmt.Errorf("menu_item line has too few fields (%d): %q", len(fields), line)
		}
		want := 5
		if len(fields) > 3 && nmActionsWithColonArg[fields[3]] {
			want = 6
		}
		if len(fields) != want {
			return fmt.Errorf("menu_item label appears to contain ':' (got %d fields, want %d): %q", len(fields), want, line)
		}
		if strings.Contains(fields[2], ":") {
			return fmt.Errorf("menu_item label contains ':': %q", fields[2])
		}
	}
	return nil
}

// buildBinary invokes go build for linux/arm/7, static (CGO disabled).
func buildBinary(root, out, version string) error {
	cmd := exec.Command("go", "build",
		"-trimpath",
		"-ldflags", "-s -w -X kpm/internal/version.Version="+version,
		"-o", out,
		"./cmd/kpm",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=arm",
		"GOARM=7",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// writeTgz assembles a gzip'd tar with root:root headers and the given modes.
func writeTgz(path string, files []tgzFile) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, m := range files {
		hdr := &tar.Header{
			Name:     m.name,
			Mode:     m.mode,
			Size:     int64(len(m.data)),
			Typeflag: tar.TypeReg,
			Uid:      0,
			Gid:      0,
			Uname:    "root",
			Gname:    "root",
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(m.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil { // durable before self-check reopens it (B3)
		return err
	}
	return f.Close()
}

// verifyTgz reopens the archive and asserts its members and modes match the
// spec exactly.
func verifyTgz(path string) error {
	want := map[string]int64{
		memBin:  0o755,
		memToml: 0o644,
		memNm:   0o644,
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	got := map[string]int64{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		got[hdr.Name] = hdr.Mode & 0o777
		if hdr.Uid != 0 || hdr.Gid != 0 {
			return fmt.Errorf("%s not owned by root:root", hdr.Name)
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("member count %d, want %d (%s)", len(got), len(want), sortedKeys(got))
	}
	for name, mode := range want {
		g, ok := got[name]
		if !ok {
			return fmt.Errorf("missing member %s", name)
		}
		if g != mode {
			return fmt.Errorf("%s mode %#o, want %#o", name, g, mode)
		}
	}
	return nil
}

func sortedKeys(m map[string]int64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// repoRoot returns the kpm module root (dir containing go.mod), starting from
// the working directory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s upward", dir)
		}
		dir = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "build:", err)
	os.Exit(1)
}
