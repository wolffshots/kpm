package main

// `kpm exclude-sync`: the ExcludeSyncFolders key of Kobo eReader.conf
// (kpm-archive/docs/EXCLUDE-SYNC.md). The key hides folders from the library
// scan. It replaces the dotfile hack (libdfh.so), which has no build for
// firmware 6.x.
//
// kpm writes this one key. Firmware 4.46 and 6.0 have a getter for it and no
// setter, so Nickel never writes it back (EXCLUDE-SYNC.md section 2). Nickel
// caches the value, so a change applies after a restart.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/modconfig"
)

// excludeSyncSection and excludeSyncKey locate the setting. Nickel writes the
// section with this case.
const (
	excludeSyncSection = "FeatureSettings"
	excludeSyncKey     = "ExcludeSyncFolders"
)

// presetDotFolders hides each folder whose name starts with a dot, except a
// top-level folder whose name starts with ".kobo" or ".adobe" (purchased books
// are in .kobo/kepub). It has the scope of the KOReader wiki value
// (\.(?!kobo|adobe).+|([^.][^/]*/)+\..+), but without lookahead, so Go's
// regexp evaluates it exactly as Nickel's PCRE does. Nickel matches the value
// against the folder path after "/mnt/onboard/", anchored at both ends.
//
// The first alternative is a top-level dot folder: the name after the dot is
// a strict prefix of "kobo" or "adobe", or it differs from both prefixes at
// some letter. The second is a dot folder below a visible top-level folder.
// The value has no ",", ";" or quote, so QSettings needs no quotes for it.
const presetDotFolders = `(\.(k|ko|kob|a|ad|ado|adob|[^ka/][^/]*|k[^o/][^/]*|ko[^b/][^/]*|kob[^o/][^/]*|a[^d/][^/]*|ad[^o/][^/]*|ado[^b/][^/]*|adob[^e/][^/]*)|[^./][^/]*/([^/]*/)*\.[^/]+)`

// excludeSyncPresets maps a preset name to its regular expression (unescaped).
var excludeSyncPresets = map[string]string{
	"dot-folders": presetDotFolders,
}

// nickelMountPrefix is the path Nickel puts in front of the value.
const nickelMountPrefix = "/mnt/onboard/"

// The sources of the current value.
const (
	excludeSourceNone      = "none"       // no key
	excludeSourceKpm       = "kpm"        // a kpm preset
	excludeSourceKpmCustom = "kpm+custom" // a kpm preset merged with the earlier value
	excludeSourceCustom    = "custom"     // a value that kpm did not write
)

// excludeSyncRecord is .adds/kpm/exclude-sync.json. It holds what `off` needs:
// the value from before the first kpm write, and the value that kpm wrote.
// Both are raw file text (QSettings escapes included).
type excludeSyncRecord struct {
	Previous *string `json:"previous"` // nil: the key was absent
	Written  string  `json:"written"`
	Preset   string  `json:"preset"`
}

func excludeSyncRecordFile(p device.Paths) string {
	return filepath.Join(p.AddsKpm(), "exclude-sync.json")
}

func excludeSyncBackupDir(p device.Paths) string {
	return filepath.Join(p.AddsKpm(), "backup")
}

