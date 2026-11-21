package sshd

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Server limits and times (SSH.md section 6).
const (
	DefaultMaxConns  = 8
	handshakeTimeout = 30 * time.Second
	shutdownWait     = 2 * time.Second
	extFingerprint   = "kpm-key-fp"
)

// DefaultPath is the PATH of a session.
const DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

var errRefused = errors.New("refused")

// Server is the SSH server of kpm. Set Files and Log, then call Serve.
type Server struct {
	Files    Files
	Log      *Logger
	Shell    string           // the shell of a session, default /bin/sh
	Home     string           // HOME and working folder of a session, default /
	Env      []string         // more environment of a session, for example PATH
	MaxConns int              // open connections, default DefaultMaxConns
	Now      func() time.Time // the clock of the IP refusal, for tests

	limiter *limiter
	signer  ssh.Signer

	mu        sync.Mutex
	closing   bool
	conns     map[*ssh.ServerConn]struct{}
	sessions  map[*session]struct{}
	handlers  sync.WaitGroup
	keysStamp string // mtimes of the key files when their bad lines were last logged
}

func (s *Server) shell() string {
	if s.Shell != "" {
		return s.Shell
	}
	return "/bin/sh"
}

func (s *Server) home() string {
	if s.Home != "" {
		return s.Home
	}
	return "/"
}

// Serve accepts connections on ln until ctx ends. Then it ends each session,
// waits at most 2 seconds, and closes the open connections (SSH.md section 6).
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	signer, err := LoadOrCreateHostKey(s.Files.HostKey())
	if err != nil {
		return err
	}
	s.signer = signer
	s.limiter = newLimiter(s.Now)
	s.mu.Lock()
	s.conns = map[*ssh.ServerConn]struct{}{}
	s.sessions = map[*session]struct{}{}
	s.mu.Unlock()

	max := s.MaxConns
	if max <= 0 {
		max = DefaultMaxConns
	}
	slots := make(chan struct{}, max)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
		}
		ln.Close()
	}()

	var serveErr error
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, net.ErrClosed) {
				serveErr = err
				break
			}
			s.Log.Printf(LevelWarn, "accept: %v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		ip := remoteIP(c.RemoteAddr())
		if s.limiter.isBlocked(ip) {
			s.Log.Printf(LevelWarn, "refused %s: too many failed logins", ip)
			c.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			s.Log.Printf(LevelWarn, "refused %s: %d connections are open", ip, max)
			c.Close()
			continue
		}
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			defer func() { <-slots }()
			s.handleConn(c, ip)
		}()
	}
	s.shutdown()
	return serveErr
}

// shutdown ends each session, waits at most shutdownWait for the handlers, and
// then closes the connections that are still open.
func (s *Server) shutdown() {
	s.mu.Lock()
	s.closing = true
	for sess := range s.sessions {
		sess.end()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.handlers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWait):
	}
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
}

func remoteIP(a net.Addr) string {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return host
}

func (s *Server) handleConn(c net.Conn, ip string) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(handshakeTimeout))
	refused := false
	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-kpm",
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return s.checkKey(meta.User(), key, ip)
		},
		// Each refused method marks the connection. Only a connection that then
		// ends without a login counts as one failed login (limit.go).
		AuthLogCallback: func(meta ssh.ConnMetadata, method string, err error) {
			if err != nil && method != "none" {
				refused = true
			}
		},
	}
	cfg.AddHostKey(s.signer)
	sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		if refused && s.limiter.fail(ip) {
			s.Log.Printf(LevelWarn, "refused %s for %s: %d failed logins in %s", ip, BlockTime, FailLimit, FailWindow)
		}
		return
	}
	c.SetDeadline(time.Time{})
	defer sconn.Close()
	s.Log.Printf(LevelInfo, "login root from %s key %s", ip, sconn.Permissions.Extensions[extFingerprint])
	if !s.trackConn(sconn) {
		return
	}
	defer s.untrackConn(sconn)

	// Global requests are port forwards (tcpip-forward). DiscardRequests
	// refuses each of them.
	go ssh.DiscardRequests(reqs)
	var sessions sync.WaitGroup
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.Prohibited, "only session channels are allowed")
			s.Log.Printf(LevelInfo, "refused %s channel from %s", nc.ChannelType(), ip)
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			s.handleSession(ch, creqs, ip)
		}()
	}
	sessions.Wait()
	s.Log.Printf(LevelInfo, "logout %s", ip)
}

// checkKey is the public key check (SSH.md section 6). It reads both
// authorized_keys files again at each login, so a new key works without a
// restart.
func (s *Server) checkKey(user string, key ssh.PublicKey, ip string) (*ssh.Permissions, error) {
	fp := ssh.FingerprintSHA256(key)
	if user != "root" {
		s.Log.Printf(LevelWarn, "refused user %q from %s key %s: only root can log in", user, ip, fp)
		return nil, errRefused
	}
	keys, err := s.loadKeys()
	if err != nil {
		s.Log.Printf(LevelError, "read keys: %v", err)
	}
	if hasKey(keys, key) {
		return &ssh.Permissions{Extensions: map[string]string{extFingerprint: fp}}, nil
	}
	s.Log.Printf(LevelWarn, "refused key %s for root from %s: not in an authorized_keys file", fp, ip)
	return nil, errRefused
}

// loadKeys reads both key files and logs their bad lines once for each change
// of the files.
func (s *Server) loadKeys() ([]ssh.PublicKey, error) {
	keys, bad, err := s.Files.LoadKeys()
	if len(bad) > 0 {
		s.LogBadLines(bad)
	}
	return keys, err
}

// LogBadLines logs the skipped lines of the key files, once for each version of
// the files.
func (s *Server) LogBadLines(bad []BadLine) {
	stamp := ""
	for _, path := range s.Files.KeyFiles() {
		stamp += fileStamp(path).String() + "|"
	}
	s.mu.Lock()
	seen := stamp == s.keysStamp
	s.keysStamp = stamp
	s.mu.Unlock()
	if seen {
		return
	}
	for _, b := range bad {
		s.Log.Printf(LevelWarn, "%s line %d skipped: %s", b.File, b.Line, b.Reason)
	}
}

func (s *Server) trackConn(c *ssh.ServerConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrackConn(c *ssh.ServerConn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) addSession(sess *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.sessions[sess] = struct{}{}
	return true
}

func (s *Server) removeSession(sess *session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}
