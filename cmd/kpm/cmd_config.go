package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/modconfig"
	"kpm/internal/uninstall"
)

// maskedValue is what a sensitive value renders as in human output and logs
// (never in --json, which the UI needs to edit) — CONFIG.md §2.
const maskedValue = "••••"

// maxShowEntries caps how many entries `config show` returns; beyond it the
// payload's truncated flag is set. Declared config files are short, so this is a
// safety valve for a pathological file, not an expected path (CONFIG.md §3.3).
const maxShowEntries = 2000

// cmdConfig dispatches the config subcommand group (CONFIG.md §3.2). list/show
// are read-only (no lock, no network, offline from packages.d + the filesystem);
// set mutates (holds the single-instance lock, writes atomically).
func (a *App) cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm config list|show|set|init")
		return exitError
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return a.cmdConfigList(rest)
	case "show":
		return a.cmdConfigShow(rest)
	case "set":
		return a.cmdConfigSet(rest)
	case "init":
		return a.cmdConfigInit(rest)
	default:
		fmt.Fprintf(os.Stderr, "kpm config: unknown subcommand %q\n", sub)
		return exitError
	}
}

// cmdConfigList lists a package's declared config files with per-file
// exists/can_create/editable state (CONFIG.md §3.3). Read-only.
func (a *App) cmdConfigList(args []string) int {
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	if len(flags) > 0 || len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: kpm config list <id> [--json]")
		return exitError
	}
	id := pos[0]
	if !config.ValidID(id) {
		if jsonMode {
			jsonError(fmt.Sprintf("invalid package id %q", id))
		}
		fmt.Fprintf(os.Stderr, "kpm config list: invalid package id %q\n", id)
		return exitError
	}
	pkg, err := a.loadPackage(id)
	if err != nil {
		if jsonMode {
			jsonError(err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm config list:", err)
		return exitError
	}

	files := make([]jsonConfigFile, 0, len(pkg.Configs))
	for _, c := range pkg.Configs {
		exists, editable := a.configState(c)
		files = append(files, jsonConfigFile{
			Name:        c.Name,
			Path:        c.Path,
			Format:      c.Format,
			Reload:      c.EffectiveReload(),
			Exists:      exists,
			CanCreate:   c.Create,
			Editable:    editable,
			HasTemplate: c.Template != "",
			Description: ptr(c.Description),
		})
	}

	if jsonMode {
		jsonLine(jsonConfigListPayload{ID: id, Configs: files})
		return exitOK
	}

	if len(files) == 0 {
		fmt.Printf("%s declares no editable config\n", id)
		return exitOK
	}
	for i, f := range files {
		state := "exists"
		if !f.Exists {
			if f.CanCreate {
				state = "not created (can create)"
			} else {
				state = "not created"
			}
		}
		fmt.Printf("%d. %s [%s, %s] — %s\n", i+1, f.Name, f.Format, f.Reload, state)
		fmt.Printf("     %s\n", f.Path)
		if f.Description != nil {
			fmt.Printf("     %s\n", *f.Description)
		}
	}
	return exitOK
}

// cmdConfigShow prints one config file's entries (CONFIG.md §3.3). Read-only.
func (a *App) cmdConfigShow(args []string) int {
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	if len(flags) > 0 || len(pos) != 2 {
		fmt.Fprintln(os.Stderr, "usage: kpm config show <id> <name-or-index> [--json]")
		return exitError
	}
	id, sel := pos[0], pos[1]
	decl, code := a.resolveConfig(id, sel, jsonMode, "show")
	if code != exitOK {
		return code
	}

	raw, exists, err := a.readConfigFile(decl)
	if err != nil {
		if jsonMode {
			jsonError(err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm config show:", err)
		return exitError
	}
	var entries []modconfig.Entry
	if exists {
		entries, err = modconfig.List(raw, decl)
		if err != nil {
			if jsonMode {
				jsonError(err.Error())
			}
			fmt.Fprintln(os.Stderr, "kpm config show:", err)
			return exitError
		}
	}
	truncated := false
	if len(entries) > maxShowEntries {
		entries = entries[:maxShowEntries]
		truncated = true
	}

	if jsonMode {
		out := make([]jsonConfigEntry, 0, len(entries))
		for _, e := range entries {
			var key *string
			if decl.Format == config.FormatINI {
				k := e.Key
				key = &k
			}
			out = append(out, jsonConfigEntry{
				Section: e.Section, Key: key, Line: e.Line, Value: e.Value, Sensitive: e.Sensitive,
			})
		}
		jsonLine(jsonConfigShowPayload{
			ID: id,
			File: jsonConfigShowFile{
				Name: decl.Name, Format: decl.Format, Reload: decl.EffectiveReload(), Exists: exists,
				HasTemplate: decl.Template != "",
			},
			Entries:   out,
			Truncated: truncated,
		})
		return exitOK
	}

	fmt.Printf("%s — %s [%s, %s]\n", id, decl.Name, decl.Format, decl.EffectiveReload())
	if !exists {
		if decl.Create {
			fmt.Println("(not created yet — an edit will create it)")
		} else {
			fmt.Println("(not created yet)")
		}
		return exitOK
	}
	if len(entries) == 0 {
		fmt.Println("(empty)")
		return exitOK
	}
	for _, e := range entries {
		val := e.Value
		if e.Sensitive {
			val = maskedValue
		}
		if decl.Format == config.FormatINI {
			label := e.Key
			if e.Section != "" {
				label = e.Section + " · " + e.Key
			}
			fmt.Printf("  %-28s %s\n", label, val)
		} else {
			fmt.Printf("  %4d  %s\n", e.Line, val)
		}
	}
	if truncated {
		fmt.Printf("  … (truncated at %d entries)\n", maxShowEntries)
	}
	return exitOK
}

// cmdConfigSet edits one entry of a config file and writes it back atomically
// (CONFIG.md §3.2/§3.3). Mutating: holds the lock, applies the sysroot write
// guard, re-validates the path policy, and refuses a symlinked parent.
func (a *App) cmdConfigSet(args []string) int {
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	key := fs.String("key", "", "ini key to set")
	section := fs.String("section", "", "ini section (default global)")
	value := fs.String("value", "", "new value / line content")
	line := fs.Int("line", 0, "text line number (1-based)")
	appendF := fs.Bool("append", false, "text: append value as a new line")
	deleteF := fs.Bool("delete", false, "text: delete the given line")
	flags, pos := splitArgs(args, map[string]bool{"key": true, "section": true, "value": true, "line": true})
	flags, jsonMode := takeJSON(flags)
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	if len(pos) != 2 {
		fmt.Fprintln(os.Stderr, "usage: kpm config set <id> <name> --key K [--section S] --value V   (ini)")
		fmt.Fprintln(os.Stderr, "       kpm config set <id> <name> --line N --value V [--append|--delete]   (text)")
		return exitError
	}
	id, sel := pos[0], pos[1]

	failJSON := func(msg string) {
		if jsonMode {
			emitMutation(nil, []jsonFailure{{ID: id, Error: msg}}, false, false, msg)
		}
	}

	// Off-device write guard, copied from cmd_uninstall.go: writes resolve
	// against KPM_SYSROOT, so a KPM_ROOT sandbox without KPM_SYSROOT would land on
	// the real rootfs. On a real device both are unset (G5).
	if os.Getenv("KPM_SYSROOT") == "" {
		if os.Getenv("KPM_ROOT") != "" {
			fmt.Fprintln(os.Stderr, "kpm config set: KPM_ROOT is set but KPM_SYSROOT is not — refusing to write against the real rootfs; set KPM_SYSROOT")
			return exitError
		}
		if runtime.GOOS != "linux" {
			fmt.Fprintln(os.Stderr, "kpm config set: set KPM_SYSROOT when running off-device")
			return exitError
		}
	}

	decl, code := a.resolveConfig(id, sel, jsonMode, "set")
	if code != exitOK {
		return code
	}

	// Re-validate the path immediately before writing (defense in depth: the local
	// packages.d snapshot is user-editable), returning the cleaned device path.
	abs, err := uninstall.ConfigPath(decl.Path)
	if err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config set:", err)
		return exitError
	}

	raw, exists, err := a.readConfigFile(decl)
	if err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config set:", err)
		return exitError
	}
	if !exists && !decl.Create {
		msg := fmt.Sprintf("%q is not created yet and its declaration does not allow creating it", decl.Name)
		failJSON(msg)
		fmt.Fprintln(os.Stderr, "kpm config set:", msg)
		return exitError
	}

	newRaw, action, err := applyEdit(raw, decl, provided, *key, *section, *value, *line, *appendF, *deleteF)
	if err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config set:", err)
		return exitError
	}

	// Enforce the size cap pre-write too, not just pre-read: a large --value could
	// otherwise balloon a small file past MaxSize, leaving a file kpm then refuses
	// to read back (CONFIG.md §3.1).
	if err := modconfig.Guard(newRaw); err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config set:", err)
		return exitError
	}

	// Never write through a symlinked parent (C7), the same guard uninstall applies.
	if bad, ok := uninstall.SymlinkedConfigParent(a.paths, abs); ok {
		msg := fmt.Sprintf("refusing to write %s: symlinked parent %s", decl.Path, bad)
		failJSON(msg)
		fmt.Fprintln(os.Stderr, "kpm config set:", msg)
		return exitError
	}
	if err := device.WriteFileAtomic(a.paths.HostPath(abs), newRaw); err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config set:", err)
		return exitError
	}

	a.paths.Log("CONFIG", fmt.Sprintf("%s  %s  %s", id, decl.Name, action))
	fmt.Printf("kpm: updated %s (%s) — %s\n", decl.Name, id, action)
	if decl.RebootRequired() {
		fmt.Println("Reboot required for the change to take effect.")
	}
	if jsonMode {
		// Reuse the mutation shape the hook already parses; a config edit stages
		// nothing (CONFIG.md §3.3).
		emitMutation([]string{id}, nil, false, decl.RebootRequired(), "")
	}
	return exitOK
}