func loadExcludeSyncRecord(p device.Paths) (*excludeSyncRecord, error) {
	raw, err := os.ReadFile(excludeSyncRecordFile(p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r excludeSyncRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", excludeSyncRecordFile(p), err)
	}
	return &r, nil
}

// qsettingsEscape writes a string the way QSettings reads it back: each
// backslash doubles. The caller makes sure the value needs no quotes.
func qsettingsEscape(s string) string { return strings.ReplaceAll(s, `\`, `\\`) }

// qsettingsUnescape returns the string that QSettings reads from raw file
// text. ok is false for text that this small reader does not cover: an escape
// other than \\ and \", or a "," outside quotes (QSettings then reads a list,
// and Nickel's toString() gives an empty string).
func qsettingsUnescape(raw string) (string, bool) {
	var b strings.Builder
	quoted := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"':
			quoted = !quoted
		case c == '\\':
			if i+1 >= len(raw) || (raw[i+1] != '\\' && raw[i+1] != '"') {
				return "", false
			}
			i++
			b.WriteByte(raw[i])
		case c == ',' && !quoted:
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	if quoted {
		return "", false
	}
	return b.String(), true
}

// nickelMatcher compiles a value the way Nickel applies it. It fails for a
// value that Go's regexp cannot compile, for example one with lookahead.
func nickelMatcher(value string) (*regexp.Regexp, error) {
	return regexp.Compile("^" + regexp.QuoteMeta(nickelMountPrefix) + "(?:" + value + ")$")
}

// excludeSyncConf is the state of the key in the file.
type excludeSyncConf struct {
	exists  bool   // the conf file exists
	raw     []byte // the file bytes
	present bool   // the key is in [FeatureSettings]
	line    int    // its 1-based line
	value   string // its raw text
}

func readExcludeSyncConf(p device.Paths) (excludeSyncConf, error) {
	raw, err := os.ReadFile(p.NickelConf())
	if errors.Is(err, fs.ErrNotExist) {
		return excludeSyncConf{}, nil
	}
	if err != nil {
		return excludeSyncConf{}, err
	}
	c := excludeSyncConf{exists: true, raw: raw}
	decl := config.ModConfig{Name: "Kobo eReader.conf", Path: "/", Format: config.FormatINI}
	entries, err := modconfig.List(raw, decl)
	if err != nil {
		return c, err
	}
	for _, e := range entries {
		if strings.EqualFold(e.Section, excludeSyncSection) && strings.EqualFold(e.Key, excludeSyncKey) {
			c.present, c.line, c.value = true, e.Line, e.Value
			break
		}
	}
	return c, nil
}

// excludeSyncSource classifies the current value against the record.
func excludeSyncSource(c excludeSyncConf, rec *excludeSyncRecord) string {
	if !c.present || c.value == "" {
		return excludeSourceNone
	}
	if rec != nil && c.value == rec.Written {
		if rec.Previous != nil && *rec.Previous != "" {
			return excludeSourceKpmCustom
		}
		return excludeSourceKpm
	}
	for _, v := range excludeSyncPresets {
		if c.value == qsettingsEscape(v) {
			return excludeSourceKpm
		}
	}
	return excludeSourceCustom
}

// scanDotFolders returns the outermost dot folders below root as slash paths
// relative to root. It does not go into a dot folder: Nickel does not scan an
// excluded folder, and kpm keeps .kobo (large) out of the walk.
func scanDotFolders(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil // an unreadable folder: skip it
		}
		if !d.IsDir() || path == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			rel, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(rel))
			return filepath.SkipDir
		}
		return nil
	})
	return out, err
}

type jsonExcludeFolder struct {
	Path   string `json:"path"`
	Hidden *bool  `json:"hidden"` // null: kpm cannot test the value
}

type jsonExcludeSync struct {
	Value    *string             `json:"value"` // the expression Nickel reads; null when there is none
	Source   string              `json:"source"`
	Presets  []string            `json:"presets"`
	Testable bool                `json:"testable"`
	Folders  []jsonExcludeFolder `json:"folders"`
}

// excludeSyncReport builds the folder list for a raw value ("" for none).
func excludeSyncReport(p device.Paths, raw, source string) jsonExcludeSync {
	rep := jsonExcludeSync{Source: source, Presets: []string{"dot-folders"}, Folders: []jsonExcludeFolder{}}
	var re *regexp.Regexp
	if raw == "" {
		rep.Testable = true
	} else if v, ok := qsettingsUnescape(raw); ok {
		rep.Value = &v
		if m, err := nickelMatcher(v); err == nil {
			re, rep.Testable = m, true
		}
	}
	dirs, _ := scanDotFolders(p.Root)
	for _, d := range dirs {
		f := jsonExcludeFolder{Path: d}
		if rep.Testable {
			hidden := re != nil && re.MatchString(nickelMountPrefix+d)
			f.Hidden = &hidden
		}
		rep.Folders = append(rep.Folders, f)
	}
	return rep
}

func printExcludeSync(rep jsonExcludeSync) {
	switch {
	case rep.Source == excludeSourceNone:
		fmt.Println("ExcludeSyncFolders: not set")
	case rep.Value == nil:
		fmt.Println("ExcludeSyncFolders: a value kpm cannot read")
	default:
		fmt.Println("ExcludeSyncFolders:", *rep.Value)
	}
	fmt.Println("source:", rep.Source)
	if !rep.Testable {
		fmt.Println("kpm cannot test this value (for example lookahead), so the folder list has no result.")
	}
	if len(rep.Folders) == 0 {
		fmt.Println("no dot folders on the user partition")
		return
	}
	fmt.Println("dot folders:")
	for _, f := range rep.Folders {
		state := "?     "
		if f.Hidden != nil {
			state = map[bool]string{true: "hidden", false: "shown "}[*f.Hidden]
		}
		fmt.Printf("  %s  %s\n", state, f.Path)
	}
}

func (a *App) cmdExcludeSync(args []string) int {
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	dryRun := false
	for _, f := range flags {
		if f == "--dry-run" || f == "-dry-run" {
			dryRun = true
			continue
		}
		fmt.Fprintf(os.Stderr, "kpm exclude-sync: unknown flag %s\n", f)
		return exitError
	}
	fail := func(msg string) int {
		if jsonMode {
			emitMutation(nil, []jsonFailure{{ID: "exclude-sync", Error: msg}}, false, false, msg)
		}
		fmt.Fprintln(os.Stderr, "kpm exclude-sync:", msg)
		return exitError
	}
	usageErr := func() int {
		fmt.Fprintln(os.Stderr, "usage: kpm exclude-sync [--json]")
		fmt.Fprintln(os.Stderr, "       kpm exclude-sync set dot-folders [--dry-run] [--json]")
		fmt.Fprintln(os.Stderr, "       kpm exclude-sync off [--dry-run] [--json]")
		return exitError
	}

	conf, err := readExcludeSyncConf(a.paths)
	if err != nil {
		return fail(err.Error())
	}
	rec, err := loadExcludeSyncRecord(a.paths)
	if err != nil {
		return fail(err.Error())
	}
	source := excludeSyncSource(conf, rec)

	if len(pos) == 0 {
		if dryRun {
			return usageErr()
		}
		rep := excludeSyncReport(a.paths, conf.value, source)
		printExcludeSync(rep)
		if jsonMode {
			jsonLine(rep)
		}
		return exitOK
	}

	var next excludeSyncRecord // the record after the change
	var newValue *string       // nil: delete the key
	var newSource string
	switch {
	case pos[0] == "set" && len(pos) == 2:
		preset, ok := excludeSyncPresets[pos[1]]
		if !ok {
			return fail(fmt.Sprintf("unknown preset %q (presets: dot-folders)", pos[1]))
		}
		next.Preset = pos[1]
		// The value from before kpm: keep the first one kpm saw.
		switch source {
		case excludeSourceKpm, excludeSourceKpmCustom:
			if rec != nil {
				next.Previous = rec.Previous
			}
		case excludeSourceCustom:
			v := conf.value
			next.Previous = &v
		}
		v := qsettingsEscape(preset)
		newSource = excludeSourceKpm
		if next.Previous != nil && *next.Previous != "" {
			prev, ok := qsettingsUnescape(*next.Previous)
			if !ok {
				return fail("kpm cannot read the current value, so it cannot merge the preset with it. Edit Kobo eReader.conf by hand.")
			}
			v = qsettingsEscape("(" + prev + "|" + preset + ")")
			newSource = excludeSourceKpmCustom
		}
		newValue = &v
		next.Written = v
	case pos[0] == "off" && len(pos) == 1:
		switch source {
		case excludeSourceNone:
			fmt.Println("kpm: ExcludeSyncFolders is not set, nothing to do")
			if jsonMode {
				emitMutation(nil, nil, false, false, "")
			}
			return exitOK
		case excludeSourceCustom:
			return fail("the value is not from kpm, so kpm leaves it. Edit Kobo eReader.conf by hand.")
		}
		newSource = excludeSourceNone
		if rec != nil && rec.Previous != nil && *rec.Previous != "" {
			newValue = rec.Previous
			newSource = excludeSourceCustom
		}
	default:
		return usageErr()
	}

	if !conf.exists {
		return fail(a.paths.NickelConf() + " does not exist; start Nickel once to create it")
	}
	if newValue != nil && conf.present && conf.value == *newValue {
		fmt.Println("kpm: ExcludeSyncFolders already has this value, nothing to do")
		if jsonMode {
			emitMutation(nil, nil, false, false, "")
		}
		return exitOK
	}

	var newRaw []byte
	if newValue == nil {
		newRaw, err = modconfig.DeleteLine(conf.raw, conf.line)
	} else if conf.present {
		newRaw, err = modconfig.SetLine(conf.raw, conf.line, excludeSyncKey+"="+*newValue)
	} else {
		newRaw, err = modconfig.SetKey(conf.raw, excludeSyncSection, excludeSyncKey, *newValue)
	}
	if err == nil {
		err = modconfig.Guard(newRaw)
	}
	if err != nil {
		return fail(err.Error())
	}

	if dryRun {
		raw := ""
		if newValue != nil {
			raw = *newValue
		}
		fmt.Println("kpm: dry run, nothing written. The new state:")
		rep := excludeSyncReport(a.paths, raw, newSource)
		printExcludeSync(rep)
		if jsonMode {
			jsonLine(rep)
		}
		return exitOK
	}

	backup, err := backupNickelConf(a.paths, conf.raw)
	if err != nil {
		return fail("backup: " + err.Error())
	}
	if err := device.WriteFileAtomic(a.paths.NickelConf(), newRaw); err != nil {
		return fail(fmt.Sprintf("%v (the old file is in %s)", err, backup))
	}
	// Read the file back: the write must give exactly the new value.
	check, err := readExcludeSyncConf(a.paths)
	switch {
	case err != nil:
		return fail(fmt.Sprintf("read back: %v (the old file is in %s)", err, backup))
	case newValue == nil && check.present, newValue != nil && (!check.present || check.value != *newValue):
		return fail(fmt.Sprintf("the value did not read back as written (the old file is in %s)", backup))
	}

	if newValue == nil || newSource == excludeSourceCustom {
		err = os.Remove(excludeSyncRecordFile(a.paths))
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	} else {
		var data []byte
		data, err = json.MarshalIndent(next, "", "  ")
		if err == nil {
			err = device.WriteFileAtomic(excludeSyncRecordFile(a.paths), append(data, '\n'))
		}
	}
	if err != nil {
		// The conf is right. Only `off` loses the earlier value; the backup has it.
		fmt.Fprintf(os.Stderr, "kpm exclude-sync: record: %v (the old file is in %s)\n", err, backup)
	}

	detail := "off"
	if pos[0] == "set" {
		detail = "set " + next.Preset
	}
	a.paths.Log("EXCLUDE", fmt.Sprintf("ExcludeSyncFolders %s (%s), backup %s", detail, newSource, filepath.Base(backup)))
	fmt.Printf("kpm: ExcludeSyncFolders %s. Backup: %s\n", detail, backup)
	fmt.Println("Restart the device to apply. Nickel reads the value at start.")
	if jsonMode {
		emitMutation([]string{"exclude-sync"}, nil, false, true, "")
	}
	return exitOK
}

// backupNickelConf copies the old conf into .adds/kpm/backup and returns the
// path of the copy.
func backupNickelConf(p device.Paths, raw []byte) (string, error) {
	name := "Kobo eReader.conf." + time.Now().UTC().Format("20060102T150405Z")
	path := filepath.Join(excludeSyncBackupDir(p), name)
	if err := device.WriteFileAtomic(path, raw); err != nil {
		return "", err
	}
	return path, nil
}
