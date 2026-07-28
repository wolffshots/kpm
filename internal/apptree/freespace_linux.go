//go:build linux

package apptree

import "syscall"

// statfsAvail reports the space available to an unprivileged writer at hostDir
// AND the filesystem's allocation block size, which is what a file actually
// costs: on the FAT32 user partition a cluster is 32 KiB, so 1044 KOReader
// entries occupy far more than the sum of their sizes. Returning Bsize is the
// whole point — discarding it is how the check came to pass on a device that
// then ran out of space mid-extraction. stdlib only — the one-dependency
// constraint (PLAN.md §10) holds.
func statfsAvail(hostDir string) (avail, bsize int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(hostDir, &st); err != nil {
		return 0, 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Bsize), true
}
