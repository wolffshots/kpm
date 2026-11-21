package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"kpm/internal/device"
	"kpm/internal/sshd"
)

// ssh_test.go covers `kpm ssh` (SSH.md section 4 and 9). The server itself is
// tested in internal/sshd. start, stop and serve run only on the device; the
// kfw vm test covers them (SSH.md section 9 step 4).

func sshSandbox(t *testing.T) sshd.Files {
	t.Helper()
	root := t.TempDir()
	t.Setenv("KPM_ROOT", root)
	old := sshAddrs
	sshAddrs = func() []string { return []string{"192.168.88.132"} }
	t.Cleanup(func() { sshAddrs = old })
	return sshd.Files{Dir: device.New().SSHDir()}
}

func pubKeyLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(k))
}

func runSSH(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() { code = run(append([]string{"ssh"}, args...)) })
	})
	return code, stdout, stderr
}

// The status --json golden of a device where ssh was never installed.
func TestSSHStatusJSONNotInstalled(t *testing.T) {
	sshSandbox(t)
	code, out, _ := runSSH(t, "status", "--json")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	want := `{"installed":false,"enabled":false,"running":false,"pid":null,"port":22,"listen":"0.0.0.0","user":"root","addresses":["192.168.88.132"],"host_key":null,"keys":0,"bad_key_lines":0,"last_error":null}`
	if got := lastJSON(t, out); got != want {
		t.Errorf("status --json\n got: %s\nwant: %s", got, want)
	}
}

// The status --json golden after install with one key and one bad line.
func TestSSHStatusJSONGolden(t *testing.T) {
	f := sshSandbox(t)
	keys := filepath.Join(t.TempDir(), "id.pub")
	os.WriteFile(keys, []byte(pubKeyLine(t)), 0o644)
	if code, _, errOut := runSSH(t, "install", "--keys", keys, "--port", "2222"); code != exitOK {
		t.Fatalf("install: exit %d: %s", code, errOut)
	}
	f2, _ := os.OpenFile(f.AuthorizedKeys(), os.O_APPEND|os.O_WRONLY, 0o644)
	f2.WriteString("not a key\n")
	f2.Close()
	(&sshd.Logger{Path: f.LogFile()}).Printf(sshd.LevelError, "listen on 0.0.0.0:2222: address already in use")

	code, out, _ := runSSH(t, "status", "--json")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	fp := sshd.HostKeyFingerprint(f.HostKey())
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("host key fingerprint %q", fp)
	}
	want := `{"installed":true,"enabled":false,"running":false,"pid":null,"port":2222,"listen":"0.0.0.0","user":"root","addresses":["192.168.88.132"],"host_key":"` + fp + `","keys":1,"bad_key_lines":1,"last_error":"listen on 0.0.0.0:2222: address already in use"}`
	if got := lastJSON(t, out); got != want {
		t.Errorf("status --json\n got: %s\nwant: %s", got, want)
	}
}

// CONTRACT: the SSH tab (hook/src/sshpage.cc) reads these fields of
// `kpm ssh status --json`.
func TestUIContractSSHStatus(t *testing.T) {
	sshSandbox(t)
	_, out, _ := runSSH(t, "status", "--json")
	m := decodeJSON(t, lastJSON(t, out))
	wantBool(t, m, "installed")
	wantBool(t, m, "enabled")
	wantBool(t, m, "running")
	wantArray(t, m, "addresses")
	wantStrOrNull(t, m, "host_key")
	wantStrOrNull(t, m, "last_error")
	wantStr(t, m, "user")
	for _, k := range []string{"port", "keys"} {
		if _, ok := m[k].(float64); !ok {
			t.Errorf("%s is %T, want a number", k, m[k])
		}
	}
	if v, ok := m["pid"]; !ok || (v != nil && func() bool { _, n := v.(float64); return !n }()) {
		t.Errorf("pid is %v, want a number or null", v)
	}
}

