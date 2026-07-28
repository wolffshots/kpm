//go:build !linux

package apptree

// statfsAvail has no portable answer off the device, so the free-space check is
// skipped on a host. The device is always linux/arm; the Windows/macOS build
// exists only to run these tests.
func statfsAvail(string) (avail, bsize int64, ok bool) { return 0, 0, false }
