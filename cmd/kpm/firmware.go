package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kpm/internal/device"
	"kpm/internal/state"
	"kpm/internal/uninstall"
)

// The firmware-change check (QT6-FIRMWARE6.md section 3f). A firmware update
// can remove the rootfs files of each tgz package, and the 4.x to 6.x upgrade
// surely does. state.json records the firmware of the last mutating run. When
// it changes, kpm forgets the installs that the update removed, so that
// `update --all` installs them again with the asset of the current line.
//
// The check has two halves around reconcile. dropOldLineStaged must run
// BEFORE reconcile: after a line change StagedTgz() names the slot of the new
// line, so reconcile would see the old staged tgz as consumed and promote it.
// forgetWipedInstalls runs AFTER reconcile, so it also sees a promotion of a
// tgz that the old firmware installed just before the update.

// firmwareChange returns the recorded and the current firmware, and whether
// the check must run. A firmware that is not recorded yet is not a change, with
// one exception: on firmware 6.x with an existing state. kpm before 0.13.0
// records no firmware and runs only on 4.x, so that state comes from 4.x and
// the device was upgraded (emulator finding of 2026-11-18: a user who goes
// from kpm 0.12.0 on 4.x straight to 6.x).
func (a *App) firmwareChange() (old, cur string, changed bool) {
	cur, ok := a.paths.Firmware()
	if !ok {
		return a.state.Firmware, "", false
	}
	old = a.state.Firmware
	if old == "" {
		upgraded := device.LineOf(cur) == device.Line6 && len(a.state.Packages) > 0
		if upgraded {
			a.paths.Log("INFO", "firmware not recorded: this state comes from firmware 4.x, so the device was upgraded")
		}
		return old, cur, upgraded
	}
	return old, cur, old != cur
}

// dropOldLineStaged deletes a staged tgz of the old firmware line and clears
// the staged state, when the line changed. The new line never reads that slot
// (section 3f rule 5). kpm deletes the file only when its content is the tgz
// that kpm staged. A foreign file stays.
func (a *App) dropOldLineStaged(old string) {
	oldLine := device.LineOf(old)
	if old == "" {
		oldLine = device.Line4 // see firmwareChange
	}
	if oldLine == 0 || oldLine == a.paths.Line() {
		return
	}
	path := a.paths.StagedTgzFor(oldLine)
	if _, err := os.Stat(path); err != nil {
		return
	}
	sum, size, err := sha256AndSize(path)
	if err != nil || a.state.StagedSHA256 == "" || sum != a.state.StagedSHA256 || size != a.state.StagedSize {
		return
	}
	if err := os.Remove(path); err != nil {
		a.paths.Log("WARN", fmt.Sprintf("firmware changed: could not remove %s: %v", filepath.Base(path), err))
		return
	}
	device.FsyncDir(filepath.Dir(path))
	a.state.RollbackStaged()
	a.paths.Log("INFO", fmt.Sprintf("firmware changed: removed the staged %s, which this firmware does not read", filepath.Base(path)))
}

// forgetWipedInstalls checks each installed package after a firmware change
// (section 3f rules 2 to 4). Only the rootfs members count: a firmware update
// writes a new rootfs, and it never touches the user partition (mnt/onboard).
//
//   - All rootfs members gone: kpm clears the installed state, so that
//     `update --all` installs the package again with the asset of the line.
//   - Some rootfs members gone: kpm records them in missing_files.
//   - kpm itself: kpm runs, so it stays installed. Its missing hook files go
//     into missing_files, and selfRepair makes `update` install kpm again.
//
// A package with no rootfs member (app-tree, file) is not checked.
func (a *App) forgetWipedInstalls() []string {
	var cleared []string
	for id, ps := range a.state.Packages {
		if ps.InstalledVersion == "" {
			continue
		}
		if id == selfID {
			// kpm installed by hand has no manifest. Its hook for this line is
			// always a kpm file (NickelSync suite finding of 2026-11-18).
			ps.Manifest = appendMissing(ps.Manifest, selfHookFor(a.paths.Line()))
		}
		var rootfs []string
		for _, m := range ps.Manifest {
			if !strings.HasPrefix(strings.ToLower(m), "mnt/onboard/") {
				rootfs = append(rootfs, m)
			}
		}
		gone := verifyManifest(a.paths, rootfs)
		if len(gone) == 0 {
			continue
		}
		if id == selfID || len(gone) < len(rootfs) {
			ps.MissingFiles = verifyManifest(a.paths, ps.Manifest)
			a.paths.Log("WARN", fmt.Sprintf("%s  firmware changed and removed %d of its files", id, len(gone)))
			continue
		}
		ps.InstalledVersion = ""
		ps.InstalledAt = ""
		ps.Manifest = nil
		ps.InstalledFingerprint = ""
		ps.MissingFiles = nil
		ps.VerifiedAt = ""
		cleared = append(cleared, id)
	}
	sort.Strings(cleared)
	for _, id := range cleared {
		a.paths.Log("INFO", fmt.Sprintf("%s  firmware changed and removed its rootfs files; marked not installed", id))
	}
	return cleared
}

