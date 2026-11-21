package sshd

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"

	"github.com/BurntSushi/toml"

	"kpm/internal/device"
)

// DefaultPort and DefaultListen are the values of a new config.toml (SSH.md
// section 3). Port 22 is the default because kpm ssh replaces NiLuJe Dropbear.
const (
	DefaultPort   = 22
	DefaultListen = "0.0.0.0"
)

// Config is the content of config.toml.
type Config struct {
	Enabled bool   `toml:"enabled"`
	Port    int    `toml:"port"`
	Listen  string `toml:"listen"`
}

// DefaultConfig is the config of a new install: off, port 22, all addresses.
func DefaultConfig() Config {
	return Config{Port: DefaultPort, Listen: DefaultListen}
}

// Addr is the listen address of the config, for example "0.0.0.0:22".
func (c Config) Addr() string {
	return net.JoinHostPort(c.Listen, fmt.Sprint(c.Port))
}

// Validate checks the port and the listen address.
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d is not in 1-65535", c.Port)
	}
	if net.ParseIP(c.Listen) == nil {
		return fmt.Errorf("listen %q is not an IP address", c.Listen)
	}
	return nil
}

// LoadConfig reads config.toml. A missing file gives the default config and
// exists=false. A missing port or listen key gets its default value.
func LoadConfig(path string) (cfg Config, exists bool, err error) {
	cfg = DefaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return cfg, false, err
	}
	var raw struct {
		Enabled *bool   `toml:"enabled"`
		Port    *int    `toml:"port"`
		Listen  *string `toml:"listen"`
	}
	if _, err := toml.Decode(string(b), &raw); err != nil {
		return cfg, true, fmt.Errorf("%s: %w", path, err)
	}
	if raw.Enabled != nil {
		cfg.Enabled = *raw.Enabled
	}
	if raw.Port != nil {
		cfg.Port = *raw.Port
	}
	if raw.Listen != nil {
		cfg.Listen = *raw.Listen
	}
	if err := cfg.Validate(); err != nil {
		return cfg, true, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, true, nil
}

// SaveConfig writes config.toml atomically. The kpm hook reads the line
// "enabled = true" at boot, so the format is fixed here and not left to an
// encoder.
func SaveConfig(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data := fmt.Sprintf("# kpm ssh settings. See the kpm README, section \"SSH\".\nenabled = %t\nport = %d\nlisten = %q\n",
		cfg.Enabled, cfg.Port, cfg.Listen)
	return device.WriteFileAtomic(path, []byte(data))
}
