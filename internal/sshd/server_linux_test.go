package sshd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// The in-process test of SSH.md section 9 step 2: a real server on 127.0.0.1
// with a random port and a temp folder, and an x/crypto/ssh client.

type testServer struct {
	srv    *Server
	addr   string
	cancel context.CancelFunc
	done   chan error
	dir    string
}

func startTestServer(t *testing.T, authorized string) *testServer {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	f := Files{Dir: dir}
	if err := os.WriteFile(f.AuthorizedKeys(), []byte(authorized), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Files: f, Log: &Logger{Path: f.LogFile()}, Home: dir}
	ctx, cancel := context.WithCancel(context.Background())
	ts := &testServer{srv: srv, addr: ln.Addr().String(), cancel: cancel, done: make(chan error, 1), dir: dir}
	go func() { ts.done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ts.done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after cancel")
		}
	})
	return ts
}

func (ts *testServer) clientConfig(auth ...ssh.AuthMethod) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User: "root",
		Auth: auth,
		HostKeyCallback: func(host string, remote net.Addr, key ssh.PublicKey) error {
			if fp := ssh.FingerprintSHA256(key); fp != HostKeyFingerprint(ts.srv.Files.HostKey()) {
				return fmt.Errorf("host key %s is not the key of the host key file", fp)
			}
			return nil
		},
		Timeout: 5 * time.Second,
	}
}

