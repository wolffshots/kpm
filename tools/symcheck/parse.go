package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
)

// Symbol is one Nickel symbol that libnkpm.so resolves at load time, extracted
// from the NickelKPMHook[] / NickelKPMDlsym[] tables in hook/src/nkpm.cc.
type Symbol struct {
	Mangled  string // C++ mangled name looked up in libnickel's .dynsym
	Optional bool   // .optional = true entry: a miss degrades gracefully
	Desc     string // human description from the entry's .desc field, if any
}

// symOrNameRe matches a `.sym = "_Z..."` or `.name = "_Z..."` field. Two guards:
//   - It does NOT match `.sym_new = "..."` (the injected hook function name, not a
//     Nickel symbol): after `.sym`/`.name` only whitespace or `=` may follow, and
//     `.sym_new` has an underscore there.
//   - The captured value must start with `_Z` (the Itanium C++ mangling prefix),
//     which every Nickel symbol has. This excludes the mod's own `.name =
//     "NickelKPM"` in the nh_info struct, which is a plain string, not a symbol.
var symOrNameRe = regexp.MustCompile(`\.(?:sym|name)[ \t]*=[ \t]*"(_Z[^"]*)"`)

// optionalRe detects `.optional = true` anywhere on the entry's line.
var optionalRe = regexp.MustCompile(`\.optional[ \t]*=[ \t]*true`)

// descRe pulls the `.desc = "..."` human description from the entry's line.
var descRe = regexp.MustCompile(`\.desc[ \t]*=[ \t]*"([^"]+)"`)

// ParseSymbols scans nkpm.cc source (line based, one table entry per line) and
// returns every Nickel symbol it resolves. It fails loudly if it finds zero
// symbols, which indicates the file shape changed and the parser needs updating
// rather than silently reporting "all good".
func ParseSymbols(r io.Reader) ([]Symbol, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // entries can be long single lines
	var syms []Symbol
	seen := map[string]bool{}
	for sc.Scan() {
		line := sc.Text()
		m := symOrNameRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mangled := m[1]
		if seen[mangled] {
			continue
		}
		seen[mangled] = true
		s := Symbol{Mangled: mangled, Optional: optionalRe.MatchString(line)}
		if d := descRe.FindStringSubmatch(line); d != nil {
			s.Desc = d[1]
		}
		syms = append(syms, s)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading nkpm source: %w", err)
	}
	if len(syms) == 0 {
		return nil, fmt.Errorf("parsed zero symbols from nkpm source: file shape changed (expected .sym=/.name= entries in NickelKPMHook[]/NickelKPMDlsym[]) — update the parser")
	}
	return syms, nil
}

// SortSymbols orders symbols required-first then alphabetically, for stable output.
func SortSymbols(syms []Symbol) {
	sort.SliceStable(syms, func(i, j int) bool {
		if syms[i].Optional != syms[j].Optional {
			return !syms[i].Optional // required before optional
		}
		return syms[i].Mangled < syms[j].Mangled
	})
}
