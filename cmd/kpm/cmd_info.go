package main

import (
	"fmt"
	"os"
	"strings"

	"kpm/internal/config"
	"kpm/internal/device"
	"kpm/internal/registry"
)

// cmd_info.go implements `kpm info <id> [--json]`: everything kpm knows about
// one package, for the detail page of the UI. Read only: no lock, no network,
// no state write. It reads state, packages.d and the registry caches.

// infoFiles is how many manifest paths info returns. The UI shows a few paths
// and the total: an app-tree package has thousands of files.
const infoFiles = 3

// Checksum states of the installed version (INTEGRITY.md section 5).
const (
	checksumDeclared   = "declared"     // the def has a sha256 for the installed tag
	checksumNoneForTag = "none-for-tag" // the def has checksums, but not for this tag
	checksumNone       = "none"         // the def has no checksums
)

// Carry-over states of a package on firmware 4.x (QT6-FIRMWARE6.md section 3h).
const (
	carryInBundle    = "in-bundle"
	carryNotInBundle = "not-in-bundle"
)

type jsonInfoFw6 struct {
	Source   *string `json:"source"`
	Forge    *string `json:"forge"`
	Asset    string  `json:"asset"`
	TestedFw *string `json:"tested_fw"`
}

// jsonInfoDef is the definition the detail page shows: the registry def when
// there is one (the upstream def), else the local packages.d def.
type jsonInfoDef struct {
	From          string       `json:"from"` // "registry" | "local"
	Registry      *string      `json:"registry"`
	Source        string       `json:"source"`
	Forge         string       `json:"forge"`
	Asset         string       `json:"asset"`
	UpdateCheck   string       `json:"update_check"`
	MinKpm        *string      `json:"min_kpm"`
	Conflicts     []string     `json:"conflicts"`
	TestedFw      *string      `json:"tested_fw"`
	Fw6           *jsonInfoFw6 `json:"fw6"`
	BuiltinFw6    *string      `json:"builtin_fw6"`
	InstallMethod string       `json:"install_method"`
	Kind          string       `json:"kind"`
}

type jsonInfoCarryover struct {
	State   string  `json:"state"`   // in-bundle | not-in-bundle
	Version *string `json:"version"` // the 6.x version in the bundle
	Reason  *string `json:"reason"`  // why the package is not in the bundle
}

type jsonInfoPayload struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	DeviceFw *string     `json:"device_fw"`
	Line     int         `json:"line"`
	Def      jsonInfoDef `json:"def"`
	// DefDiffers is true when the local def is behind its registry def, so
	// `kpm sync` would change it.
	DefDiffers  bool     `json:"def_differs"`
	Registered  bool     `json:"registered"` // a packages.d def exists
	Installed   *string  `json:"installed"`
	Staged      *string  `json:"staged"`
	Pinned      *string  `json:"pinned"`
	LatestSeen  *string  `json:"latest_seen"`
	LastChecked *string  `json:"last_checked"`
	Checksum    *string  `json:"checksum"` // null when not installed
	Files       []string `json:"files"`    // the first infoFiles manifest paths
	FilesTotal  int      `json:"files_total"`
	Configs     []string `json:"configs"`
	// Carryover is null on firmware 6.x and for a package that is not installed.
	Carryover *jsonInfoCarryover `json:"carryover"`
}

func (a *App) cmdInfo(args []string) int {
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	fail := func(msg string) int {
		if jsonMode {
			jsonError(msg)
		}
		fmt.Fprintln(os.Stderr, "kpm info:", msg)
		return exitError
	}
	if len(flags) > 0 || len(pos) != 1 {
		return fail("usage: kpm info <id> [--json]")
	}
	id := pos[0]

	// A missing or broken registry cache is not fatal: info then shows the
	// local def only.
	var entry *registry.Entry
	manByName := map[string]*registry.Manifest{}
	if cfg, err := a.loadRegistryConfig(); err == nil {
		mans, _ := a.cachedManifests(cfg)
		for _, m := range mans {
			manByName[m.Name] = m.Manifest
		}
		entries, _ := registry.Merge(mans)
		entry = entries[id]
	}
	local, _ := a.loadPackage(id)
	if entry == nil && local == nil {
		return fail(fmt.Sprintf("package %q not found", id))
	}

	out := a.packageInfo(id, entry, local, manByName)
	if jsonMode {
		jsonLine(out)
		return exitOK
	}
	printInfo(out)
	return exitOK
}

