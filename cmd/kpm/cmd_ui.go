package main

import (
	"fmt"
	"os"
)

// uiTrigger is the tmpfs file the NickelKPM hook watches to open the graphical
// package manager (NICKELMENU-LAUNCH.md). Kept in sync with Files::ui_trigger in
// the hook's files.h.
const uiTrigger = "/tmp/nkpm-open"

// cmdUI signals the in-Nickel NickelKPM hook to open the browser by truncating+
// writing the trigger file. It is what the NickelMenu "kpm - Package manager"
// item runs. It touches no kpm state and takes no lock; if the hook is not
// loaded (UI not installed) the write is a harmless no-op.
func cmdUI(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: kpm ui")
		return exitError
	}
	f, err := os.OpenFile(uiTrigger, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm ui:", err)
		return exitError
	}
	_, werr := f.Write([]byte("1"))
	cerr := f.Close()
	if werr != nil {
		fmt.Fprintln(os.Stderr, "kpm ui:", werr)
		return exitError
	}
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "kpm ui:", cerr)
		return exitError
	}
	fmt.Println("requested the package manager UI")
	return exitOK
}
