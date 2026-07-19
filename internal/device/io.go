package device

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// logTime is the timestamp format used in kpm.log lines.
const logTime = "2006-01-02 15:04:05"

// logMaxBytes is the size past which kpm.log is rotated to kpm.log.1 (G3).
const logMaxBytes = 256 * 1024

// Log appends one event line to kpm.log. event is a fixed-width verb
// (CHECK, STAGE, REBOOT, INSTALLED, PIN, ...); detail is the rest of the line.
// Before appending it rotates the log to kpm.log.1 if it has grown past
// logMaxBytes (G3).
func (p Paths) Log(event, detail string) error {
	if info, err := os.Stat(p.LogFile()); err == nil && info.Size() > logMaxBytes {
		os.Rename(p.LogFile(), p.LogFile()+".1") // overwrites any previous .1
	}
	line := fmt.Sprintf("%s  %-9s  %s\n", time.Now().Format(logTime), event, detail)
	f, err := os.OpenFile(p.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

// WriteStatus overwrites status.txt with a short dialog-friendly summary,
// written atomically (temp file + fsync + rename) so a reader never sees a
// half-written status (G2/B3).
func (p Paths) WriteStatus(s string) error {
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	dir := filepath.Dir(p.StatusFile())
	tmp, err := os.CreateTemp(dir, ".status-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(s); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, p.StatusFile())
}

// WriteFileAtomic writes data to path atomically (temp file in the same dir +
// fsync + rename), so a reader never sees a half-written file and a failed write
// never clobbers the previous good copy (B3/G2). Used for config.toml and the
// registry.toml caches.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".kpm-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadStatus returns status.txt contents (empty string if it does not exist).
func (p Paths) ReadStatus() string {
	b, err := os.ReadFile(p.StatusFile())
	if err != nil {
		return ""
	}
	return string(b)
}

// TailLog returns the last n lines of the current kpm.log (the rotated
// kpm.log.1 is not read). n <= 0 returns no lines (callers clamp first, G1).
func (p Paths) TailLog(n int) []string {
	if n <= 0 {
		return nil
	}
	b, err := os.ReadFile(p.LogFile())
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