// cmdConfigInit creates a declared config file from its example template
// (CONFIG.md §3.x). Mutating: it uses the identical write path as `config set`
// (sysroot write guard, path-policy re-check, symlinked-parent refusal, atomic
// write, Guard on the bytes). It refuses a declaration with no template, and
// refuses to overwrite an existing file unless --force is given.
func (a *App) cmdConfigInit(args []string) int {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	forceF := fs.Bool("force", false, "overwrite the file if it already exists")
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	if err := fs.Parse(flags); err != nil {
		return exitError
	}
	if len(pos) != 2 {
		fmt.Fprintln(os.Stderr, "usage: kpm config init <id> <name-or-index> [--force] [--json]")
		return exitError
	}
	id, sel := pos[0], pos[1]

	failJSON := func(msg string) {
		if jsonMode {
			emitMutation(nil, []jsonFailure{{ID: id, Error: msg}}, false, false, msg)
		}
	}

	// Off-device write guard, identical to cmd_config.go set / cmd_uninstall.go:
	// writes resolve against KPM_SYSROOT, so a KPM_ROOT sandbox without KPM_SYSROOT
	// would land on the real rootfs. On a real device both are unset (G5).
	if os.Getenv("KPM_SYSROOT") == "" {
		if os.Getenv("KPM_ROOT") != "" {
			fmt.Fprintln(os.Stderr, "kpm config init: KPM_ROOT is set but KPM_SYSROOT is not — refusing to write against the real rootfs; set KPM_SYSROOT")
			return exitError
		}
		if runtime.GOOS != "linux" {
			fmt.Fprintln(os.Stderr, "kpm config init: set KPM_SYSROOT when running off-device")
			return exitError
		}
	}

	decl, code := a.resolveConfig(id, sel, jsonMode, "init")
	if code != exitOK {
		return code
	}
	if decl.Template == "" {
		msg := fmt.Sprintf("%q declares no example template to create from", decl.Name)
		failJSON(msg)
		fmt.Fprintln(os.Stderr, "kpm config init:", msg)
		return exitError
	}

	// Re-validate the path immediately before writing (defense in depth: the local
	// packages.d snapshot is user-editable), returning the cleaned device path.
	abs, err := uninstall.ConfigPath(decl.Path)
	if err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config init:", err)
		return exitError
	}

	_, exists, err := a.readConfigFile(decl)
	if err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config init:", err)
		return exitError
	}
	if exists && !*forceF {
		msg := fmt.Sprintf("%q already exists — use --force to overwrite it from the template", decl.Name)
		failJSON(msg)
		fmt.Fprintln(os.Stderr, "kpm config init:", msg)
		return exitError
	}

	newRaw, err := modconfig.SeedContent(decl)
	if err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config init:", err)
		return exitError
	}

	// Never write through a symlinked parent (C7), the same guard set/uninstall apply.
	if bad, ok := uninstall.SymlinkedConfigParent(a.paths, abs); ok {
		msg := fmt.Sprintf("refusing to write %s: symlinked parent %s", decl.Path, bad)
		failJSON(msg)
		fmt.Fprintln(os.Stderr, "kpm config init:", msg)
		return exitError
	}
	if err := device.WriteFileAtomic(a.paths.HostPath(abs), newRaw); err != nil {
		failJSON(err.Error())
		fmt.Fprintln(os.Stderr, "kpm config init:", err)
		return exitError
	}

	a.paths.Log("CONFIG", fmt.Sprintf("%s  %s  init from template", id, decl.Name))
	fmt.Printf("kpm: created %s (%s) from its example\n", decl.Name, id)
	if decl.RebootRequired() {
		fmt.Println("Reboot required for the change to take effect.")
	}
	if jsonMode {
		emitMutation([]string{id}, nil, false, decl.RebootRequired(), "")
	}
	return exitOK
}

