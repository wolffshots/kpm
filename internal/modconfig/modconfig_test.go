package modconfig

import (
	"bytes"
	"strings"
	"testing"

	"kpm/internal/config"
)

// Realistic fixtures used across the round-trip tests (CONFIG.md §5.1).

// A NickelClock settings.ini: three sections, full-line comments (Qt QSettings
// has no inline comments, so a value is the whole text after '='), CRLF-free, a
// trailing newline, and a value containing '{' and '%'.
const clockINI = `; NickelClock settings
[General]
Margin = 10

[Clock]
; show the clock in the reading footer
Enabled = true
Placement = Footer
Position = Right

[Battery]
BatteryType = Percentage
LevelTemplate = {{level}}%
`

// A NickelNote content.template: Qt rich-text, no trailing newline.
const noteTemplate = `<p><span style="font-size:24pt;">Good night</span></p>
<p><span style="font-size:12pt;">Sleep well, reader.</span></p>`

// A NickelNote style.template: QSS body, CRLF line endings, trailing newline.
const styleTemplateCRLF = "QWidget {\r\n  background-color: #ffffff;\r\n  color: #000000;\r\n}\r\n"

func iniDecl() config.ModConfig {
	return config.ModConfig{Name: "Settings", Path: "/mnt/onboard/.adds/nickelclock/settings.ini", Format: config.FormatINI}
}
func textDecl() config.ModConfig {
	return config.ModConfig{Name: "Note", Path: "/mnt/onboard/.adds/nickelnote/content.template", Format: config.FormatText}
}

// ---- physical-line model ------------------------------------------------

func TestSplitJoinRoundTrip(t *testing.T) {
	cases := []string{
		"", "a", "a\n", "a\nb", "a\nb\n",
		"a\r\nb\r\n", "a\r\nb", "\n\n\n", "a\n\nb\n",
		clockINI, noteTemplate, styleTemplateCRLF,
	}
	for _, c := range cases {
		got := joinLines(splitLines([]byte(c)))
		if !bytes.Equal(got, []byte(c)) {
			t.Errorf("round trip lost bytes\n in: %q\nout: %q", c, string(got))
		}
	}
}

// ---- guards -------------------------------------------------------------

func TestGuardSizeCap(t *testing.T) {
	big := bytes.Repeat([]byte("a"), MaxSize+1)
	if err := Guard(big); err == nil {
		t.Error("oversized file must be refused")
	}
	if err := Guard(bytes.Repeat([]byte("a"), MaxSize)); err != nil {
		t.Errorf("exactly-at-cap file must be allowed: %v", err)
	}
}

func TestGuardBinaryRefusal(t *testing.T) {
	if err := Guard([]byte("ok\x00nope")); err == nil {
		t.Error("NUL-bearing file must be refused")
	}
	if _, err := List([]byte("a\x00b"), textDecl()); err == nil {
		t.Error("List must refuse binary")
	}
}

// ---- text engine --------------------------------------------------------

func TestTextList(t *testing.T) {
	entries, err := List([]byte(noteTemplate), textDecl())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 lines, got %d", len(entries))
	}
	if entries[0].Line != 1 || entries[0].Key != "" || entries[0].Section != "" {
		t.Errorf("text entry 0 shape wrong: %+v", entries[0])
	}
	if !strings.Contains(entries[1].Value, "Sleep well") {
		t.Errorf("text entry 1 value = %q", entries[1].Value)
	}
}

func TestSetLinePreservesRest(t *testing.T) {
	out, err := SetLine([]byte(noteTemplate), 1, `<p>New heading</p>`)
	if err != nil {
		t.Fatal(err)
	}
	want := `<p>New heading</p>
<p><span style="font-size:12pt;">Sleep well, reader.</span></p>`
	if string(out) != want {
		t.Errorf("SetLine\n got: %q\nwant: %q", string(out), want)
	}
}

func TestSetLineCRLFPreserved(t *testing.T) {
	out, err := SetLine([]byte(styleTemplateCRLF), 3, "  color: #111111;")
	if err != nil {
		t.Fatal(err)
	}
	want := "QWidget {\r\n  background-color: #ffffff;\r\n  color: #111111;\r\n}\r\n"
	if string(out) != want {
		t.Errorf("CRLF must be preserved\n got: %q\nwant: %q", string(out), want)
	}
}

