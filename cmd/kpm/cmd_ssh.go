package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"kpm/internal/device"
	"kpm/internal/sshd"
	"kpm/internal/version"
)

// cmd_ssh.go implements `kpm ssh` (kpm-archive/docs/SSH.md section 4). It needs
// no state.json and no kpm lock, so main dispatches it before newApp, as it
// does `kpm ui`. The long-running `serve` must never hold the kpm lock.

const sshUsage = `usage: kpm ssh <command>
  install [--keys <file>] [--port N]   make the ssh folder, host key and config
  enable [--port N]                    install if needed, turn on at boot, start
  disable                              turn off at boot, stop
  start | stop | restart               control the server now
  status [--json]                      show the server state
`

// sshAddrs lists the IPv4 addresses of the device. Tests replace it.
var sshAddrs = deviceAddrs

// Times of start and stop.
var (
	sshStartWait = 3 * time.Second
	sshStopWait  = 4 * time.Second
)

func cmdSSH(args []string) int {
	if runtime.GOOS != "linux" && os.Getenv("KPM_ROOT") == "" {
		fmt.Fprintln(os.Stderr, "kpm ssh: set KPM_ROOT when running off-device")
		return exitError
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, sshUsage)
		return exitError
	}
	f := sshd.Files{Dir: device.New().SSHDir()}
	sub, rest := args[0], args[1:]
	flags, pos := splitArgs(rest, map[string]bool{"keys": true, "port": true})
	flags, jsonMode := takeJSON(flags)
	if len(pos) > 0 || (jsonMode && sub != "status") {
		fmt.Fprint(os.Stderr, sshUsage)
		return exitError
	}
	fs := flag.NewFlagSet("ssh "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var keys *string
	var port *int
	switch sub {
	case "install":
		keys = fs.String("keys", "", "")
		port = fs.Int("port", 0, "")
	case "enable":
		port = fs.Int("port", 0, "")
	}
	if err := fs.Parse(flags); err != nil {
		fmt.Fprintf(os.Stderr, "kpm ssh %s: %v\n%s", sub, err, sshUsage)
		return exitError
	}
	portVal := 0
	if port != nil {
		portVal = *port
		if portVal < 0 || portVal > 65535 {
			fmt.Fprintf(os.Stderr, "kpm ssh %s: port %d is not in 1-65535\n", sub, portVal)
			return exitError
		}
	}

	switch sub {
	case "install":
		return sshInstallCmd(f, *keys, portVal)
	case "enable":
		return sshEnable(f, portVal)
	case "disable":
		return sshDisable(f)
	case "start":
		return sshStart(f)
	case "stop":
		return sshStop(f)
	case "restart":
		if code := sshStop(f); code != exitOK {
			return code
		}
		return sshStart(f)
	case "status":
		return sshStatusCmd(f, jsonMode)
	case "serve":
		return sshServe(f)
	}
	fmt.Fprintf(os.Stderr, "kpm ssh: unknown command %q\n%s", sub, sshUsage)
	return exitError
}

// sshInstall makes the folder, the host key, config.toml and an empty
// authorized_keys. It is safe to run again: it keeps each file that exists.
// port 0 keeps the port of the config.
func sshInstall(f sshd.Files, keysFile string, port int) error {
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		return err
	}
	if _, err := sshd.LoadOrCreateHostKey(f.HostKey()); err != nil {
		return err
	}
	cfg, exists, err := sshd.LoadConfig(f.Config())
	if err != nil {
		return err
	}
	if port != 0 && port != cfg.Port {
		cfg.Port = port
		exists = false // write the change
	}
	if !exists {
		if err := sshd.SaveConfig(f.Config(), cfg); err != nil {
			return err
		}
	}
	if _, err := os.Stat(f.AuthorizedKeys()); errors.Is(err, os.ErrNotExist) {
		header := "# kpm ssh: one OpenSSH public key per line, for example the content of id_ed25519.pub.\n"
		if err := device.WriteFileAtomic(f.AuthorizedKeys(), []byte(header)); err != nil {
			return err
		}
	}
	if keysFile == "" {
		return nil
	}
	src, err := os.ReadFile(keysFile)
	if err != nil {
		return err
	}
	added, bad, err := sshd.AddAuthorizedKeys(f.AuthorizedKeys(), src)
	for _, b := range bad {
		fmt.Printf("%s line %d skipped: %s\n", keysFile, b.Line, b.Reason)
	}
	if err != nil {
		return err
	}
	fmt.Printf("added %d key(s) to %s\n", added, f.AuthorizedKeys())
	return nil
}