// applyEdit dispatches the format-specific edit and returns the new bytes plus a
// short human/log action description. It enforces the flag combinations valid for
// each format (CONFIG.md §3.2).
func applyEdit(raw []byte, decl config.ModConfig, provided map[string]bool, key, section, value string, line int, appendF, deleteF bool) ([]byte, string, error) {
	switch decl.Format {
	case config.FormatINI:
		if provided["line"] || appendF || deleteF {
			return nil, "", fmt.Errorf("--line/--append/--delete are for text files; use --key for %q", decl.Name)
		}
		if key == "" {
			return nil, "", fmt.Errorf("ini edit needs --key")
		}
		if !provided["value"] {
			return nil, "", fmt.Errorf("ini edit needs --value")
		}
		out, err := modconfig.SetKey(raw, section, key, value)
		if err != nil {
			return nil, "", err
		}
		label := key
		if section != "" {
			label = section + "." + key
		}
		return out, "set " + label, nil
	case config.FormatText:
		if key != "" || provided["section"] {
			return nil, "", fmt.Errorf("--key/--section are for ini files; use --line for %q", decl.Name)
		}
		switch {
		case appendF:
			if deleteF {
				return nil, "", fmt.Errorf("--append and --delete are mutually exclusive")
			}
			if !provided["value"] {
				return nil, "", fmt.Errorf("--append needs --value")
			}
			out, err := modconfig.AppendLine(raw, value)
			return out, "append line", err
		case deleteF:
			if !provided["line"] {
				return nil, "", fmt.Errorf("--delete needs --line")
			}
			out, err := modconfig.DeleteLine(raw, line)
			return out, fmt.Sprintf("delete line %d", line), err
		default:
			if !provided["line"] {
				return nil, "", fmt.Errorf("text edit needs --line (or --append)")
			}
			if !provided["value"] {
				return nil, "", fmt.Errorf("text edit needs --value")
			}
			out, err := modconfig.SetLine(raw, line, value)
			return out, fmt.Sprintf("set line %d", line), err
		}
	default:
		return nil, "", fmt.Errorf("unsupported config format %q", decl.Format)
	}
}