// packageInfo builds the info payload. entry or local may be nil, not both.
func (a *App) packageInfo(id string, entry *registry.Entry, local *config.Package, manByName map[string]*registry.Manifest) jsonInfoPayload {
	deviceFw, _ := a.paths.Firmware()
	line := a.paths.Line()
	ps := a.state.Get(id)

	out := jsonInfoPayload{
		ID:          id,
		DeviceFw:    ptr(deviceFw),
		Line:        line,
		Registered:  local != nil,
		Installed:   ptr(ps.InstalledVersion),
		Staged:      ptr(ps.StagedVersion),
		LatestSeen:  ptr(ps.LatestSeen),
		LastChecked: ptr(ps.LastChecked),
		Files:       []string{},
		FilesTotal:  len(ps.Manifest),
		Configs:     []string{},
	}

	// The def to show: the registry def (upstream), else the local def.
	var def config.Package
	var testedFw, testedFw6 string
	if entry != nil {
		def = *entry.Def.ToPackage(id, entry.Registry, "")
		testedFw = entry.Def.TestedFw
		if entry.Def.Fw6 != nil {
			testedFw6 = entry.Def.Fw6.TestedFw
		}
		out.Def.From = "registry"
		out.Def.Registry = ptr(entry.Registry)
		out.Name = entry.Def.Name
	} else {
		def = *local
		out.Def.From = "local"
		out.Def.Registry = ptr(local.Registry)
	}
	if out.Name == "" && local != nil {
		out.Name = local.Name
	}
	if out.Name == "" {
		out.Name = id
	}
	out.Def.Source = def.Source
	if local != nil && entry == nil {
		out.Def.Source = a.effectiveSource(local) // an adopted kpm keeps its source in state
	}
	out.Def.Forge = def.Forge
	if out.Def.Forge == "" {
		out.Def.Forge = config.ForgeGitHub
	}
	out.Def.Asset = def.Asset
	out.Def.UpdateCheck = def.EffectiveUpdateCheck()
	out.Def.MinKpm = ptr(def.MinKpm)
	out.Def.Conflicts = def.Conflicts
	if out.Def.Conflicts == nil {
		out.Def.Conflicts = []string{}
	}
	out.Def.TestedFw = ptr(testedFw)
	if def.Fw6 != nil {
		out.Def.Fw6 = &jsonInfoFw6{
			Source:   ptr(def.Fw6.Source),
			Forge:    ptr(def.Fw6.Forge),
			Asset:    def.Fw6.Asset,
			TestedFw: ptr(testedFw6),
		}
	}
	out.Def.BuiltinFw6 = ptr(def.BuiltinFw6)
	inst := effectiveInstall(local, entry)
	out.Def.InstallMethod = installMethodOf(inst)
	out.Def.Kind = kindOf(inst)

	if local != nil && entry != nil {
		out.DefDiffers = a.defUpdateAvailable(id, local, entry, manByName)
	}
	if local != nil {
		out.Pinned = ptr(a.effectivePin(local))
		for _, c := range local.Configs {
			out.Configs = append(out.Configs, c.Name)
		}
	} else if id == selfID {
		out.Pinned = ptr(ps.Pin)
	}

	n := min(infoFiles, len(ps.Manifest))
	out.Files = append(out.Files, ps.Manifest[:n]...)

	if ps.InstalledVersion != "" {
		// The checksum and the carry-over follow the local def: that is the
		// def kpm installed from and updates with.
		ref := def
		if local != nil {
			ref = *local
		}
		out.Checksum = ptr(checksumState(ref.ForLine(line), ps.InstalledVersion))
		if line == device.Line4 {
			out.Carryover = a.carryoverInfo(id, local, entry)
		}
	}
	return out
}

