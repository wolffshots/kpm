// Command symcheck is a host-side desk check for NickelKPM firmware compatibility.
//
// NickelKPM (hook/libnkpm.so) resolves ~30 mangled C++ symbols from Nickel's
// libnickel.so.1.0.0 at load time. A missing REQUIRED symbol makes the whole mod
// fail to load on that firmware — historically only discoverable on hardware.
// symcheck parses the symbol list straight out of hook/src/nkpm.cc and checks it
// against libnickel's .dynsym, extracted from a firmware .zip, a KoboRoot.tgz, a
// bare libnickel.so*, or an https URL (downloaded and cached).
//
// Exit codes: 0 = all required symbols present, 1 = a required symbol is MISSING,
// 2 = operational error (bad input, download/extract failure).
//
// This is a dev tool only; it is not wired into the release build.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	os.Exit(run())
}

func run() int {
	var srcPath, cacheDir string
	fs := parseFlags(&srcPath, &cacheDir)
	inputs := fs

	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}

	// Locate and parse nkpm.cc — the single source of truth for the symbol list.
	resolved, err := resolveSrc(srcPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	f, err := os.Open(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	syms, err := ParseSymbols(f)
	f.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing %s: %v\n", resolved, err)
		return 2
	}
	SortSymbols(syms)

	reqN, optN := 0, 0
	for _, s := range syms {
		if s.Optional {
			optN++
		} else {
			reqN++
		}
	}
	fmt.Printf("Symbol list: %d symbols from %s (%d required, %d optional)\n",
		len(syms), resolved, reqN, optN)

	exit := 0
	for _, in := range inputs {
		fmt.Printf("\n=== %s ===\n", in)
		data, srcDesc, err := LoadLibnickel(in, cacheDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			exit = worse(exit, 2)
			continue
		}
		dynsym, err := DynSymSet(sliceReaderAt(data))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			exit = worse(exit, 2)
			continue
		}
		results := CheckSymbols(dynsym, syms)
		printTable(os.Stdout, srcDesc, len(dynsym), results)
		if MissingRequired(results) {
			exit = worse(exit, 1)
		}
	}
	return exit
}

// worse returns the higher-severity exit code (2 dominates 1 dominates 0).
func worse(a, b int) int {
	if b > a {
		return b
	}
	return a
}

func printTable(w io.Writer, srcDesc string, dynsymCount int, results []Result) {
	fmt.Fprintf(w, "  libnickel: %s (%d defined dynamic symbols)\n\n", srcDesc, dynsymCount)

	// Column width for the human-readable name.
	nameW := 20
	for _, r := range results {
		if l := len(humanName(r.Symbol)); l > nameW {
			nameW = l
		}
	}
	if nameW > 52 {
		nameW = 52
	}

	fmt.Fprintf(w, "  %-8s %-8s %-*s  %s\n", "STATUS", "KIND", nameW, "SYMBOL", "")
	fmt.Fprintf(w, "  %-8s %-8s %-*s  %s\n", strings.Repeat("-", 6), strings.Repeat("-", 4), nameW, strings.Repeat("-", 6), "")

	var reqTotal, reqFound, optTotal, optFound int
	var missing []Result
	for _, r := range results {
		status := "FOUND"
		if !r.Found {
			status = "MISSING"
			missing = append(missing, r)
		}
		kind := "required"
		if r.Symbol.Optional {
			kind = "optional"
			optTotal++
			if r.Found {
				optFound++
			}
		} else {
			reqTotal++
			if r.Found {
				reqFound++
			}
		}
		name := truncate(humanName(r.Symbol), nameW)
		fmt.Fprintf(w, "  %-8s %-8s %-*s\n", status, kind, nameW, name)
	}

	fmt.Fprintln(w)
	if len(missing) > 0 {
		fmt.Fprintln(w, "  Missing symbols (full mangled names):")
		for _, r := range missing {
			tag := "REQUIRED"
			if r.Symbol.Optional {
				tag = "optional"
			}
			fmt.Fprintf(w, "    [%s] %s\n", tag, r.Symbol.Mangled)
		}
		fmt.Fprintln(w)
	}

	reqMiss := reqTotal - reqFound
	optMiss := optTotal - optFound
	switch {
	case reqMiss > 0:
		fmt.Fprintf(w, "  RESULT: INCOMPATIBLE — %d/%d required symbols MISSING (%d optional also missing)\n",
			reqMiss, reqTotal, optMiss)
	case optMiss > 0:
		fmt.Fprintf(w, "  RESULT: OK (with warnings) — all %d required symbols found; %d/%d optional missing (entry point degrades gracefully)\n",
			reqTotal, optMiss, optTotal)
	default:
		fmt.Fprintf(w, "  RESULT: OK — all %d symbols found (%d required, %d optional)\n", reqTotal+optTotal, reqTotal, optTotal)
	}
}

// humanName returns a readable label for a symbol: its source .desc if present,
// else a best-effort demangling, else the mangled name.
func humanName(s Symbol) string {
	if s.Desc != "" {
		return s.Desc
	}
	if d, ok := demangle(s.Mangled); ok {
		return d
	}
	return s.Mangled
}

// demangle is a tiny best-effort Itanium demangler covering the shapes NickelKPM
// uses: nested names `_ZN<len><name>...E` and plain `_Z<len><name>`. It returns
// "Class::method" style output; params are ignored. Not a general demangler.
func demangle(s string) (string, bool) {
	if !strings.HasPrefix(s, "_Z") {
		return "", false
	}
	r := s[2:]
	if strings.HasPrefix(r, "N") {
		r = r[1:]
		for len(r) > 0 && (r[0] == 'K' || r[0] == 'V' || r[0] == 'r') { // CV-qualifiers
			r = r[1:]
		}
	}
	var parts []string
	for len(r) > 0 && r[0] >= '0' && r[0] <= '9' {
		j := 0
		for j < len(r) && r[j] >= '0' && r[j] <= '9' {
			j++
		}
		n, err := strconv.Atoi(r[:j])
		if err != nil || j+n > len(r) {
			break
		}
		parts = append(parts, r[j:j+n])
		r = r[j+n:]
	}
	if len(parts) == 0 {
		return "", false
	}
	name := strings.Join(parts, "::")
	switch {
	case strings.HasPrefix(r, "C"): // C1/C2/C3 constructor
		name += "::<constructor>"
	case strings.HasPrefix(r, "D"): // destructor
		name += "::<destructor>"
	}
	return name, true
}

func truncate(s string, w int) string {
	if len(s) <= w {
		return s
	}
	if w <= 1 {
		return s[:w]
	}
	return s[:w-1] + "…"
}

// resolveSrc finds nkpm.cc: the explicit flag value, else a small list of
// defaults relative to the current directory (repo root or tools/symcheck).
func resolveSrc(flagVal string) (string, error) {
	candidates := []string{flagVal}
	if flagVal == defaultSrc {
		candidates = []string{
			defaultSrc,
			filepath.Join("..", "..", "hook", "src", "nkpm.cc"),
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("could not find nkpm.cc (tried %s); pass -src", strings.Join(candidates, ", "))
}
