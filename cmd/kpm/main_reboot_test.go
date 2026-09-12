package main

import (
	"os"
	"testing"
)

// TestMain makes a real reboot unreachable from the test binary. cmdUpdate and
// cmdUninstall call device.Reboot through the rebootDevice var, and a fixture
// that passes --reboot down a path that decides to use it would otherwise run
// /sbin/reboot on the machine running `go test`. Today the only such fixture is
// saved by its dest not being a content dir, which is not a property a future
// fixture has to keep.
//
// A test that MEANS to exercise the reboot decision stubs the var itself (see
// noReboot in content_test.go); this default is for every test that does not.
func TestMain(m *testing.M) {
	rebootDevice = func() error {
		panic("a test reached device.Reboot: stub rebootDevice (see noReboot) if the reboot decision is under test")
	}
	os.Exit(m.Run())
}
