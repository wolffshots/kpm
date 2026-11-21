package sshd

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// session is one session channel: a shell, an exec command or sftp (SSH.md
// section 6).
type session struct {
	srv *Server
	ch  ssh.Channel
	ip  string

	mu      sync.Mutex
	started bool
	ended   bool
	hasPTY  bool
	term    string
	rows    uint16
	cols    uint16
	ptmx    *os.File
	pid     int // the process of the session, 0 when it is not running
}

func fileStamp(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// clamp16 keeps a window size in the range of a PTY.
func clamp16(v uint32) uint16 {
	if v > 0xffff {
		return 0xffff
	}
	return uint16(v)
}

func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request, ip string) {
	sess := &session{srv: s, ch: ch, ip: ip}
	if !s.addSession(sess) {
		ch.Close()
		return
	}
	defer s.removeSession(sess)
	// The client closed the channel or the connection: end the process too.
	defer sess.end()

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term       string
				Cols, Rows uint32
				W, H       uint32
				Modes      string
			}
			if ssh.Unmarshal(req.Payload, &p) != nil || !sess.setPTY(p.Term, clamp16(p.Rows), clamp16(p.Cols)) {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
		case "window-change":
			var w struct {
				Cols, Rows uint32
				W, H       uint32
			}
			if ssh.Unmarshal(req.Payload, &w) == nil {
				sess.resize(clamp16(w.Rows), clamp16(w.Cols))
			}
			req.Reply(false, nil)
		case "shell":
			sess.startCommand(req, []string{"-l"})
		case "exec":
			var e struct{ Command string }
			if ssh.Unmarshal(req.Payload, &e) != nil {
				req.Reply(false, nil)
				continue
			}
			sess.startCommand(req, []string{"-c", e.Command})
		case "subsystem":
			var sub struct{ Name string }
			if ssh.Unmarshal(req.Payload, &sub) != nil || sub.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			sess.startSFTP(req)
		default:
			// env, x11-req, auth-agent-req@openssh.com, signal and others: refused.
			req.Reply(false, nil)
		}
	}
}

func (sess *session) setPTY(term string, rows, cols uint16) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.started {
		return false
	}
	sess.hasPTY, sess.term, sess.rows, sess.cols = true, term, rows, cols
	return true
}

func (sess *session) resize(rows, cols uint16) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.rows, sess.cols = rows, cols
	if sess.ptmx != nil {
		setPTYSize(sess.ptmx, rows, cols)
	}
}

// end sends SIGHUP to the process group of the session and closes the channel.
// The run goroutine then sends the exit status if the channel is still open.
func (sess *session) end() {
	sess.mu.Lock()
	sess.ended = true
	pid := sess.pid
	sess.mu.Unlock()
	hangup(pid)
	if pid == 0 {
		sess.ch.Close()
	}
}

func (sess *session) env() []string {
	srv := sess.srv
	env := []string{"HOME=" + srv.home(), "USER=root", "LOGNAME=root", "SHELL=" + srv.shell()}
	hasPath := false
	for _, e := range srv.Env {
		if len(e) >= 5 && e[:5] == "PATH=" {
			hasPath = true
		}
	}
	if !hasPath {
		env = append(env, "PATH="+DefaultPath)
	}
	env = append(env, srv.Env...)
	if sess.term != "" {
		env = append(env, "TERM="+sess.term)
	}
	return env
}

// startCommand runs the shell with args, on the PTY when the client asked for
// one. It replies to req before any output goes to the client.
func (sess *session) startCommand(req *ssh.Request, args []string) {
	sess.mu.Lock()
	if sess.started || sess.ended {
		sess.mu.Unlock()
		req.Reply(false, nil)
		return
	}
	sess.started = true
	cmd := exec.Command(sess.srv.shell(), args...)
	cmd.Env = sess.env()
	cmd.Dir = sess.srv.home()

	if sess.hasPTY {
		ptmx, err := startPTY(cmd, sess.rows, sess.cols)
		if err != nil {
			sess.mu.Unlock()
			sess.srv.Log.Printf(LevelError, "pty for %s: %v", sess.ip, err)
			req.Reply(false, nil)
			return
		}
		sess.ptmx = ptmx
		sess.pid = cmd.Process.Pid
		sess.mu.Unlock()
		req.Reply(true, nil)
		go sess.runPTY(cmd, ptmx)
		return
	}

	newProcessGroup(cmd)
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	stderr, err3 := cmd.StderrPipe()
	err := errors.Join(err1, err2, err3)
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		sess.mu.Unlock()
		sess.srv.Log.Printf(LevelError, "start %s for %s: %v", sess.srv.shell(), sess.ip, err)
		req.Reply(false, nil)
		return
	}
	sess.pid = cmd.Process.Pid
	sess.mu.Unlock()
	req.Reply(true, nil)
	go sess.runPipes(cmd, stdin, stdout, stderr)
}

func (sess *session) runPTY(cmd *exec.Cmd, ptmx *os.File) {
	go func() {
		io.Copy(ptmx, sess.ch)
	}()
	out := make(chan struct{})
	go func() {
		// The read ends with EIO when the last process of the PTY closes it.
		io.Copy(sess.ch, ptmx)
		close(out)
	}()
	err := cmd.Wait()
	sess.exited()
	select {
	case <-out:
	case <-time.After(time.Second):
		// A background process still holds the PTY. Do not wait for it.
	}
	sess.mu.Lock()
	sess.ptmx = nil
	sess.mu.Unlock()
	ptmx.Close()
	sess.finish(exitStatus(err))
}

func (sess *session) runPipes(cmd *exec.Cmd, stdin io.WriteCloser, stdout, stderr io.Reader) {
	go func() {
		io.Copy(stdin, sess.ch)
		stdin.Close()
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(sess.ch, stdout) }()
	go func() { defer wg.Done(); io.Copy(sess.ch.Stderr(), stderr) }()
	wg.Wait()
	err := cmd.Wait()
	sess.exited()
	sess.finish(exitStatus(err))
}

// exited marks the process as reaped, so end never signals a reused pid.
func (sess *session) exited() {
	sess.mu.Lock()
	sess.pid = 0
	sess.mu.Unlock()
}

// finish sends the exit status and closes the channel: the end of the session.
func (sess *session) finish(code uint32) {
	sess.ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
	sess.ch.Close()
}

func exitStatus(err error) uint32 {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return uint32(ee.ExitCode())
	}
	return 255
}

// startSFTP runs the sftp subsystem in process (pkg/sftp). OpenSSH scp uses it
// too. The old scp protocol is not supported.
func (sess *session) startSFTP(req *ssh.Request) {
	sess.mu.Lock()
	if sess.started || sess.ended {
		sess.mu.Unlock()
		req.Reply(false, nil)
		return
	}
	sess.started = true
	sess.mu.Unlock()
	srv, err := sftp.NewServer(sess.ch, sftp.WithServerWorkingDirectory(sess.srv.home()))
	if err != nil {
		sess.srv.Log.Printf(LevelError, "sftp for %s: %v", sess.ip, err)
		req.Reply(false, nil)
		return
	}
	req.Reply(true, nil)
	go func() {
		code := uint32(0)
		if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
			code = 1
		}
		sess.finish(code)
		srv.Close()
	}()
}
