package sshd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// LogMaxBytes is the size past which sshd.log moves to sshd.log.1 (SSH.md
// section 3). The move replaces an older sshd.log.1.
const LogMaxBytes = 256 * 1024

// Log levels of sshd.log.
const (
	LevelInfo  = "INFO"
	LevelWarn  = "WARN"
	LevelError = "ERROR"
)

// Logger appends lines to sshd.log. A line is "<UTC time> <LEVEL> <message>".
// It opens the file for each line, as device.Paths.Log does for kpm.log, so a
// rotation never leaves a stale file handle.
type Logger struct {
	Path string
	mu   sync.Mutex
}

// Printf writes one line. A write error is ignored: the log is best-effort and
// must never stop the server.
func (l *Logger) Printf(level, format string, args ...any) {
	if l == nil || l.Path == "" {
		return
	}
	msg := strings.ReplaceAll(fmt.Sprintf(format, args...), "\n", " ")
	line := fmt.Sprintf("%s %s %s\n", time.Now().UTC().Format(time.RFC3339), level, msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	if info, err := os.Stat(l.Path); err == nil && info.Size()+int64(len(line)) > LogMaxBytes {
		os.Rename(l.Path, l.Path+".1")
	}
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	f.WriteString(line)
	f.Close()
}

// LastError returns the message of the last ERROR line of the log at path, or
// "" when there is none.
func LastError(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	last := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	marker := " " + LevelError + " "
	for sc.Scan() {
		if _, msg, ok := strings.Cut(sc.Text(), marker); ok {
			last = msg
		}
	}
	return last
}