// resolveConfig loads the package and resolves the file selector, emitting the
// right error surface (jsonError for read-only, best-effort for set) on failure.
// A non-exitOK return means the caller should return that code.
func (a *App) resolveConfig(id, sel string, jsonMode bool, verb string) (config.ModConfig, int) {
	if !config.ValidID(id) {
		msg := fmt.Sprintf("invalid package id %q", id)
		a.configErr(jsonMode, verb, id, msg)
		return config.ModConfig{}, exitError
	}
	pkg, err := a.loadPackage(id)
	if err != nil {
		a.configErr(jsonMode, verb, id, err.Error())
		return config.ModConfig{}, exitError
	}
	decl, ok := selectConfig(pkg.Configs, sel)
	if !ok {
		msg := fmt.Sprintf("no config matching %q for %s", sel, id)
		a.configErr(jsonMode, verb, id, msg)
		return config.ModConfig{}, exitError
	}
	return decl, exitOK
}

// configErr emits the error surface for a config command: a best-effort mutation
// failure for `set` (the shape the hook parses), a plain jsonError otherwise.
func (a *App) configErr(jsonMode bool, verb, id, msg string) {
	if jsonMode {
		if verb == "set" {
			emitMutation(nil, []jsonFailure{{ID: id, Error: msg}}, false, false, msg)
		} else {
			jsonError(msg)
		}
	}
	fmt.Fprintf(os.Stderr, "kpm config %s: %s\n", verb, msg)
}

