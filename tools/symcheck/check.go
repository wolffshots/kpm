package main

import (
	"debug/elf"
	"fmt"
	"io"
)

// Result pairs a Symbol with whether it was found in a libnickel .dynsym table.
type Result struct {
	Symbol Symbol
	Found  bool
}

// DynSymSet reads the dynamic symbol table (.dynsym) of an ELF shared object and
// returns the set of exported/defined symbol names. dlsym on device resolves
// against exactly this table, so a name absent here is a load-time miss.
func DynSymSet(r io.ReaderAt) (map[string]bool, error) {
	f, err := elf.NewFile(r)
	if err != nil {
		return nil, fmt.Errorf("not a valid ELF: %w", err)
	}
	defer f.Close()

	syms, err := f.DynamicSymbols()
	if err != nil {
		return nil, fmt.Errorf("reading .dynsym: %w", err)
	}
	set := make(map[string]bool, len(syms))
	for _, s := range syms {
		// A dlsym-resolvable symbol must be defined (not undefined/imported).
		// SHN_UNDEF (section index 0) means the symbol is imported, not exported.
		if s.Section == elf.SHN_UNDEF {
			continue
		}
		set[s.Name] = true
	}
	if len(set) == 0 {
		return nil, fmt.Errorf(".dynsym contained no defined symbols (wrong/corrupt libnickel?)")
	}
	return set, nil
}

// CheckSymbols marks each parsed symbol found or missing against the dynsym set.
func CheckSymbols(dynsym map[string]bool, syms []Symbol) []Result {
	out := make([]Result, len(syms))
	for i, s := range syms {
		out[i] = Result{Symbol: s, Found: dynsym[s.Mangled]}
	}
	return out
}

// MissingRequired reports whether any required (non-optional) symbol is missing.
func MissingRequired(results []Result) bool {
	for _, r := range results {
		if !r.Found && !r.Symbol.Optional {
			return true
		}
	}
	return false
}
