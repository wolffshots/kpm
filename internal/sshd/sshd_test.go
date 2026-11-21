package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Unit tests of SSH.md section 9 step 1. The pid lock test and the in-process
// client test are Linux only (pidlock_linux_test.go, server_linux_test.go).

func newKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, string(ssh.MarshalAuthorizedKey(s.PublicKey()))
}

func TestConfigDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, exists, err := LoadConfig(path)
	if err != nil || exists {
		t.Fatalf("missing file: exists=%v err=%v", exists, err)
	}
	if cfg != (Config{Port: 22, Listen: "0.0.0.0"}) {
		t.Fatalf("default config = %+v", cfg)
	}
	cfg.Enabled, cfg.Port = true, 2222
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	// The hook reads this exact line at boot (hook/src/ssh.cc).
	if !strings.Contains(string(b), "\nenabled = true\n") {
		t.Errorf("config.toml has no line \"enabled = true\":\n%s", b)
	}
	got, exists, err := LoadConfig(path)
	if err != nil || !exists || got != cfg {
		t.Fatalf("round trip: got %+v exists=%v err=%v, want %+v", got, exists, err, cfg)
	}
}

func TestConfigPartialAndBad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("enabled = true\n"), 0o644)
	cfg, _, err := LoadConfig(path)
	if err != nil || cfg != (Config{Enabled: true, Port: 22, Listen: "0.0.0.0"}) {
		t.Fatalf("partial config: %+v %v", cfg, err)
	}
	for _, bad := range []string{"port = 0\n", "port = 70000\n", "listen = \"kobo\"\n", "enabled = \n"} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, _, err := LoadConfig(path); err == nil {
			t.Errorf("config %q: no error", bad)
		}
	}
	if err := SaveConfig(path, Config{Port: 0, Listen: "0.0.0.0"}); err == nil {
		t.Error("SaveConfig accepted port 0")
	}
}

func TestParseAuthorizedKeys(t *testing.T) {
	_, k1 := newKey(t)
	_, k2 := newKey(t)
	data := "# comment\n\n" + k1 + "not a key\n" +
		`from="10.0.0.1" ` + k2 + strings.TrimSpace(k2) + " user@host\n"
	keys, bad := ParseAuthorizedKeys([]byte(data))
	if len(keys) != 2 {
		t.Fatalf("keys = %d, want 2 (k1 and the plain k2 line)", len(keys))
	}
	want := []BadLine{{Line: 4, Reason: "not a public key"}, {Line: 5, Reason: "key options are not supported"}}
	if len(bad) != len(want) {
		t.Fatalf("bad = %+v, want %+v", bad, want)
	}
	for i := range want {
		if bad[i] != want[i] {
			t.Errorf("bad[%d] = %+v, want %+v", i, bad[i], want[i])
		}
	}
}

func TestAddAuthorizedKeysNoDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_keys")
	_, k1 := newKey(t)
	_, k2 := newKey(t)
	os.WriteFile(path, []byte("# header\n"+strings.TrimSpace(k1)), 0o644) // no final newline
	added, bad, err := AddAuthorizedKeys(path, []byte(k1+k2+"junk\n"))
	if err != nil || added != 1 || len(bad) != 1 {
		t.Fatalf("added=%d bad=%v err=%v, want 1 added and 1 bad line", added, bad, err)
	}
	added, _, err = AddAuthorizedKeys(path, []byte(k1+k2))
	if err != nil || added != 0 {
		t.Fatalf("second add: added=%d err=%v, want 0", added, err)
	}
	keys, bad, _ := LoadAuthorizedKeys(path)
	if len(keys) != 2 || len(bad) != 0 {
		t.Fatalf("file has %d keys and %d bad lines, want 2 and 0", len(keys), len(bad))
	}
}

func TestHostKeyCreatedOnceNeverReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh", "ssh_host_ed25519_key")
	s1, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(s1.PublicKey()) != ssh.FingerprintSHA256(s2.PublicKey()) {
		t.Fatal("the second load made a new host key")
	}
	if fp := HostKeyFingerprint(path); fp != ssh.FingerprintSHA256(s1.PublicKey()) {
		t.Fatalf("HostKeyFingerprint = %q", fp)
	}
	os.WriteFile(path, []byte("broken"), 0o600)
	if _, err := LoadOrCreateHostKey(path); err == nil {
		t.Fatal("a broken host key was accepted")
	}
	if b, _ := os.ReadFile(path); string(b) != "broken" {
		t.Fatal("a broken host key was replaced")
	}
}

