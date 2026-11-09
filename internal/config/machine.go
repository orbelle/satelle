package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MachineIDFile is the file in the satelle home that holds this home's random
// part of the machine id.
const MachineIDFile = "machine-id"

// hostMachineIDFile is the host's own machine id, folded into every id so a
// home copied to another host gets a new one. A variable so tests can point it
// at two different hosts.
var hostMachineIDFile = "/etc/machine-id"

// MachineID is this machine's id for seats: the first 16 hex characters of
// sha256(<home>/machine-id, NUL, the host's /etc/machine-id when readable). It
// differs per home (the home file is random) and per host (the host file
// differs). It creates the home file when absent; a concurrent creator's file
// wins, so every caller derives the same id.
func MachineID(home string) (string, error) {
	if id, ok := ReadMachineID(home); ok {
		return id, nil
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("no satelle home for the machine id")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(home, MachineIDFile+".*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.WriteString(hex.EncodeToString(buf) + "\n")
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		// Link never replaces: when another process created the file first,
		// its content stands.
		if err := os.Link(tmp.Name(), filepath.Join(home, MachineIDFile)); err != nil && !errors.Is(err, os.ErrExist) {
			werr = err
		}
	}
	_ = os.Remove(tmp.Name())
	if werr != nil {
		return "", werr
	}
	if id, ok := ReadMachineID(home); ok {
		return id, nil
	}
	return "", fmt.Errorf("cannot read %s", filepath.Join(home, MachineIDFile))
}

// ReadMachineID derives the machine id as MachineID does without creating
// anything: false when the home file is absent or empty.
func ReadMachineID(home string) (string, bool) {
	if strings.TrimSpace(home) == "" {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(home, MachineIDFile))
	if err != nil {
		return "", false
	}
	part := strings.TrimSpace(string(b))
	if part == "" {
		return "", false
	}
	h := sha256.New()
	h.Write([]byte(part))
	h.Write([]byte{0})
	if host, err := os.ReadFile(hostMachineIDFile); err == nil {
		h.Write([]byte(strings.TrimSpace(string(host))))
	}
	return hex.EncodeToString(h.Sum(nil))[:16], true
}
