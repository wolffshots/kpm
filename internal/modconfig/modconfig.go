// Package modconfig reads and surgically edits a package's declared config
// files (CONFIG.md §3.1). It is pure over bytes — no filesystem access — so the
// cmd layer owns reading/writing and the engines are trivially table-tested.
//
// Non-negotiable: edits are round-trip preserving. Set locates the ONE target
// line and rewrites only it; comments, ordering, blank lines, unknown keys,
// quote style, and the file's existing line-ending flavor (LF vs CRLF, and the
// presence/absence of a trailing newline) are preserved byte-for-byte
// everywhere else. The engines never parse-and-reserialize.
package modconfig

import (
	"bytes"
	"fmt"
	"strings"

	"kpm/internal/config"
)

// MaxSize is the read/write size cap (256 KiB). A config file larger than this
// is refused before any content is returned — these are short hand-edited files,
// not data blobs (CONFIG.md §3.1).
const MaxSize = 256 * 1024

// Entry is one editable unit: a key (ini) or a physical line (text).
type Entry struct {
	Section   string // ini section, "" for global keys or text lines
	Key       string // ini key, "" for text lines
	Line      int    // 1-based physical line number
	Value     string // current value (ini) or full line content (text)
	Sensitive bool   // masked in human output/logs, never in --json (CONFIG.md §2)
}

// Guard enforces the size cap and NUL-byte (binary) refusal on raw before any
// engine touches it. The cmd layer also applies it right after reading.
func Guard(raw []byte) error {
	if len(raw) > MaxSize {
		return fmt.Errorf("config file is %d bytes, over the %d-byte limit", len(raw), MaxSize)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return fmt.Errorf("config file contains NUL bytes (binary), refusing to edit")
	}
	return nil
}

// guardValue rejects a replacement value that would smuggle structure into the
// file. A single Set/Append targets ONE ini value or ONE physical line, so an
// embedded newline (which joinLines would write verbatim, splitting one value or
// line into several — e.g. "false\n[Evil]\nHacked = 1") and a NUL byte (which
// would make the file binary and unreadable on the next Guard) are always
// errors, never silently written.
func guardValue(value string) error {
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("value contains a line break; a config value/line must be a single line")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("value contains a NUL byte, refusing to write")
	}
	return nil
}

// SeedContent normalizes a declaration's template into the exact bytes to write
// when creating the file from its example (CONFIG.md §3.x — `kpm config init`).
// It is pure over the template: strip every \r (so a registry authored on Windows
// still lands as LF on the device), then ensure the content ends in exactly one
// trailing \n (registry authors need not be precise about the closing newline),
// and finally run the read/write Guard so an oversized or binary template is
// refused before it can be written. An empty template yields a lone "\n" — but
// `config init` refuses a declaration with no template before ever calling this.
func SeedContent(decl config.ModConfig) ([]byte, error) {
	s := strings.ReplaceAll(decl.Template, "\r", "")
	s = strings.TrimRight(s, "\n") + "\n"
	out := []byte(s)
	if err := Guard(out); err != nil {
		return nil, err
	}
	return out, nil
}

// List returns the editable entries of raw for the declared format. A nil/empty
// raw yields no entries (an empty or not-yet-created file). Errors only on the
// size/binary guards or an unsupported format.
func List(raw []byte, decl config.ModConfig) ([]Entry, error) {
	if err := Guard(raw); err != nil {
		return nil, err
	}
	switch decl.Format {
	case config.FormatINI:
		return listINI(raw, decl), nil
	case config.FormatText:
		return listText(raw), nil
	default:
		return nil, fmt.Errorf("unsupported config format %q", decl.Format)
	}
}

// ---- physical line model -----------------------------------------------

// physLine is one physical line: its content (no terminator) and the exact
// terminator that followed it ("\n", "\r\n", or "" for a final unterminated
// line). Joining every text+eol reproduces the original bytes exactly.
type physLine struct {
	text string
	eol  string
}