func TestCheckKey(t *testing.T) {
	dir := t.TempDir()
	signer, line := newKey(t)
	other, _ := newKey(t)
	srv := &Server{Files: Files{Dir: dir}, Log: &Logger{Path: filepath.Join(dir, "sshd.log")}}
	if _, err := srv.checkKey("root", signer.PublicKey(), "10.0.0.2"); err == nil {
		t.Fatal("a key passed with no authorized_keys file")
	}
	os.WriteFile(srv.Files.AuthorizedKeys(), []byte("bad line\n"+line), 0o644)

	perms, err := srv.checkKey("root", signer.PublicKey(), "10.0.0.2")
	if err != nil {
		t.Fatalf("known key refused: %v", err)
	}
	if perms.Extensions[extFingerprint] != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Errorf("permissions carry fingerprint %q", perms.Extensions[extFingerprint])
	}
	if _, err := srv.checkKey("root", other.PublicKey(), "10.0.0.2"); err == nil {
		t.Error("unknown key passed")
	}
	if _, err := srv.checkKey("admin", signer.PublicKey(), "10.0.0.2"); err == nil {
		t.Error("user admin passed")
	}

	// The file is read again at each login: a new key works without a restart.
	os.WriteFile(srv.Files.AuthorizedKeys(), []byte(string(ssh.MarshalAuthorizedKey(other.PublicKey()))), 0o644)
	if _, err := srv.checkKey("root", other.PublicKey(), "10.0.0.2"); err != nil {
		t.Errorf("new key refused: %v", err)
	}

	log, _ := os.ReadFile(srv.Log.Path)
	for _, want := range []string{
		"authorized_keys line 1 skipped: not a public key",
		"refused key " + ssh.FingerprintSHA256(other.PublicKey()) + " for root from 10.0.0.2",
		`refused user "admin" from 10.0.0.2`,
	} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log has no %q:\n%s", want, log)
		}
	}
	if n := strings.Count(string(log), "line 1 skipped"); n != 1 {
		t.Errorf("bad line logged %d times, want once for one version of the file", n)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	l := newLimiter(func() time.Time { return now })
	for i := 1; i < FailLimit; i++ {
		if l.fail("10.0.0.9") {
			t.Fatalf("failure %d started a refusal", i)
		}
		now = now.Add(10 * time.Second)
	}
	if l.isBlocked("10.0.0.9") {
		t.Fatal("blocked before the 5th failure")
	}
	if !l.fail("10.0.0.9") {
		t.Fatal("5th failure in one minute did not start a refusal")
	}
	if !l.isBlocked("10.0.0.9") || l.isBlocked("10.0.0.10") {
		t.Fatal("wrong address refused")
	}
	now = now.Add(BlockTime - time.Second)
	if !l.isBlocked("10.0.0.9") {
		t.Fatal("refusal ended early")
	}
	now = now.Add(2 * time.Second)
	if l.isBlocked("10.0.0.9") {
		t.Fatal("refusal did not end after 5 minutes")
	}

	// Failures spread over more than one minute never block.
	for i := 0; i < 3*FailLimit; i++ {
		if l.fail("10.0.0.11") {
			t.Fatalf("slow failure %d started a refusal", i)
		}
		now = now.Add(16 * time.Second)
	}
}

func TestLogRotateAndLastError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshd.log")
	l := &Logger{Path: path}
	if LastError(path) != "" {
		t.Fatal("LastError of a missing file")
	}
	l.Printf(LevelError, "listen on 0.0.0.0:22: %s", "address already in use")
	l.Printf(LevelInfo, "login root")
	if got := LastError(path); got != "listen on 0.0.0.0:22: address already in use" {
		t.Fatalf("LastError = %q", got)
	}
	big := strings.Repeat("x", 1000)
	for i := 0; i < 300; i++ {
		l.Printf(LevelInfo, "%s", big)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > LogMaxBytes {
		t.Fatalf("log size %v err %v, want at most %d", info.Size(), err, LogMaxBytes)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no old log file: %v", err)
	}
}