func (ts *testServer) dial(t *testing.T, signer ssh.Signer) *ssh.Client {
	t.Helper()
	c, err := ssh.Dial("tcp", ts.addr, ts.clientConfig(ssh.PublicKeys(signer)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestServerInProcess(t *testing.T) {
	signer, line := newKey(t)
	ts := startTestServer(t, line)
	c := ts.dial(t, signer)

	t.Run("exec", func(t *testing.T) {
		s, _ := c.NewSession()
		defer s.Close()
		out, err := s.Output("echo ok")
		if err != nil || string(out) != "ok\n" {
			t.Fatalf("echo ok: %q %v", out, err)
		}
	})

	t.Run("exit status", func(t *testing.T) {
		s, _ := c.NewSession()
		defer s.Close()
		err := s.Run("exit 3")
		var ee *ssh.ExitError
		if !errors.As(err, &ee) || ee.ExitStatus() != 3 {
			t.Fatalf("exit 3: %v", err)
		}
	})

	t.Run("stderr", func(t *testing.T) {
		s, _ := c.NewSession()
		defer s.Close()
		var stdout, stderr bytes.Buffer
		s.Stdout, s.Stderr = &stdout, &stderr
		if err := s.Run("echo out; echo err >&2"); err != nil {
			t.Fatal(err)
		}
		if stdout.String() != "out\n" || stderr.String() != "err\n" {
			t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
		}
	})

	t.Run("pty shell", func(t *testing.T) {
		s, _ := c.NewSession()
		defer s.Close()
		if err := s.RequestPty("xterm", 40, 100, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
			t.Fatalf("pty-req: %v", err)
		}
		var out bytes.Buffer
		s.Stdout = &out
		s.Stdin = strings.NewReader("tty\necho cols=$(stty size)\nexit\n")
		if err := s.Shell(); err != nil {
			t.Fatal(err)
		}
		if err := s.Wait(); err != nil {
			t.Fatalf("shell: %v (output %q)", err, out.String())
		}
		if !strings.Contains(out.String(), "/dev/pts/") {
			t.Fatalf("tty did not print a PTY: %q", out.String())
		}
		if !strings.Contains(out.String(), "cols=40 100") {
			t.Errorf("window size not applied: %q", out.String())
		}
	})

	t.Run("sftp", func(t *testing.T) {
		cl, err := sftp.NewClient(c)
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		remote := filepath.Join(ts.dir, "put.txt")
		w, err := cl.Create(remote)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, "hello kobo\n")
		w.Close()
		if b, _ := os.ReadFile(remote); string(b) != "hello kobo\n" {
			t.Fatalf("put: file has %q", b)
		}
		os.WriteFile(filepath.Join(ts.dir, "get.txt"), []byte("from device\n"), 0o644)
		r, err := cl.Open(filepath.Join(ts.dir, "get.txt"))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		r.Close()
		if string(b) != "from device\n" {
			t.Fatalf("get: %q", b)
		}
	})

	t.Run("port forward refused", func(t *testing.T) {
		if conn, err := c.Dial("tcp", ts.addr); err == nil {
			conn.Close()
			t.Fatal("direct-tcpip channel accepted")
		}
		if l, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
			l.Close()
			t.Fatal("tcpip-forward accepted")
		}
	})

	t.Run("password refused", func(t *testing.T) {
		cfg := ts.clientConfig(ssh.Password("root"))
		if c2, err := ssh.Dial("tcp", ts.addr, cfg); err == nil {
			c2.Close()
			t.Fatal("password login passed")
		}
	})

	t.Run("wrong user refused", func(t *testing.T) {
		cfg := ts.clientConfig(ssh.PublicKeys(signer))
		cfg.User = "admin"
		if c2, err := ssh.Dial("tcp", ts.addr, cfg); err == nil {
			c2.Close()
			t.Fatal("user admin passed")
		}
	})

	log, _ := os.ReadFile(ts.srv.Files.LogFile())
	if !strings.Contains(string(log), "login root from 127.0.0.1 key "+ssh.FingerprintSHA256(signer.PublicKey())) {
		t.Errorf("no login line in the log:\n%s", log)
	}
}

// After 5 failed logins in one minute, the address is refused, also with a
// good key.
func TestServerRefusesAddress(t *testing.T) {
	good, line := newKey(t)
	bad, _ := newKey(t)
	ts := startTestServer(t, line)
	for i := 0; i < FailLimit; i++ {
		if c, err := ssh.Dial("tcp", ts.addr, ts.clientConfig(ssh.PublicKeys(bad))); err == nil {
			c.Close()
			t.Fatal("unknown key passed")
		}
	}
	// The client sees the failure before the server goroutine records it.
	for deadline := time.Now().Add(2 * time.Second); !ts.srv.limiter.isBlocked("127.0.0.1"); {
		if time.Now().After(deadline) {
			t.Fatal("the address was not refused after 5 failed logins")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c, err := ssh.Dial("tcp", ts.addr, ts.clientConfig(ssh.PublicKeys(good))); err == nil {
		c.Close()
		t.Fatal("a refused address logged in")
	}
	log, _ := os.ReadFile(ts.srv.Files.LogFile())
	if !strings.Contains(string(log), "refused 127.0.0.1: too many failed logins") {
		t.Errorf("no refusal line in the log:\n%s", log)
	}
}

// A client that offers other keys before the right one is not counted as a
// failed login.
func TestServerAgentKeysNotCounted(t *testing.T) {
	good, line := newKey(t)
	ts := startTestServer(t, line)
	var signers []ssh.Signer
	for i := 0; i < 3; i++ {
		s, _ := newKey(t)
		signers = append(signers, s)
	}
	signers = append(signers, good)
	for i := 0; i < FailLimit+1; i++ {
		c, err := ssh.Dial("tcp", ts.addr, ts.clientConfig(ssh.PublicKeys(signers...)))
		if err != nil {
			t.Fatalf("login %d with the good key last: %v", i, err)
		}
		c.Close()
	}
}

// On shutdown the server ends open sessions and returns within the wait.
func TestServerShutdown(t *testing.T) {
	signer, line := newKey(t)
	ts := startTestServer(t, line)
	c := ts.dial(t, signer)
	s, _ := c.NewSession()
	if err := s.Start("sleep 30"); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- s.Wait() }()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	ts.cancel()
	select {
	case <-ts.done:
	case <-time.After(shutdownWait + 2*time.Second):
		t.Fatal("Serve did not return")
	}
	if d := time.Since(start); d > shutdownWait+time.Second {
		t.Errorf("shutdown took %s", d)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not end")
	}
	ts.done <- nil // for the cleanup
}