func sshInstallCmd(f sshd.Files, keysFile string, port int) int {
	if err := sshInstall(f, keysFile, port); err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh install:", err)
		return exitError
	}
	cfg, _, _ := sshd.LoadConfig(f.Config())
	keys, _, _ := sshd.LoadAuthorizedKeys(f.AuthorizedKeys())
	fmt.Printf("ssh installed in %s\nhost key: %s\nport: %d\nkeys: %d\n", f.Dir, sshd.HostKeyFingerprint(f.HostKey()), cfg.Port, len(keys))
	if len(keys) == 0 {
		fmt.Printf("Add your public key to %s, then run: kpm ssh enable\n", f.AuthorizedKeys())
	}
	return exitOK
}

// requireKeys refuses when authorized_keys has no valid key (SSH.md section 4).
func requireKeys(f sshd.Files, cmd string) bool {
	keys, _, err := sshd.LoadAuthorizedKeys(f.AuthorizedKeys())
	if err != nil {
		fmt.Fprintf(os.Stderr, "kpm ssh %s: %v\n", cmd, err)
		return false
	}
	if len(keys) == 0 {
		fmt.Fprintf(os.Stderr, "kpm ssh %s: no valid public key in %s; add your key there first\n", cmd, f.AuthorizedKeys())
		return false
	}
	return true
}

// sshEnable turns the server on at boot and starts it. It refuses before it
// changes the config when there is no valid key, so "enabled" never names a
// server that cannot start.
func sshEnable(f sshd.Files, port int) int {
	if err := sshInstall(f, "", port); err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh enable:", err)
		return exitError
	}
	if !requireKeys(f, "enable") {
		return exitError
	}
	cfg, _, err := sshd.LoadConfig(f.Config())
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh enable:", err)
		return exitError
	}
	if !cfg.Enabled {
		cfg.Enabled = true
		if err := sshd.SaveConfig(f.Config(), cfg); err != nil {
			fmt.Fprintln(os.Stderr, "kpm ssh enable:", err)
			return exitError
		}
	}
	fmt.Println("ssh enabled: it starts at boot")
	// A new port applies only after a restart of a running server.
	if _, running := sshd.ProbePid(f.PidFile()); running && port != 0 {
		if code := sshStop(f); code != exitOK {
			return code
		}
	}
	return sshStart(f)
}

func sshDisable(f sshd.Files) int {
	cfg, exists, err := sshd.LoadConfig(f.Config())
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh disable:", err)
		return exitError
	}
	if exists && cfg.Enabled {
		cfg.Enabled = false
		if err := sshd.SaveConfig(f.Config(), cfg); err != nil {
			fmt.Fprintln(os.Stderr, "kpm ssh disable:", err)
			return exitError
		}
	}
	fmt.Println("ssh disabled: it does not start at boot")
	return sshStop(f)
}