// selfHookFor returns the manifest member of the kpm hook for the firmware
// line: the Qt 6 build on line 6, the Qt 5 build on line 4.
func selfHookFor(line int) string {
	if line == device.Line6 {
		return strings.TrimPrefix(selfHooks[1], "/")
	}
	return strings.TrimPrefix(selfHooks[0], "/")
}

// selfRepair reports whether kpm must install itself again although its
// version is current: a file of its manifest is missing, for example the hook
// after a firmware update. reverifyMissing clears missing_files when the files
// come back, for example after a manual install of Kobo.tgz.
func selfRepair(id string, ps *state.PackageState) bool {
	return id == selfID && len(ps.MissingFiles) > 0
}

// syncForLine syncs, from the registry cache, each registry-managed package
// that has no build for the firmware line. A def written by an older kpm has no
// [fw6] table, and the registry def can have one. syncOne skips a def with
// local edits, so this is the same change as `kpm sync <id>`.
func (a *App) syncForLine() {
	cfg, err := a.loadRegistryConfig()
	if err != nil {
		return
	}
	pkgs, err := a.loadPackages()
	if err != nil {
		return
	}
	line := a.paths.Line()
	for _, p := range pkgs {
		if p.Registry != "" && !p.AvailableOn(line) {
			a.syncOne(cfg, p, false)
		}
	}
}

// removeBuiltin uninstalls each package that firmware 6.x has built in
// (builtin_fw6, QT6-FIRMWARE6.md section 3g). It runs on line 6 after
// syncForLine, so a def that just got the key from the registry counts. kpm
// runs the uninstall recipe with purge, then deletes the def and the state. A
// failed removal keeps both, and the next firmware change or registry refresh
// tries again. The caller saves the state.
func (a *App) removeBuiltin() {
	if a.paths.Line() != device.Line6 {
		return
	}
	pkgs, err := a.loadPackages()
	if err != nil {
		return
	}
	for _, p := range pkgs {
		if p.BuiltinFw6 == "" || p.ID == selfID {
			continue
		}
		if err := deleteGuard(); err != nil {
			a.paths.Log("WARN", fmt.Sprintf("%s  built into firmware 6.x, not removed: %v", p.ID, err))
			continue
		}
		ps := a.state.Get(p.ID)
		plan, err := uninstall.Compute(ps.Manifest, p.Uninstall, true)
		if err != nil {
			a.paths.Log("WARN", fmt.Sprintf("%s  built into firmware 6.x, not removed: %v", p.ID, err))
			continue
		}
		if plan.RunBefore != "" {
			if err := runHook(plan.RunBefore); err != nil {
				a.paths.Log("WARN", fmt.Sprintf("%s  built into firmware 6.x, not removed: run_before failed: %v", p.ID, err))
				continue
			}
		}
		res := uninstall.Execute(a.paths, plan)
		if plan.RunAfter != "" {
			if err := runHook(plan.RunAfter); err != nil {
				a.paths.Log("WARN", fmt.Sprintf("%s  run_after failed: %v", p.ID, err))
			}
		}
		a.logUninstall(p.ID, plan, res)
		if !res.OK() {
			a.paths.Log("WARN", fmt.Sprintf("%s  built into firmware 6.x, removal partial: %d failed, %d skipped", p.ID, len(res.Failed), len(res.Skipped)))
			continue
		}
		if err := os.Remove(a.paths.PackageFile(p.ID)); err != nil && !os.IsNotExist(err) {
			a.paths.Log("WARN", fmt.Sprintf("%s  built into firmware 6.x, could not remove its def: %v", p.ID, err))
			continue
		}
		delete(a.state.Packages, p.ID)
		a.paths.Log("INFO", fmt.Sprintf("%s  built into firmware 6.x (%s); uninstalled", p.ID, p.BuiltinFw6))
	}
}
