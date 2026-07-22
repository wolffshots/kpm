package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"kpm/internal/config"
	"kpm/internal/registry"
	"kpm/internal/state"
	"kpm/internal/uninstall"
)

// cmd_doctor.go implements `kpm doctor [<id>]` — a read-only diagnostic that
// reports whether an installed NickelHook plugin actually LOADED into Nickel, as
// opposed to merely being installed on disk (DOCTOR.md). Born from the
// NickelNote-on-FW-4.45 case: kpm said "installed / up to date" while the mod did
// nothing. It takes no lock, no network, and never mutates state.
//
// The three on-device signals kpm can read as root, in precedence order:
//   1. a `<plugin_so>.failsafe` quarantine sibling → crashed and backed out;
//   2. a NickelHook dump-log `/mnt/onboard/<nh_name>_*.log` newer than the
//      install → the two required hooks hard-failed and NickelHook dumped logread
//      (nh.c:266 — onboard ROOT, filename date is raw struct-tm, matched by
//      prefix/suffix + mtime, never by parsing the date);
//   3. the plugin basename absent from /proc/<nickel>/maps → not mapped in.
// Otherwise the plugin is mapped → "loaded" (which proves mapping, NOT that the
// mod functions — a backed-out mod stays mapped and a silent hook no-op leaves no
// trace; see the human footer and DOCTOR.md).

// doctorResult is one package's diagnosis.
type doctorResult struct {
	id         string
	verdict    string // loaded|not-loaded|crashed|load-failed|unknown
	detail     string // human sentence ("" when none)
	plugin     string // diagnosed plugin .so device path ("" when non-diagnosable)
	testedFw   string
	fwUntested bool
}