func TestSetLineOutOfRange(t *testing.T) {
	if _, err := SetLine([]byte(noteTemplate), 9, "x"); err == nil {
		t.Error("out-of-range line must error")
	}
	if _, err := SetLine([]byte(noteTemplate), 0, "x"); err == nil {
		t.Error("line 0 must error")
	}
}

func TestAppendLine(t *testing.T) {
	cases := []struct {
		name, in, val, want string
	}{
		{"empty", "", "first", "first\n"},
		{"trailing-newline", "a\nb\n", "c", "a\nb\nc\n"},
		{"no-trailing-newline", "a\nb", "c", "a\nb\nc"},
		{"crlf-trailing", "a\r\nb\r\n", "c", "a\r\nb\r\nc\r\n"},
	}
	for _, tc := range cases {
		out, err := AppendLine([]byte(tc.in), tc.val)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(out) != tc.want {
			t.Errorf("%s: AppendLine\n got: %q\nwant: %q", tc.name, string(out), tc.want)
		}
	}
}

func TestDeleteLine(t *testing.T) {
	out, err := DeleteLine([]byte("a\nb\nc\n"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a\nc\n" {
		t.Errorf("DeleteLine got %q, want %q", string(out), "a\nc\n")
	}
	if _, err := DeleteLine([]byte("a\n"), 5); err == nil {
		t.Error("out-of-range delete must error")
	}
}

// ---- ini engine ---------------------------------------------------------

func TestINIList(t *testing.T) {
	entries, err := List([]byte(clockINI), iniDecl())
	if err != nil {
		t.Fatal(err)
	}
	// Margin, Enabled, Placement, Position, BatteryType, LevelTemplate = 6.
	if len(entries) != 6 {
		t.Fatalf("want 6 entries, got %d: %+v", len(entries), entries)
	}
	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.Section+"/"+e.Key] = e
	}
	if e := byKey["Clock/Enabled"]; e.Value != "true" {
		t.Errorf("Clock/Enabled value = %q, want true", e.Value)
	}
	if e := byKey["Battery/LevelTemplate"]; e.Value != "{{level}}%" {
		t.Errorf("LevelTemplate value = %q, want {{level}}%%", e.Value)
	}
	if e := byKey["General/Margin"]; e.Line != 3 {
		t.Errorf("Margin line = %d, want 3", e.Line)
	}
}

func TestINISetExistingKeyOnlyValueChanges(t *testing.T) {
	out, err := SetKey([]byte(clockINI), "Clock", "Enabled", "false")
	if err != nil {
		t.Fatal(err)
	}
	// Only "Enabled = true" -> "Enabled = false"; the preceding comment and the
	// spacing around '=' survive byte-for-byte.
	want := strings.Replace(clockINI, "Enabled = true", "Enabled = false", 1)
	if string(out) != want {
		t.Errorf("SetKey existing\n got: %q\nwant: %q", string(out), want)
	}
	assertOnlyLineDiffers(t, clockINI, string(out))
}

func TestINISetNewKeyInExistingSection(t *testing.T) {
	out, err := SetKey([]byte(clockINI), "Clock", "TwentyFourHour", "true")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "TwentyFourHour = true") {
		t.Fatalf("new key not inserted:\n%s", s)
	}
	// Inserted within [Clock], before the [Battery] header (not at EOF).
	clockIdx := strings.Index(s, "[Clock]")
	batteryIdx := strings.Index(s, "[Battery]")
	keyIdx := strings.Index(s, "TwentyFourHour")
	if !(clockIdx < keyIdx && keyIdx < batteryIdx) {
		t.Errorf("new key not placed inside [Clock] section:\n%s", s)
	}
}

func TestINISetNewSectionAtEOF(t *testing.T) {
	out, err := SetKey([]byte(clockINI), "Advanced", "Debug", "1")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, clockINI) {
		t.Errorf("original content must be preserved as a prefix:\n%s", s)
	}
	if !strings.Contains(s, "[Advanced]") || !strings.Contains(s, "Debug = 1") {
		t.Errorf("new section+key not appended:\n%s", s)
	}
}

