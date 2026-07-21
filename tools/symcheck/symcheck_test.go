package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realSrc is the actual hook/src/nkpm.cc relative to tools/symcheck (where tests run).
const realSrc = "../../hook/src/nkpm.cc"

// TestParseRealSource verifies the parser extracts exactly the symbol set present
// in the real nkpm.cc. If nkpm.cc gains/loses symbols this test must be updated
// deliberately — that is the point: it pins the symbol list against drift.
func TestParseRealSource(t *testing.T) {
	f, err := os.Open(realSrc)
	if err != nil {
		t.Fatalf("open %s: %v", realSrc, err)
	}
	defer f.Close()

	syms, err := ParseSymbols(f)
	if err != nil {
		t.Fatalf("ParseSymbols: %v", err)
	}

	// Since 0.7.0 the UI is launched from a NickelMenu item (NICKELMENU-LAUNCH.md),
	// so the hook injects no menu row and dlsyms only the dialog symbols. Six
	// optional (all null-checked): N3Dialog::enableFullViewMode,
	// N3Dialog::enableBackButton, and the status-bar chrome-control set
	// (statusBarController/hide/show/isVisible).
	const wantTotal, wantOptional = 33, 6
	if len(syms) != wantTotal {
		t.Errorf("total symbols = %d, want %d", len(syms), wantTotal)
	}
	opt := 0
	for _, s := range syms {
		if s.Optional {
			opt++
		}
	}
	if opt != wantOptional {
		t.Errorf("optional symbols = %d, want %d", opt, wantOptional)
	}

	// A known required symbol must be present with its .desc absent (no comment).
	if !hasSym(syms, "_ZN20MainWindowController14sharedInstanceEv") {
		t.Error("missing expected required symbol MainWindowController::sharedInstance")
	}
	// The obsolete menu-injection symbols must be gone.
	for _, m := range []string{
		"_ZN28AbstractNickelMenuController18createMenuTextItemEP5QMenuRK7QStringbbS4_",
		"_ZN22AbstractMenuController12createActionEP5QMenuP7QWidgetbbb",
	} {
		if hasSym(syms, m) {
			t.Errorf("obsolete menu symbol %s should no longer be present", m)
		}
	}
}

// TestParseFailsOnEmpty ensures the parser fails loudly rather than silently
// reporting success when the file shape yields no symbols.
func TestParseFailsOnEmpty(t *testing.T) {
	_, err := ParseSymbols(strings.NewReader("int main() { return 0; }\n"))
	if err == nil {
		t.Fatal("expected error on source with zero symbols, got nil")
	}
}

// TestParseIgnoresSymNew confirms .sym is captured but the adjacent .sym_new is not.
func TestParseIgnoresSymNew(t *testing.T) {
	line := `{ .sym = "_ZReal", .sym_new = "_my_hook", .lib = "libnickel.so.1.0.0", .optional = true },`
	syms, err := ParseSymbols(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 1 {
		t.Fatalf("got %d symbols, want 1: %+v", len(syms), syms)
	}
	if syms[0].Mangled != "_ZReal" {
		t.Errorf("mangled = %q, want _ZReal", syms[0].Mangled)
	}
	if !syms[0].Optional {
		t.Error("symbol should be optional")
	}
}

// TestMissingRequiredExit1 is the core negative test: with the real symbol list
// checked against a dynsym set that is missing one REQUIRED symbol, the table
// logic must report MISSING/INCOMPATIBLE and MissingRequired must be true (the
// signal main() maps to exit code 1). No real ELF is needed — the ELF read is
// factored out of the check via DynSymSet, so we feed the set directly.
func TestMissingRequiredExit1(t *testing.T) {
	f, err := os.Open(realSrc)
	if err != nil {
		t.Fatal(err)
	}
	syms, err := ParseSymbols(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	SortSymbols(syms)

	// Build a dynsym set containing every symbol EXCEPT one required one.
	drop := "_ZN20MainWindowController14sharedInstanceEv"
	dynsym := map[string]bool{}
	for _, s := range syms {
		if s.Mangled != drop {
			dynsym[s.Mangled] = true
		}
	}

	results := CheckSymbols(dynsym, syms)
	if !MissingRequired(results) {
		t.Fatal("MissingRequired = false, want true (a required symbol was dropped)")
	}
	if got := worse(0, boolExit(MissingRequired(results))); got != 1 {
		t.Fatalf("exit signal = %d, want 1", got)
	}

	var buf bytes.Buffer
	printTable(&buf, "synthetic", len(dynsym), results)
	out := buf.String()
	if !strings.Contains(out, "MISSING") {
		t.Error("table output should contain MISSING")
	}
	if !strings.Contains(out, "INCOMPATIBLE") {
		t.Error("table output should report INCOMPATIBLE result")
	}
	if !strings.Contains(out, drop) {
		t.Errorf("table output should list the dropped mangled name %s", drop)
	}
}

// TestAllFoundExit0 confirms that when every symbol is present the check passes
// and no required symbol is flagged missing.
func TestAllFoundExit0(t *testing.T) {
	f, err := os.Open(realSrc)
	if err != nil {
		t.Fatal(err)
	}
	syms, err := ParseSymbols(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	dynsym := map[string]bool{}
	for _, s := range syms {
		dynsym[s.Mangled] = true
	}
	results := CheckSymbols(dynsym, syms)
	if MissingRequired(results) {
		t.Fatal("MissingRequired = true, want false (all symbols present)")
	}
}

// TestOptionalMissingIsWarning confirms a missing OPTIONAL symbol does not flag
// the firmware incompatible (exit stays 0).
func TestOptionalMissingIsWarning(t *testing.T) {
	syms := []Symbol{
		{Mangled: "_ZReq"},
		{Mangled: "_ZOpt", Optional: true},
	}
	dynsym := map[string]bool{"_ZReq": true} // optional one missing
	results := CheckSymbols(dynsym, syms)
	if MissingRequired(results) {
		t.Error("a missing optional symbol must not count as a required miss")
	}
	var buf bytes.Buffer
	printTable(&buf, "synthetic", len(dynsym), results)
	if !strings.Contains(buf.String(), "OK (with warnings)") {
		t.Errorf("expected OK-with-warnings result, got:\n%s", buf.String())
	}
}

// TestDemangle spot-checks the readability helper.
func TestDemangle(t *testing.T) {
	cases := map[string]string{
		"_ZN20MainWindowController14sharedInstanceEv": "MainWindowController::sharedInstance",
		"_ZNK20MainWindowController11currentViewEv":   "MainWindowController::currentView",
		"_ZN13TouchLineEditC1EP7QWidget":              "TouchLineEdit::<constructor>",
	}
	for in, want := range cases {
		got, ok := demangle(in)
		if !ok || got != want {
			t.Errorf("demangle(%s) = %q,%v; want %q", in, got, ok, want)
		}
	}
}

// boolExit maps MissingRequired's bool to the exit code main() would choose.
func boolExit(missing bool) int {
	if missing {
		return 1
	}
	return 0
}

func hasSym(syms []Symbol, mangled string) bool { return findSym(syms, mangled) != nil }

func findSym(syms []Symbol, mangled string) *Symbol {
	for i := range syms {
		if syms[i].Mangled == mangled {
			return &syms[i]
		}
	}
	return nil
}

// ensure realSrc path is correct at test time (helpful failure if repo moves).
func TestSrcExists(t *testing.T) {
	if _, err := os.Stat(filepath.FromSlash(realSrc)); err != nil {
		t.Fatalf("real nkpm.cc not found at %s: %v", realSrc, err)
	}
}