// cmdDoctor diagnoses one package (by id) or every installed NickelHook plugin
// (no id). Read-only: it reads state, the registry cache, on-disk siblings, and
// /proc only. Exit 0 whenever the diagnosis ran — an individual bad verdict is
// data, not a command failure; exit 1 only for a usage or unknown-package error.
func (a *App) cmdDoctor(args []string) int {
	flags, pos := splitArgs(args, nil)
	flags, jsonMode := takeJSON(flags)
	if len(flags) > 0 || len(pos) > 1 {
		if jsonMode {
			jsonError("usage: kpm doctor [<id>] [--json]")
		}
		fmt.Fprintln(os.Stderr, "usage: kpm doctor [<id>] [--json]")
		return exitError
	}

	// Registry cache defs supply nh_name and tested_fw, cross-referenced by id the
	// same way search does (cachedManifests + Merge). A missing/broken cache is not
	// fatal here — doctor just loses the dump-log key (falls back to the display
	// name) and the firmware note.
	entries := map[string]*registry.Entry{}
	if cfg, err := a.loadRegistryConfig(); err == nil {
		mans, _ := a.cachedManifests(cfg)
		entries, _ = registry.Merge(mans)
	}

	pkgs, err := a.loadPackages()
	if err != nil {
		if jsonMode {
			jsonError(err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm doctor:", err)
		return exitError
	}

	var targets []*config.Package
	if len(pos) == 1 {
		id := pos[0]
		for _, p := range pkgs {
			if p.ID == id {
				targets = []*config.Package{p}
				break
			}
		}
		if targets == nil {
			msg := fmt.Sprintf("package %q not registered", id)
			if jsonMode {
				jsonError(msg)
			}
			fmt.Fprintln(os.Stderr, "kpm doctor:", msg)
			return exitError
		}
	} else {
		// No id: every INSTALLED package (installed_version recorded). A def with no
		// install has no manifest to infer a plugin from anyway.
		for _, p := range pkgs {
			if a.state.Get(p.ID).InstalledVersion != "" {
				targets = append(targets, p)
			}
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	}

	deviceFw, _ := a.paths.Firmware()
	results := make([]doctorResult, 0, len(targets))
	for _, p := range targets {
		results = append(results, a.diagnose(p, entries, deviceFw))
	}

	if jsonMode {
		out := make([]jsonDoctorPkg, 0, len(results))
		for _, r := range results {
			out = append(out, jsonDoctorPkg{
				ID:         r.id,
				Verdict:    r.verdict,
				Detail:     ptr(r.detail),
				Plugin:     ptr(r.plugin),
				TestedFw:   ptr(r.testedFw),
				FwUntested: r.fwUntested,
			})
		}
		jsonLine(jsonDoctorPayload{DeviceFw: ptr(deviceFw), Packages: out})
		return exitOK
	}

	a.printDoctor(results, deviceFw)
	return exitOK
}

// diagnose runs the signal chain for one package and returns its verdict.
func (a *App) diagnose(p *config.Package, entries map[string]*registry.Entry, deviceFw string) doctorResult {
	ps := a.state.Get(p.ID)
	res := doctorResult{id: p.ID, verdict: "unknown"}

	var def *registry.PackageDef
	if e := entries[p.ID]; e != nil {
		def = e.Def
		res.testedFw = def.TestedFw
	}

	// Plugin inference: a manifest member under the NickelHook imageformats dir.
	// No such member (a plain-binary package like kscribbler, or an empty manifest)
	// → not a Nickel plugin, nothing to diagnose.
	abs, ok := pluginSO(ps.Manifest)
	if !ok {
		res.detail = "not a Nickel plugin — not diagnosable"
		return res
	}
	res.plugin = abs
	host := a.paths.HostPath(abs)
	base := filepath.Base(abs)

	// nh_name: the registry field, else the display name with spaces stripped.
	nhName := ""
	if def != nil {
		nhName = def.NhName
	}
	if nhName == "" {
		nhName = strings.ReplaceAll(p.Name, " ", "")
	}

	res.fwUntested = registry.FirmwareUntested(deviceFw, res.testedFw)

	// Signal 1: failsafe quarantine sibling (the loader renamed the .so aside and
	// Nickel crashed before it was restored — nh.c failsafe).
	if _, err := os.Lstat(host + ".failsafe"); err == nil {
		res.verdict = "crashed"
		res.detail = "crashed and quarantined by failsafe"
		appendCause(&res, deviceFw)
		return res
	}

	// Signal 2: a NickelHook dump-log newer than the install → load hard-failed.
	if name := recentDumpLog(a.paths.Root, nhName, ps.InstalledAt); name != "" {
		res.verdict = "load-failed"
		res.detail = "load failed — see " + name
		appendCause(&res, deviceFw)
		return res
	}

	// Signal 3: is the plugin mapped into the running Nickel? Off-device (or with
	// no running nickel) the map is unavailable, so this signal is skipped and the
	// verdict falls through to loaded-but-unverified rather than erroring.
	maps, avail := a.mapsReader()()
	if !avail {
		res.verdict = "loaded"
		res.detail = "mapping check unavailable (no running Nickel); on-disk signals are clean"
		return res
	}
	if !strings.Contains(maps, base) {
		res.verdict = "not-loaded"
		res.detail = "not loaded — plugin not mapped into Nickel"
		appendCause(&res, deviceFw)
		return res
	}
	res.verdict = "loaded"
	res.detail = "plugin mapped into Nickel (proves it loaded, not that it works)"
	return res
}

// appendCause tacks the advisory firmware clause onto a bad verdict's detail when
// the device is newer than the def's tested_fw — the likely cause of the failure
// (D). FirmwareUntested already guarded the empty cases when res.fwUntested was set.
func appendCause(res *doctorResult, deviceFw string) {
	if res.fwUntested {
		res.detail += fmt.Sprintf("; last confirmed on firmware %s; your device runs %s", res.testedFw, deviceFw)
	}
}

// pluginSO returns the device-absolute path of the first manifest member that is a
// NickelHook imageformats plugin (usr/local/Kobo/imageformats/*.so), and whether
// one was found. Members are cleaned through the uninstall path plumbing (a member
// that fails to clean is skipped); path.Match's '*' does not span '/', so only a
// .so directly in that directory matches.
func pluginSO(members []string) (deviceAbs string, ok bool) {
	for _, m := range members {
		abs, err := uninstall.CleanDeviceAbs("/" + m)
		if err != nil {
			continue
		}
		if matched, _ := path.Match("/usr/local/Kobo/imageformats/*.so", abs); matched {
			return abs, true
		}
	}
	return "", false
}

// recentDumpLog returns the basename of the newest NickelHook dump-log for nhName
// that is newer than the install, or "" when there is none. NickelHook writes
// logread to /mnt/onboard/<name>_<date>.log on a hard load failure (nh.c:266); the
// date is a raw struct-tm we never parse — matching is by the "<name>_" prefix and
// ".log" suffix, and staleness is decided purely by file mtime vs installed_at, so
// a dump left over from a previous install is ignored. An unparseable/empty
// installed_at makes the comparison impossible, so no dump is reported (fail safe:
// never a false load-failed).
func recentDumpLog(root, nhName, installedAt string) string {
	if nhName == "" || installedAt == "" {
		return ""
	}
	installed, err := time.Parse(state.TimeFormat, installedAt)
	if err != nil {
		return ""
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	prefix := nhName + "_"
	newest := ""
	var newestT time.Time
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(installed) && info.ModTime().After(newestT) {
			newest = name
			newestT = info.ModTime()
		}
	}
	return newest
}

// mapsReader returns the seam used to read the running Nickel process's memory
// map (signal 3): the App's injected fake when set (tests), else the real
// /proc-backed reader (DOCTOR.md).
func (a *App) mapsReader() func() (string, bool) {
	if a.nickelMaps != nil {
		return a.nickelMaps
	}
	return readNickelMaps
}

// readNickelMaps finds the running Nickel process by scanning /proc/*/comm for the
// exact name "nickel" and returns its /proc/<pid>/maps content. The bool is false
// when there is no such process or /proc is unavailable (any non-device host,
// where the glob simply matches nothing) — doctor then skips signal 3 gracefully.
func readNickelMaps() (string, bool) {
	comms, _ := filepath.Glob("/proc/*/comm")
	for _, c := range comms {
		b, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) != "nickel" {
			continue
		}
		pid := filepath.Base(filepath.Dir(c))
		m, err := os.ReadFile("/proc/" + pid + "/maps")
		if err != nil {
			return "", false
		}
		return string(m), true
	}
	return "", false
}

// printDoctor renders the human table plus the always-on caveat footer.
func (a *App) printDoctor(results []doctorResult, deviceFw string) {
	if len(results) == 0 {
		fmt.Println("no installed packages to diagnose")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "PACKAGE\tVERDICT\tDETAIL")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.id, r.verdict, r.detail)
	}
	w.Flush()
	fmt.Println()
	fmt.Println(`Note: "loaded" means the plugin is mapped into Nickel — it does NOT prove the`)
	fmt.Println(`mod works. A backed-out mod stays mapped, and a hook that silently no-ops`)
	fmt.Println(`leaves no trace kpm can detect. Run tools/symcheck before install to catch`)
	fmt.Println(`missing-symbol incompatibilities statically.`)
}