func TestINISetGlobalKey(t *testing.T) {
	raw := "; header\nGlobalOne = a\n\n[S]\nk = v\n"
	// Existing global key.
	out, err := SetKey([]byte(raw), "", "GlobalOne", "b")
	if err != nil {
		t.Fatal(err)
	}
	if want := "; header\nGlobalOne = b\n\n[S]\nk = v\n"; string(out) != want {
		t.Errorf("global set\n got: %q\nwant: %q", string(out), want)
	}
	// New global key inserted in the global area, before [S].
	out2, _ := SetKey([]byte(raw), "", "GlobalTwo", "c")
	s := string(out2)
	if idx, sIdx := strings.Index(s, "GlobalTwo"), strings.Index(s, "[S]"); idx < 0 || idx > sIdx {
		t.Errorf("new global key must precede the first section:\n%s", s)
	}
}

func TestINIWhitespaceAndQuoteStylePreserved(t *testing.T) {
	// Odd spacing and a quoted value: only the value token is replaced.
	raw := "[S]\n  key   =   \"old val\"   \n"
	out, err := SetKey([]byte(raw), "S", "key", "\"new val\"")
	if err != nil {
		t.Fatal(err)
	}
	want := "[S]\n  key   =   \"new val\"   \n"
	if string(out) != want {
		t.Errorf("whitespace/quote preservation\n got: %q\nwant: %q", string(out), want)
	}
}

func TestINICommentsNotEntries(t *testing.T) {
	raw := "[S]\n; k = commented\n# also = comment\nk = real\n"
	entries, _ := List([]byte(raw), iniDecl())
	if len(entries) != 1 || entries[0].Key != "k" || entries[0].Value != "real" {
		t.Errorf("comments must not be parsed as entries: %+v", entries)
	}
}

// ---- value injection guard ---------------------------------------------

// A replacement value carrying an embedded newline must never smuggle extra
// structure into the file: one ini value / one text line stays one line. A NUL
// value is refused too (it would make the file binary). CONFIG.md §3.1.
func TestValueInjectionRefused(t *testing.T) {
	if _, err := SetKey([]byte("[Clock]\nEnabled = true\n"), "Clock", "Enabled", "false\n[Evil]\nHacked = 1"); err == nil {
		t.Error("SetKey must refuse a value with an embedded newline")
	}
	if _, err := SetKey([]byte("[S]\nk = v\n"), "S", "k", "a\rb"); err == nil {
		t.Error("SetKey must refuse a value with a carriage return")
	}
	if _, err := SetLine([]byte("one\ntwo\n"), 1, "a\nb"); err == nil {
		t.Error("SetLine must refuse a value with an embedded newline")
	}
	if _, err := SetLine([]byte("one\n"), 1, "a\x00b"); err == nil {
		t.Error("SetLine must refuse a value with a NUL byte")
	}
	if _, err := AppendLine([]byte("a\n"), "x\ny"); err == nil {
		t.Error("AppendLine must refuse a value with an embedded newline")
	}
	// A benign single-line value with inner spaces/equals is still fine.
	if _, err := SetKey([]byte("[S]\nk = v\n"), "S", "k", "a = b"); err != nil {
		t.Errorf("a normal value must still be accepted: %v", err)
	}
}

// ---- sensitive ----------------------------------------------------------

func TestSensitiveMarking(t *testing.T) {
	decl := config.ModConfig{
		Name: "Sync", Path: "/mnt/onboard/.adds/x/config.ini", Format: config.FormatINI,
		SensitiveKeys: []string{"authorization"},
	}
	raw := "[General]\nauthorization = secrettoken\nother = 1\n"
	entries, _ := List([]byte(raw), decl)
	var auth, other Entry
	for _, e := range entries {
		switch e.Key {
		case "authorization":
			auth = e
		case "other":
			other = e
		}
	}
	if !auth.Sensitive {
		t.Error("authorization must be marked sensitive")
	}
	if auth.Value != "secrettoken" {
		t.Errorf("sensitive value must still be reported (masking is a display concern): %q", auth.Value)
	}
	if other.Sensitive {
		t.Error("non-listed key must not be sensitive")
	}
}

// ---- create-on-missing (empty raw) --------------------------------------

