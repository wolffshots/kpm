package sshd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"kpm/internal/device"
)

// BadLine is a line of authorized_keys that kpm skipped.
type BadLine struct {
	Line   int // 1-based line number
	Reason string
}

// ParseAuthorizedKeys parses OpenSSH authorized_keys data. Empty lines and
// comment lines are ignored. A line that does not parse is skipped and returned
// in bad. A line with options (for example from= or command=) is also skipped:
// the server cannot apply the options, and a key with a restriction must not
// get more access than its owner gave it.
func ParseAuthorizedKeys(data []byte) (keys []ssh.PublicKey, bad []BadLine) {
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		switch {
		case err != nil:
			bad = append(bad, BadLine{Line: i + 1, Reason: "not a public key"})
		case len(options) > 0:
			bad = append(bad, BadLine{Line: i + 1, Reason: "key options are not supported"})
		case len(bytes.TrimSpace(rest)) > 0:
			bad = append(bad, BadLine{Line: i + 1, Reason: "more than one key on the line"})
		default:
			keys = append(keys, key)
		}
	}
	return keys, bad
}

// LoadAuthorizedKeys reads and parses the authorized_keys file. A missing file
// gives no keys and no error.
func LoadAuthorizedKeys(path string) ([]ssh.PublicKey, []BadLine, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	keys, bad := ParseAuthorizedKeys(b)
	return keys, bad, nil
}

// hasKey reports whether keys has k.
func hasKey(keys []ssh.PublicKey, k ssh.PublicKey) bool {
	want := k.Marshal()
	for _, x := range keys {
		if bytes.Equal(x.Marshal(), want) {
			return true
		}
	}
	return false
}

// AddAuthorizedKeys adds the valid keys of src to the authorized_keys file at
// path. A key that the file already has is not added again. It returns the
// count of added keys and the bad lines of src.
func AddAuthorizedKeys(path string, src []byte) (added int, bad []BadLine, err error) {
	newKeys, bad := ParseAuthorizedKeys(src)
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, bad, err
	}
	have, _ := ParseAuthorizedKeys(old)
	out := old
	if len(out) > 0 && !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	for _, k := range newKeys {
		if hasKey(have, k) {
			continue
		}
		have = append(have, k)
		out = append(out, ssh.MarshalAuthorizedKey(k)...)
		added++
	}
	if added == 0 {
		return 0, bad, nil
	}
	return added, bad, device.WriteFileAtomic(path, out)
}

// LoadOrCreateHostKey reads the host key. If the file does not exist, it makes
// a new ed25519 key and writes it. It never replaces an existing file: a host
// key that does not parse is an error, so the clients of the user do not see a
// changed key.
func LoadOrCreateHostKey(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		s, perr := ssh.ParsePrivateKey(b)
		if perr != nil {
			return nil, fmt.Errorf("host key %s: %w", path, perr)
		}
		return s, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "kpm")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := device.WriteFileAtomic(path, pem.EncodeToMemory(blk)); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

// HostKeyFingerprint returns the SHA256 fingerprint of the host key file, or ""
// when the file is missing or does not parse.
func HostKeyFingerprint(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(s.PublicKey())
}