// selectConfig resolves a file selector to a declared config: a 1-based index or
// a case-insensitive name match (CONFIG.md §3.2). Index is tried first.
func selectConfig(cfgs []config.ModConfig, sel string) (config.ModConfig, bool) {
	if n, err := strconv.Atoi(strings.TrimSpace(sel)); err == nil {
		if n >= 1 && n <= len(cfgs) {
			return cfgs[n-1], true
		}
		return config.ModConfig{}, false
	}
	for _, c := range cfgs {
		if strings.EqualFold(c.Name, sel) {
			return c, true
		}
	}
	return config.ModConfig{}, false
}

// readConfigFile reads a declared file through the host mapping, applying the
// path policy and the modconfig read guards. Returns (raw, exists, err); a
// missing file is (nil, false, nil).
func (a *App) readConfigFile(decl config.ModConfig) (raw []byte, exists bool, err error) {
	abs, err := uninstall.ConfigPath(decl.Path)
	if err != nil {
		return nil, false, err
	}
	b, rerr := os.ReadFile(a.paths.HostPath(abs))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, false, nil
		}
		return nil, false, rerr
	}
	if gerr := modconfig.Guard(b); gerr != nil {
		return nil, true, gerr
	}
	return b, true, nil
}

// configState reports (exists, editable) for a declared file: editable is true
// for a valid path in an editable format (ini/text in this release); a path that
// fails the policy is non-editable and never probed on disk (CONFIG.md §3.3).
func (a *App) configState(decl config.ModConfig) (exists, editable bool) {
	abs, err := uninstall.ConfigPath(decl.Path)
	if err != nil {
		return false, false
	}
	editable = decl.Format == config.FormatINI || decl.Format == config.FormatText
	if _, serr := os.Stat(a.paths.HostPath(abs)); serr == nil {
		exists = true
	}
	return exists, editable
}