func TestSetKeyOnEmptyCreatesSectionAndKey(t *testing.T) {
	out, err := SetKey(nil, "Clock", "Enabled", "true")
	if err != nil {
		t.Fatal(err)
	}
	if want := "[Clock]\nEnabled = true\n"; string(out) != want {
		t.Errorf("create-from-empty\n got: %q\nwant: %q", string(out), want)
	}
}

func TestAppendOnEmptyCreatesLine(t *testing.T) {
	out, err := AppendLine(nil, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "hello\n" {
		t.Errorf("append to empty got %q", string(out))
	}
}

// assertOnlyLineDiffers fails unless exactly one physical line differs between
// before and after — the surgical-edit invariant (CONFIG.md §3.1).
func assertOnlyLineDiffers(t *testing.T, before, after string) {
	t.Helper()
	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")
	if len(b) != len(a) {
		t.Fatalf("line count changed: %d -> %d", len(b), len(a))
	}
	diffs := 0
	for i := range b {
		if b[i] != a[i] {
			diffs++
		}
	}
	if diffs != 1 {
		t.Errorf("expected exactly 1 changed line, got %d", diffs)
	}
}

// ---- SeedContent (CONFIG.md §3.x — `kpm config init`) -------------------

// The three real NickelNote templates (PII scrubbed exactly as shipped in the
// registry), used as golden fixtures: SeedContent must reproduce them byte for
// byte with a single trailing newline.
const seedContentTemplate = `<span>Your Name</span><br />
<span style="font-size: 32px">If found, Please return at</span><br />
<span style="font-size: 32px;">+1 555 000 0000</span>
`

const seedStyleTemplate = `#infoWidget {
  color: black;
  background-color: rgba(255,255,255,170);
  min-height: 290px;
  max-height: 290px;

  margin-top: 200px;
  margin-left: -30px;
}

#infoWidget[powerOffView=true]{
  background-color: rgba(0,0,0,170);
}

QLabel{
	min-width: 400px;
	min-height: 300px;
}
`

const seedPinTemplate = `<p style="font-size: 32px;">
This tablet is protected and belongs to <br/>
<b>Your Name</b>
<br/>
<br/>
If found, please return to <br/>
US: +1 555 000 0000<br/>
CA: +1 555 000 0000<br/>
</p>
`

func TestSeedContent(t *testing.T) {
	cases := []struct {
		name     string
		template string
		want     string
	}{
		// Golden fixtures: already-normalized templates pass through unchanged.
		{"nickelnote content", seedContentTemplate, seedContentTemplate},
		{"nickelnote style (tabs preserved)", seedStyleTemplate, seedStyleTemplate},
		{"nickelnote pin", seedPinTemplate, seedPinTemplate},
		// Normalization: CRLF stripped to LF.
		{"crlf stripped", "a\r\nb\r\n", "a\nb\n"},
		// Normalization: a missing trailing newline is added.
		{"adds trailing newline", "a\nb", "a\nb\n"},
		// Normalization: multiple trailing newlines collapse to exactly one.
		{"collapses trailing newlines", "a\nb\n\n\n", "a\nb\n"},
		// A lone content line still gets its single terminator.
		{"single line", "just one line", "just one line\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SeedContent(config.ModConfig{Name: "N", Path: "/mnt/onboard/.adds/x/c.template", Format: config.FormatText, Template: tc.template})
			if err != nil {
				t.Fatalf("SeedContent error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("SeedContent\n got: %q\nwant: %q", got, tc.want)
			}
			// The seeded bytes must always end in exactly one newline.
			if !bytes.HasSuffix(got, []byte("\n")) || bytes.HasSuffix(got, []byte("\n\n")) {
				t.Errorf("seeded bytes must end in exactly one newline: %q", got)
			}
		})
	}
}

// A NUL byte in a template is refused by SeedContent's Guard (defense in depth —
// ParseManifest already drops such a def via config.Validate).
func TestSeedContentRefusesBinary(t *testing.T) {
	if _, err := SeedContent(config.ModConfig{Name: "N", Path: "/p", Format: config.FormatText, Template: "a\x00b"}); err == nil {
		t.Error("SeedContent must refuse a NUL-bearing template")
	}
}