func TestSSHInstallIsIdempotent(t *testing.T) {
	f := sshSandbox(t)
	keys := filepath.Join(t.TempDir(), "keys")
	os.WriteFile(keys, []byte(pubKeyLine(t)+pubKeyLine(t)), 0o644)
	for i := 0; i < 2; i++ {
		if code, _, errOut := runSSH(t, "install", "--keys", keys); code != exitOK {
			t.Fatalf("install %d: exit %d: %s", i, code, errOut)
		}
	}
	fp := sshd.HostKeyFingerprint(f.HostKey())
	runSSH(t, "install")
	if sshd.HostKeyFingerprint(f.HostKey()) != fp {
		t.Fatal("install replaced the host key")
	}
	k, _, _ := sshd.LoadAuthorizedKeys(f.AuthorizedKeys())
	if len(k) != 2 {
		t.Fatalf("authorized_keys has %d keys, want 2", len(k))
	}
	cfg, exists, _ := sshd.LoadConfig(f.Config())
	if !exists || cfg.Enabled || cfg.Port != 22 {
		t.Fatalf("config %+v exists=%v", cfg, exists)
	}
}

func TestSSHStartRefusesWithoutKey(t *testing.T) {
	f := sshSandbox(t)
	runSSH(t, "install")
	for _, cmd := range []string{"start", "enable"} {
		code, _, errOut := runSSH(t, cmd)
		if code != exitError || !strings.Contains(errOut, f.AuthorizedKeys()) {
			t.Errorf("%s: exit %d, stderr %q; want exit 1 that names authorized_keys", cmd, code, errOut)
		}
	}
	// A refused enable does not turn ssh on at boot.
	if cfg, _, _ := sshd.LoadConfig(f.Config()); cfg.Enabled {
		t.Error("enable without a key set enabled = true")
	}
}

func TestSSHStartRefusesPortInUse(t *testing.T) {
	f := sshSandbox(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	os.MkdirAll(f.Dir, 0o755)
	sshd.SaveConfig(f.Config(), sshd.Config{Port: port, Listen: "127.0.0.1"})
	os.WriteFile(f.AuthorizedKeys(), []byte(pubKeyLine(t)), 0o644)
	code, _, errOut := runSSH(t, "start")
	if code != exitError || !strings.Contains(errOut, "port "+strconv.Itoa(port)+" is in use") {
		t.Errorf("start on a used port: exit %d, stderr %q", code, errOut)
	}
}

func TestSSHUsage(t *testing.T) {
	sshSandbox(t)
	for _, args := range [][]string{{}, {"frobnicate"}, {"start", "--json"}, {"status", "extra"}, {"install", "--port", "70000"}} {
		if code, _, _ := runSSH(t, args...); code != exitError {
			t.Errorf("kpm ssh %v: exit %d, want 1", args, code)
		}
	}
}

// The doctor ssh row (SSH.md section 8): an enabled server that does not run
// is a problem.
func TestDoctorSSHRow(t *testing.T) {
	a := newTestApp(t)
	f := sshd.Files{Dir: a.paths.SSHDir()}
	os.MkdirAll(f.Dir, 0o755)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // a closed port
	sshd.SaveConfig(f.Config(), sshd.Config{Enabled: true, Port: port, Listen: "127.0.0.1"})

	out := captureStdout(t, func() { a.cmdDoctor([]string{"--json"}) })
	m := decodeJSON(t, lastJSON(t, out))
	row, ok := m["ssh"].(map[string]any)
	if !ok {
		t.Fatalf("ssh row is %v", m["ssh"])
	}
	if row["enabled"] != true || row["running"] != false || row["pid_alive"] != false || row["port_open"] != false {
		t.Errorf("ssh row %v", row)
	}
	if d, _ := row["detail"].(string); !strings.Contains(d, "enabled but not running") {
		t.Errorf("detail %q", d)
	}
	human := captureStdout(t, func() { a.cmdDoctor(nil) })
	if !strings.Contains(human, "ssh: enabled, stopped, port "+strconv.Itoa(port)+" closed") {
		t.Errorf("human doctor has no ssh row:\n%s", human)
	}
}