// splitLines splits raw into physical lines preserving each terminator. An empty
// input yields no lines. A trailing newline does NOT produce a spurious empty
// final line (so "a\n" is one line, "a\nb" is two).
func splitLines(raw []byte) []physLine {
	if len(raw) == 0 {
		return nil
	}
	s := string(raw)
	var out []physLine
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start && s[i-1] == '\r' {
				out = append(out, physLine{text: s[start : i-1], eol: "\r\n"})
			} else {
				out = append(out, physLine{text: s[start:i], eol: "\n"})
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, physLine{text: s[start:], eol: ""})
	}
	return out
}

// joinLines reassembles physical lines into bytes.
func joinLines(lines []physLine) []byte {
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString(ln.text)
		b.WriteString(ln.eol)
	}
	return []byte(b.String())
}

// dominantEol returns the terminator flavor to use for newly-inserted lines: the
// first terminator seen in raw, defaulting to "\n" for an empty/terminator-less
// file (CONFIG.md §3.1 — preserve the file's existing flavor).
func dominantEol(lines []physLine) string {
	for _, ln := range lines {
		if ln.eol != "" {
			return ln.eol
		}
	}
	return "\n"
}

// ---- text engine -------------------------------------------------------

func listText(raw []byte) []Entry {
	lines := splitLines(raw)
	out := make([]Entry, 0, len(lines))
	for i, ln := range lines {
		out = append(out, Entry{Line: i + 1, Value: ln.text})
	}
	return out
}

// SetLine replaces the content of 1-based physical line n, keeping its
// terminator. Out-of-range n is an error.
func SetLine(raw []byte, n int, value string) ([]byte, error) {
	if err := Guard(raw); err != nil {
		return nil, err
	}
	if err := guardValue(value); err != nil {
		return nil, err
	}
	lines := splitLines(raw)
	if n < 1 || n > len(lines) {
		return nil, fmt.Errorf("line %d out of range (file has %d line(s))", n, len(lines))
	}
	lines[n-1].text = value
	return joinLines(lines), nil
}

// AppendLine adds value as a new final physical line, preserving the file's eol
// flavor and its trailing-newline state: a file that ended with a newline keeps
// one; a file without a trailing newline stays without one.
func AppendLine(raw []byte, value string) ([]byte, error) {
	if err := Guard(raw); err != nil {
		return nil, err
	}
	if err := guardValue(value); err != nil {
		return nil, err
	}
	lines := splitLines(raw)
	eol := dominantEol(lines)
	if len(lines) == 0 {
		return []byte(value + eol), nil
	}
	if lines[len(lines)-1].eol == "" {
		// No trailing newline: terminate the current last line, add value
		// unterminated so the no-trailing-newline flavor is preserved.
		lines[len(lines)-1].eol = eol
		lines = append(lines, physLine{text: value, eol: ""})
	} else {
		// Trailing newline present: append a terminated line so it is kept.
		lines = append(lines, physLine{text: value, eol: eol})
	}
	return joinLines(lines), nil
}

// DeleteLine removes 1-based physical line n (content and its terminator).
// Out-of-range n is an error.
func DeleteLine(raw []byte, n int) ([]byte, error) {
	if err := Guard(raw); err != nil {
		return nil, err
	}
	lines := splitLines(raw)
	if n < 1 || n > len(lines) {
		return nil, fmt.Errorf("line %d out of range (file has %d line(s))", n, len(lines))
	}
	lines = append(lines[:n-1], lines[n:]...)
	return joinLines(lines), nil
}

// ---- ini engine --------------------------------------------------------

// isSectionHeader reports whether a trimmed line is a [section] header.
func isSectionHeader(trimmed string) bool {
	return len(trimmed) >= 2 && trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']'
}

// sectionName returns the name inside a [section] header (trimmed).
func sectionName(trimmed string) string {
	return strings.TrimSpace(trimmed[1 : len(trimmed)-1])
}

// isComment reports whether a trimmed line is an ini comment (; or #).
func isComment(trimmed string) bool {
	return strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#")
}

