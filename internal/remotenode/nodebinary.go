package remotenode

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"strings"

	"github.com/bobmcallan/satelle/internal/remote/provider"
)

const NodeSatellePath = "/usr/local/bin/satelle"

func linuxArch(binary string) (string, error) {
	if binary == "" {
		return "", errors.New("the firing satelle's path is unknown: not a linux binary")
	}
	f, err := elf.Open(binary)
	if err != nil {
		return "", fmt.Errorf("the firing satelle %s is not a linux binary: %v", binary, err)
	}
	defer f.Close()
	if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return "", fmt.Errorf("the firing satelle %s is not a linux binary (%s)", binary, f.OSABI)
	}
	switch f.Machine {
	case elf.EM_X86_64:
		return "amd64", nil
	case elf.EM_AARCH64:
		return "arm64", nil
	}
	return "", fmt.Errorf("the firing satelle %s is a linux binary for %s: no node architecture matches it", binary, f.Machine)
}

func nodeArch(uname string) (string, bool) {
	switch strings.TrimSpace(uname) {
	case "x86_64", "amd64":
		return "amd64", true
	case "aarch64", "arm64":
		return "arm64", true
	}
	return "", false
}

func giveSatelle(ctx context.Context, h provider.Handle, binary, version, arch string) error {
	raw, err := h.Exec(ctx, "uname", "-m")
	if err != nil {
		return fmt.Errorf("the node's architecture is unknown: %v", err)
	}
	uname := strings.TrimSpace(string(raw))
	if got, ok := nodeArch(uname); !ok || got != arch {
		return fmt.Errorf("the node is linux/%s; the firing satelle is linux/%s: no satelle binary for %s", uname, arch, uname)
	}
	if err := h.CopyIn(ctx, binary, NodeSatellePath); err != nil {
		return fmt.Errorf("no writable path: copying satelle to %s failed: %v", NodeSatellePath, err)
	}
	out, err := h.Exec(ctx, "satelle", "--version")
	if got := strings.TrimSpace(string(out)); err != nil || got != strings.TrimSpace(version) {
		if err != nil {
			got = strings.TrimSpace(got + " " + err.Error())
		}
		return fmt.Errorf("the copied satelle does not run as %s in the node: %s", strings.TrimSpace(version), tailOf(got, 500))
	}
	return nil
}
