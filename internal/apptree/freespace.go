package apptree

// FreeSpaceSlack is the headroom the extractor requires beyond a plan's
// footprint, exported for the cmd layer's pre-download check. It aliases the one
// constant apply.go uses, so the two checks cannot drift to different numbers.
const FreeSpaceSlack = freeSpaceSlack

// AvailableBytes reports the space available to an unprivileged writer at
// hostDir, and whether the host could answer at all. It is statfsAvail's exported
// half, for the cmd layer's pre-download check: a 22 MiB font zip downloaded onto
// a full FAT32 partition leaves Nickel unable to write its database, and the
// extractor's own check runs far too late — after the bytes are already on disk.
//
// The block size stays internal: only the extraction footprint needs it. ok is
// false on every non-linux build (freespace_other.go), and the caller must let
// the operation proceed then — an unknown filesystem is not a full one.
func AvailableBytes(hostDir string) (avail int64, ok bool) {
	avail, _, ok = availableBytes(hostDir)
	return avail, ok
}
