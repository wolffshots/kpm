package apptree

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A `file` plan renames the downloaded asset onto dest, and a second apply
// replaces the first in place. The asset is moved, not copied.
func TestApplyFileRenamesAssetIntoPlace(t *testing.T) {
	p, sysroot := sandbox(t)
	dest := "/mnt/onboard/.kobo/custom-dict/dicthtml-x.zip"
	plan, err := PlanFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Dest != "/mnt/onboard/.kobo/custom-dict" || !plan.Verbatim {
		t.Fatalf("plan = %+v", plan)
	}

	for _, body := range []string{"v1", "v2"} {
		src := filepath.Join(sysroot, "asset.file")
		if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Apply(p, plan, src)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if got, _ := os.ReadFile(hostOf(sysroot, dest)); string(got) != body {
			t.Errorf("%s: dest holds %q", body, got)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Errorf("%s: the asset must be moved, not copied", body)
		}
		if want := []string{"mnt/onboard/.kobo/custom-dict/dicthtml-x.zip"}; !reflect.DeepEqual(res.Manifest, want) {
			t.Errorf("%s: manifest = %v, want %v", body, res.Manifest, want)
		}
	}
}

func TestPlanFileRefusesBadDest(t *testing.T) {
	if _, err := PlanFile("/mnt/onboard/.kobo/custom-dict/notes.txt"); err == nil {
		t.Error("a name Nickel ignores must be refused")
	}
}