// sshStart starts `kpm ssh serve` in the background (SSH.md section 5). It
// does not change "enabled".
func sshStart(f sshd.Files) int {
	cfg, _, err := sshd.LoadConfig(f.Config())
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh start:", err)
		return exitError
	}
	if pid, running := sshd.ProbePid(f.PidFile()); running {
		fmt.Printf("ssh server already running (pid %d)\n", pid)
		return exitOK
	}
	if !requireKeys(f, "start") {
		return exitError
	}
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh start:", err)
		return exitError
	}
	if _, err := sshd.LoadOrCreateHostKey(f.HostKey()); err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh start:", err)
		return exitError
	}
	// The port check names the port. NiLuJe Dropbear holds port 22 on a device
	// with usbnet (SSH.md section 4).
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		fmt.Fprintf(os.Stderr, "kpm ssh start: port %d is in use or not available (%v)\n", cfg.Port, err)
		return exitError
	}
	ln.Close()

	exited, err := spawnServe(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh start:", err)
		return exitError
	}
	deadline := time.Now().Add(sshStartWait)
	for time.Now().Before(deadline) {
		if pid, running := sshd.ProbePid(f.PidFile()); running {
			fmt.Printf("ssh server started (pid %d), port %d\n", pid, cfg.Port)
			return exitOK
		}
		select {
		case <-exited:
			msg := sshd.LastError(f.LogFile())
			if msg == "" {
				msg = "see " + f.LogFile()
			}
			fmt.Fprintln(os.Stderr, "kpm ssh start: the server stopped:", msg)
			return exitError
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Fprintln(os.Stderr, "kpm ssh start: the server did not start in time; see", f.LogFile())
	return exitError
}

// sshEnv is the clean environment of `kpm ssh serve` (SSH.md section 5): no
// LD_LIBRARY_PATH or other values from Nickel. KPM_ROOT and KPM_SYSROOT pass,
// so an off-device sandbox finds its folder.
func sshEnv() []string {
	env := []string{"PATH=" + sshd.DefaultPath, "HOME=/"}
	for _, k := range []string{"TERM", "KPM_ROOT", "KPM_SYSROOT"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// sshStop sends SIGTERM and waits for the server to end. It does not change
// "enabled".
func sshStop(f sshd.Files) int {
	pid, running := sshd.ProbePid(f.PidFile())
	if !running {
		fmt.Println("ssh server not running")
		return exitOK
	}
	if err := signalPid(pid, syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "kpm ssh stop: pid %d: %v\n", pid, err)
		return exitError
	}
	deadline := time.Now().Add(sshStopWait)
	for time.Now().Before(deadline) {
		if _, running := sshd.ProbePid(f.PidFile()); !running {
			fmt.Println("ssh server stopped")
			return exitOK
		}
		time.Sleep(100 * time.Millisecond)
	}
	signalPid(pid, syscall.SIGKILL)
	os.Remove(f.PidFile())
	fmt.Println("ssh server killed: it did not stop in time")
	return exitOK
}

// sshServe runs the server in the foreground. `kpm ssh start` runs it.
func sshServe(f sshd.Files) int {
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "kpm ssh serve:", err)
		return exitError
	}
	logger := &sshd.Logger{Path: f.LogFile()}
	lock, err := sshd.LockPid(f.PidFile())
	if errors.Is(err, sshd.ErrRunning) {
		fmt.Fprintln(os.Stderr, "kpm ssh serve:", err)
		return exitOK
	}
	if err != nil {
		logger.Printf(sshd.LevelError, "pid file %s: %v", f.PidFile(), err)
		fmt.Fprintln(os.Stderr, "kpm ssh serve:", err)
		return exitError
	}
	defer lock.Release()

	cfg, _, err := sshd.LoadConfig(f.Config())
	if err != nil {
		logger.Printf(sshd.LevelError, "%v", err)
		return exitError
	}
	if os.Getenv("KPM_ROOT") == "" {
		if note := sshd.EnsureDevpts(); note != "" {
			logger.Printf(sshd.LevelInfo, "%s", note)
		}
	}
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		logger.Printf(sshd.LevelError, "listen on %s: %v", cfg.Addr(), err)
		return exitError
	}
	srv := &sshd.Server{Files: f, Log: logger, Env: []string{"PATH=" + sshd.DefaultPath}}
	if _, bad, err := sshd.LoadAuthorizedKeys(f.AuthorizedKeys()); err == nil && len(bad) > 0 {
		srv.LogBadLines(bad)
	}
	signal.Ignore(syscall.SIGHUP)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	logger.Printf(sshd.LevelInfo, "kpm %s: listening on %s (pid %d)", version.Version, cfg.Addr(), os.Getpid())
	if err := srv.Serve(ctx, ln); err != nil {
		logger.Printf(sshd.LevelError, "serve: %v", err)
		return exitError
	}
	logger.Printf(sshd.LevelInfo, "stopped")
	return exitOK
}

// jsonSSHStatus is the `kpm ssh status --json` payload (JSON-OUTPUT.md section
// 2.7).
type jsonSSHStatus struct {
	Installed   bool     `json:"installed"`
	Enabled     bool     `json:"enabled"`
	Running     bool     `json:"running"`
	Pid         *int     `json:"pid"`
	Port        int      `json:"port"`
	Listen      string   `json:"listen"`
	User        string   `json:"user"`
	Addresses   []string `json:"addresses"`
	HostKey     *string  `json:"host_key"`
	Keys        int      `json:"keys"`
	BadKeyLines int      `json:"bad_key_lines"`
	LastError   *string  `json:"last_error"`
}