func listINI(raw []byte, decl config.ModConfig) []Entry {
	lines := splitLines(raw)
	var out []Entry
	section := ""
	for i, ln := range lines {
		t := strings.TrimSpace(ln.text)
		switch {
		case t == "" || isComment(t):
			continue
		case isSectionHeader(t):
			section = sectionName(t)
		case strings.ContainsRune(t, '='):
			eq := strings.IndexByte(t, '=')
			key := strings.TrimSpace(t[:eq])
			if key == "" {
				continue
			}
			val := strings.TrimSpace(t[eq+1:])
			out = append(out, Entry{
				Section:   section,
				Key:       key,
				Line:      i + 1,
				Value:     val,
				Sensitive: decl.IsSensitive(key),
			})
		}
	}
	return out
}

// SetKey sets key=value within section (section "" is the global area before the
// first header). An existing key has ONLY its value token rewritten (surrounding
// whitespace on the line kept); a key missing from an existing section is
// appended at the end of that section's content; a missing section is created at
// EOF with the key under it.
func SetKey(raw []byte, section, key, value string) ([]byte, error) {
	if err := Guard(raw); err != nil {
		return nil, err
	}
	if err := guardValue(value); err != nil {
		return nil, err
	}
	lines := splitLines(raw)
	eol := dominantEol(lines)

	cur := ""
	inTarget := section == "" // the global area starts "in" the "" section
	sawSection := section == ""
	lastContentIdx := -1 // last non-blank content line index within the target section

	for i := range lines {
		t := strings.TrimSpace(lines[i].text)
		if isSectionHeader(t) {
			cur = sectionName(t)
			inTarget = cur == section
			if inTarget {
				sawSection = true
			}
			continue
		}
		if !inTarget {
			continue
		}
		if t == "" {
			continue
		}
		lastContentIdx = i
		if isComment(t) {
			continue
		}
		if eq := strings.IndexByte(t, '='); eq >= 0 && strings.TrimSpace(t[:eq]) == key {
			lines[i].text = replaceINIValue(lines[i].text, value)
			return joinLines(lines), nil
		}
	}

	newLine := physLine{text: key + " = " + value, eol: eol}

	if sawSection {
		// Insert after the section's last content line; if the section is empty,
		// after its header (or at the top for the global area).
		insertAt := lastContentIdx + 1
		if lastContentIdx < 0 {
			insertAt = sectionInsertStart(lines, section)
		}
		if insertAt == len(lines) {
			ensureFinalEol(lines, eol)
			lines = append(lines, newLine)
		} else {
			lines = append(lines[:insertAt], append([]physLine{newLine}, lines[insertAt:]...)...)
		}
		return joinLines(lines), nil
	}

	// Section absent: create the header at EOF, then the key under it.
	ensureFinalEol(lines, eol)
	lines = append(lines, physLine{text: "[" + section + "]", eol: eol}, newLine)
	return joinLines(lines), nil
}

// sectionInsertStart returns the index just after the header of the named
// section (0 for the global "" section, which has no header).
func sectionInsertStart(lines []physLine, section string) int {
	if section == "" {
		return 0
	}
	for i := range lines {
		t := strings.TrimSpace(lines[i].text)
		if isSectionHeader(t) && sectionName(t) == section {
			return i + 1
		}
	}
	return len(lines)
}

// ensureFinalEol gives the last line a terminator when it lacks one, so a newly
// appended line starts on its own line without disturbing existing content.
func ensureFinalEol(lines []physLine, eol string) {
	if len(lines) > 0 && lines[len(lines)-1].eol == "" {
		lines[len(lines)-1].eol = eol
	}
}

// replaceINIValue rewrites only the value token of an ini "key = value" line,
// preserving indentation, the key, the "=" separator, and the exact whitespace
// that surrounded the old value (so quote style and alignment survive).
func replaceINIValue(content, value string) string {
	eq := strings.IndexByte(content, '=')
	if eq < 0 {
		return content // not a key line; leave untouched (unreachable via SetKey)
	}
	rem := content[eq+1:]
	lead := rem[:len(rem)-len(strings.TrimLeft(rem, " \t"))]
	trail := rem[len(strings.TrimRight(rem, " \t")):]
	return content[:eq+1] + lead + value + trail
}
