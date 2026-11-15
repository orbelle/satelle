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

const MachineIDFile = "machine-id"

var hostMachineIDFile = "/etc/machine-id"

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