// sshStatus collects the state of the server. It reads files only.
func sshStatus(f sshd.Files) (jsonSSHStatus, error) {
	cfg, exists, err := sshd.LoadConfig(f.Config())
	if err != nil {
		return jsonSSHStatus{}, err
	}
	keys, bad, err := sshd.LoadAuthorizedKeys(f.AuthorizedKeys())
	if err != nil {
		return jsonSSHStatus{}, err
	}
	st := jsonSSHStatus{
		Installed:   exists,
		Enabled:     cfg.Enabled,
		Port:        cfg.Port,
		Listen:      cfg.Listen,
		User:        "root",
		Addresses:   sshAddrs(),
		HostKey:     ptr(sshd.HostKeyFingerprint(f.HostKey())),
		Keys:        len(keys),
		BadKeyLines: len(bad),
		LastError:   ptr(sshd.LastError(f.LogFile())),
	}
	if st.Addresses == nil {
		st.Addresses = []string{}
	}
	if pid, running := sshd.ProbePid(f.PidFile()); running {
		st.Running = true
		st.Pid = &pid
	}
	return st, nil
}

func sshStatusCmd(f sshd.Files, jsonMode bool) int {
	st, err := sshStatus(f)
	if err != nil {
		if jsonMode {
			jsonError(err.Error())
		}
		fmt.Fprintln(os.Stderr, "kpm ssh status:", err)
		return exitError
	}
	onOff := map[bool]string{true: "enabled", false: "disabled"}[st.Enabled]
	run := "stopped"
	if st.Running {
		run = fmt.Sprintf("running (pid %d)", *st.Pid)
	}
	if !st.Installed {
		onOff = "not installed"
	}
	fmt.Printf("ssh:       %s, %s\n", onOff, run)
	fmt.Printf("port:      %d (listen %s)\n", st.Port, st.Listen)
	if len(st.Addresses) == 0 {
		fmt.Println("address:   no network address")
	}
	for _, a := range st.Addresses {
		fmt.Printf("address:   %s@%s\n", st.User, a)
	}
	if st.HostKey != nil {
		fmt.Printf("host key:  %s\n", *st.HostKey)
	}
	fmt.Printf("keys:      %d in %s\n", st.Keys, f.AuthorizedKeys())
	if st.BadKeyLines > 0 {
		fmt.Printf("skipped:   %d bad line(s) in authorized_keys\n", st.BadKeyLines)
	}
	if st.LastError != nil {
		fmt.Printf("last error: %s\n", *st.LastError)
	}
	if jsonMode {
		jsonLine(st)
	}
	return exitOK
}

// deviceAddrs returns the IPv4 addresses of the interfaces that are up, except
// loopback.
func deviceAddrs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, n.IP.String())
			}
		}
	}
	sort.Strings(out)
	return out
}

// sshDoctor is the ssh row of `kpm doctor` (SSH.md section 8). It returns
// nil when ssh was never installed.
func sshDoctor(f sshd.Files) *jsonDoctorSSH {
	cfg, exists, err := sshd.LoadConfig(f.Config())
	if !exists {
		return nil
	}
	d := &jsonDoctorSSH{Enabled: cfg.Enabled, Port: cfg.Port}
	if err != nil {
		d.Detail = err.Error()
		return d
	}
	pid, running := sshd.ProbePid(f.PidFile())
	d.Running = running
	d.PidAlive = pid > 0 && sshd.PidAlive(pid)
	host := cfg.Listen
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(cfg.Port)), time.Second); err == nil {
		c.Close()
		d.PortOpen = true
	}
	var problems []string
	switch {
	case cfg.Enabled && !running:
		problems = append(problems, "enabled but not running")
	case running && !d.PidAlive:
		problems = append(problems, "pid file held but the pid is not alive")
	}
	if running && !d.PortOpen {
		problems = append(problems, fmt.Sprintf("port %d does not answer", cfg.Port))
	}
	if !running && d.PortOpen {
		problems = append(problems, fmt.Sprintf("port %d is used by another program", cfg.Port))
	}
	if msg := sshd.LastError(f.LogFile()); msg != "" && len(problems) > 0 {
		problems = append(problems, "last error: "+msg)
	}
	d.Detail = strings.Join(problems, "; ")
	return d
}
