package device

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
	"time"
)

// HasQndb reports whether the NickelDBus qndb binary is available.
func HasQndb() bool {
	_, err := exec.LookPath("qndb")
	return err == nil
}

// Toast shows a NickelDBus toast for ms milliseconds if qndb is present.
// It is best-effort: absence of qndb or any error is silently ignored.
func Toast(ms int, msg string) {
	path, err := exec.LookPath("qndb")
	if err != nil {
		return
	}
	_ = exec.Command(path, "-m", "mwcToast", strconv.Itoa(ms), msg).Run()
}

// netTimeout, netInterval and netBudget define the WaitForNetwork gate: a HEAD
// request every netInterval (each capped at netTimeout) until netBudget total
// elapses (~30 s) — D3b.
const (
	netTimeout  = 2 * time.Second
	netInterval = 1 * time.Second
	netBudget   = 30 * time.Second
)

// WaitForNetwork polls a HEAD request against host until it responds or the
// ~30 s budget is exhausted (D3b). Returns true once reachable.
func WaitForNetwork(client *http.Client, host string) bool {
	url := "https://" + host + "/"
	deadline := time.Now().Add(netBudget)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), netTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		if err == nil {
			resp, derr := client.Do(req)
			if derr == nil {
				resp.Body.Close()
				cancel()
				return true
			}
		}
		cancel()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(netInterval)
	}
}

// Reboot syncs and reboots the device. Tries /sbin/reboot, then busybox
// reboot, then bare reboot. On success the reboot command has been launched
// and returns nil; on failure it returns the last error (G6).
func Reboot() error {
	sync()
	time.Sleep(2 * time.Second)
	candidates := [][]string{
		{"/sbin/reboot"},
		{"busybox", "reboot"},
		{"reboot"},
	}
	var lastErr error
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			lastErr = err
			continue
		}
		lastErr = exec.Command(c[0], c[1:]...).Run()
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func sync() {
	if p, err := exec.LookPath("sync"); err == nil {
		_ = exec.Command(p).Run()
	}
}
