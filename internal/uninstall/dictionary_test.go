package uninstall

import (
	"testing"

	"kpm/internal/config"
)

const dictDir = "/mnt/onboard/.kobo/custom-dict"

// A `file` install goes DIRECTLY into a content dir that takes files, under the
// name Nickel lists. Anything else would install and do nothing, or would leave
// the per-file ownership rules (D4).
func TestInstallFileDest(t *testing.T) {
	for _, ok := range []string{
		dictDir + "/dicthtml-cosmere-en.zip",
		"/mnt/onboard/.kobo/Custom-Dict/DICTHTML-x.ZIP", // FAT32 folds ASCII case
	} {
		if _, err := InstallFileDest(ok); err != nil {
			t.Errorf("InstallFileDest(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{
		dictDir + "/foo.zip",                  // a name Nickel ignores
		dictDir + "/sub/dicthtml-x.zip",       // not a direct child
		dictDir,                               // the dir itself
		"/mnt/onboard/fonts/dicthtml-x.zip",   // fonts take no file installs
		"/mnt/onboard/.adds/x/dicthtml-x.zip", // not a content dir
		dictDir + "/../dict/dicthtml-x.zip",   // traversal
		"/mnt/onboard/dicthtml-x.zip",         // the partition root
	} {
		if _, err := InstallFileDest(bad); err == nil {
			t.Errorf("InstallFileDest(%q) accepted, want refused", bad)
		}
	}
}

// The dictionary row is a content dir with its own kind, and it needs no reboot.
func TestContentDirForFile(t *testing.T) {
	c, ok := ContentDirFor(&config.Install{Method: config.MethodFile, Dest: dictDir + "/dicthtml-x.zip"})
	if !ok || c.Kind != "dictionary" || c.Reboot {
		t.Errorf("ContentDirFor = %+v, %v; want the dictionary row with no reboot", c, ok)
	}
	if c, ok := ContentDirFor(&config.Install{Method: config.MethodAppTree, Dest: "/mnt/onboard/fonts"}); !ok || !c.Reboot {
		t.Errorf("fonts = %+v, %v; want the font row with a reboot", c, ok)
	}
}

func TestValidateInstallDeclFile(t *testing.T) {
	dest := dictDir + "/dicthtml-x.zip"
	if err := ValidateInstallDecl(&config.Install{Method: config.MethodFile, Dest: dest}); err != nil {
		t.Fatalf("a dictionary def must validate: %v", err)
	}
	for name, bad := range map[string]*config.Install{
		"no dest":  {Method: config.MethodFile},
		"dir dest": {Method: config.MethodFile, Dest: dictDir},
		"root":     {Method: config.MethodFile, Dest: dest, Root: "x"},
		"launcher": {Method: config.MethodFile, Dest: dest, Launcher: &config.Launcher{NmFile: "/mnt/onboard/.adds/nm/x", NmEntry: "x"}},
	} {
		if ValidateInstallDecl(bad) == nil {
			t.Errorf("%s: accepted, want refused", name)
		}
	}
}