// checksumState says whether the def declares a sha256 for the tag.
func checksumState(p config.Package, tag string) string {
	if _, ok := p.ChecksumFor(tag); ok {
		return checksumDeclared
	}
	if len(p.Checksums) > 0 {
		return checksumNoneForTag
	}
	return checksumNone
}

// carryoverInfo says whether an installed package is in the 6.x bundle, and
// why not. The rules mirror carryoverWant.
func (a *App) carryoverInfo(id string, local *config.Package, entry *registry.Entry) *jsonInfoCarryover {
	if co := a.state.Carryover; co != nil {
		if cp, ok := co.Packages[id]; ok {
			return &jsonInfoCarryover{State: carryInBundle, Version: ptr(cp.Version)}
		}
	}
	reason := "not in the bundle yet: kpm builds it after the next check"
	src := ""
	if local != nil {
		src, _ = a.fw6SourcePin(local)
	}
	switch {
	case local == nil:
		reason = "not registered with kpm"
	case local.InstallsLive():
		reason = "installs on the user partition, which the upgrade keeps"
	case local.BuiltinFw6 != "":
		reason = "built into firmware 6.x"
	case local.Fw6 == nil && entry != nil && entry.Def.Fw6 != nil:
		reason = "the local def has no [fw6] table: sync updates it"
	case local.Fw6 == nil:
		reason = config.Unavailable6
	case !config.SourceConfigured(src):
		reason = "no source for the 6.x build"
	}
	return &jsonInfoCarryover{State: carryNotInBundle, Reason: ptr(reason)}
}

// printInfo is the human form of `kpm info`.
func printInfo(in jsonInfoPayload) {
	line := func(k string, v *string) {
		if v != nil {
			fmt.Printf("%-14s %s\n", k+":", *v)
		}
	}
	fmt.Printf("%s (%s)\n", in.Name, in.ID)
	line("installed", in.Installed)
	line("staged", in.Staged)
	line("pinned", in.Pinned)
	line("latest seen", in.LatestSeen)
	line("last checked", in.LastChecked)
	line("checksum", in.Checksum)
	fmt.Printf("%-14s %s\n", "def:", in.Def.From)
	line("registry", in.Def.Registry)
	fmt.Printf("%-14s %s (%s)\n", "source:", in.Def.Source, in.Def.Forge)
	fmt.Printf("%-14s %s\n", "asset 4.x:", in.Def.Asset)
	line("tested_fw", in.Def.TestedFw)
	if in.Def.Fw6 != nil {
		fmt.Printf("%-14s %s\n", "asset 6.x:", in.Def.Fw6.Asset)
		line("source 6.x", in.Def.Fw6.Source)
		line("tested_fw 6.x", in.Def.Fw6.TestedFw)
	}
	line("builtin_fw6", in.Def.BuiltinFw6)
	if in.DefDiffers {
		fmt.Println("def:           the local def differs from the registry (kpm sync)")
	}
	if in.FilesTotal > 0 {
		fmt.Printf("%-14s %d (%s)\n", "files:", in.FilesTotal, strings.Join(in.Files, ", "))
	}
	if len(in.Configs) > 0 {
		fmt.Printf("%-14s %s\n", "configs:", strings.Join(in.Configs, ", "))
	}
	if c := in.Carryover; c != nil {
		if c.State == carryInBundle {
			fmt.Printf("%-14s in the 6.x bundle (%s)\n", "carry-over:", *c.Version)
		} else {
			fmt.Printf("%-14s not in the 6.x bundle: %s\n", "carry-over:", *c.Reason)
		}
	}
}
