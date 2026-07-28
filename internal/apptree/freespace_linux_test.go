//go:build linux

package apptree

// freespace_linux_test.go is the half of the free-space check that CANNOT run on
// the Windows development host: freespace_other.go returns ok=false, so on a dev
// machine Apply skips the check entirely and every assertion about it is vacuous.
// Both tests here therefore exist to be run on linux, which for this project
// means cross-compiling the test binary and running it under WSL:
//
//	GOOS=linux GOARCH=amd64 go test -c -o /tmp/apptree.test ./internal/apptree
//
// (TestApplyRefusesOnASmallFilesystem additionally needs a SMALL filesystem; the
// skip message below spells out how to make one without root.)

import (
	"fmt"
	"os"
	"testing"
)

// TestStatfsAvailReportsBlockSize pins the plumbing the cluster-rounding fix
// depends on. statfs has always returned Bsize; discarding it is what made the
// free-space check compare a plan's raw byte sum against free space and pass on
// a device that then filled its user partition to zero mid-extraction.
func TestStatfsAvailReportsBlockSize(t *testing.T) {
	dir := t.TempDir()
	avail, bsize, ok := statfsAvail(dir)
	if !ok {
		t.Fatalf("statfs(%s) failed on linux, where it is the whole point", dir)
	}
	if bsize < 512 {
		t.Errorf("bsize = %d, want the filesystem's real allocation block (>= 512)", bsize)
	}
	if bsize&(bsize-1) != 0 {
		t.Errorf("bsize = %d, want a power of two", bsize)
	}
	if avail <= 0 {
		t.Errorf("avail = %d, want the free space at %s", avail, dir)
	}
}

// TestApplyRefusesOnASmallFilesystem is the end-to-end form of H2 against a REAL
// filesystem: many small files whose raw byte sum fits comfortably and whose
// per-file block cost does not. Before the fix this passed the check and then
// died with ENOSPC partway through, leaving the filesystem at zero bytes free —
// on a Kobo, the state in which Nickel can no longer write its database.
//
// It runs only where t.TempDir() lands on a filesystem small enough to construct
// the scenario. To make one without root:
//
//	unshare -Urm --propagation private bash -c \
//	  'mount -t tmpfs -o size=24M tmpfs /mnt && TMPDIR=/mnt /tmp/apptree.test -test.run SmallFilesystem -test.v'
func TestApplyRefusesOnASmallFilesystem(t *testing.T) {
	p, sysroot := sandbox(t)
	avail, bsize, ok := statfsAvail(sysroot)
	if !ok {
		t.Fatalf("statfs(%s) failed", sysroot)
	}
	if bsize <= 0 {
		t.Fatalf("statfs(%s) reported block size %d; the whole check depends on it", sysroot, bsize)
	}
	// n files of one byte each: n bytes of content, n blocks of footprint. Pick
	// the smallest n whose block cost clears the space left over after the slack.
	spare := avail - freeSpaceSlack
	if spare <= 0 {
		t.Skipf("only %d bytes free at %s: below the %d-byte slack, so every plan is refused "+
			"and this test would pass without measuring anything", avail, sysroot, freeSpaceSlack)
	}
	n := spare/bsize + 64
	if n > 8000 {
		t.Skipf("%s has %d bytes free in %d-byte blocks: %d entries would be needed to "+
			"over-subscribe it. Re-run with TMPDIR on a small filesystem — see the comment above.",
			sysroot, avail, bsize, n)
	}

	ents := make([]zent, 0, n)
	for i := int64(0); i < n; i++ {
		ents = append(ents, zent{name: fmt.Sprintf("f%06d", i), body: "x"})
	}
	z := makeZip(t, ents...)
	plan, err := Compute(z, "", "/mnt/onboard/.adds/x")
	if err != nil {
		t.Fatal(err)
	}
	// The premise: by the raw sum this plan trivially fits.
	if plan.Bytes+freeSpaceSlack >= avail {
		t.Fatalf("fixture: raw need %d must fit in %d free", plan.Bytes+freeSpaceSlack, avail)
	}
	if _, err := Apply(p, plan, z); err == nil {
		t.Fatal("Apply must refuse: the plan's real block cost does not fit")
	} else {
		t.Logf("refused as designed: %v", err)
	}
	if _, err := os.Stat(hostOf(sysroot, "/mnt/onboard/.adds/x/f000000")); err == nil {
		t.Error("the refusal must happen before any file is written")
	}
	// And the filesystem still has room — the whole point of refusing early.
	after, _, _ := statfsAvail(sysroot)
	if after < avail/2 {
		t.Errorf("free space fell from %d to %d: the refusal did not happen before the writes", avail, after)
	}
}
