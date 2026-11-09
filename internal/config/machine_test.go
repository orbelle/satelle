package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The machine id differs per home and per host: the same home file under two
// host ids gives two ids, two homes on one host give two ids, and reading
// never creates the file (sto_e127b2e6 AC4).
func TestMachineIDDiffersPerHomeAndPerHost(t *testing.T) {
	hosts := t.TempDir()
	host1, host2 := filepath.Join(hosts, "host1"), filepath.Join(hosts, "host2")
	if err := os.WriteFile(host1, []byte("11111111111111111111111111111111\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(host2, []byte("22222222222222222222222222222222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := hostMachineIDFile
	t.Cleanup(func() { hostMachineIDFile = old })
	hostMachineIDFile = host1

	homeA, homeB, homeC := t.TempDir(), t.TempDir(), t.TempDir()
	if _, ok := ReadMachineID(homeA); ok {
		t.Fatal("ReadMachineID found an id before one was created")
	}
	if _, err := os.Stat(filepath.Join(homeA, MachineIDFile)); !os.IsNotExist(err) {
		t.Fatalf("ReadMachineID created the file: %v", err)
	}

	a1, err := MachineID(homeA)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := MachineID(homeA)
	if err != nil {
		t.Fatal(err)
	}
	if a1 == "" || a1 != a2 {
		t.Fatalf("one home and host gave %q then %q", a1, a2)
	}
	if r, ok := ReadMachineID(homeA); !ok || r != a1 {
		t.Fatalf("ReadMachineID = %q %v, want %q", r, ok, a1)
	}

	// Per host: home A's file copied into home B, under two host ids.
	b, err := os.ReadFile(filepath.Join(homeA, MachineIDFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeB, MachineIDFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	onHost1, _ := MachineID(homeB)
	hostMachineIDFile = host2
	onHost2, _ := MachineID(homeB)
	if onHost1 != a1 || onHost1 == onHost2 {
		t.Fatalf("the same home file under two hosts gave %q and %q (home A %q)", onHost1, onHost2, a1)
	}

	// Per home: two independently created homes on one host.
	hostMachineIDFile = host1
	c, err := MachineID(homeC)
	if err != nil {
		t.Fatal(err)
	}
	if c == a1 {
		t.Fatalf("two homes on one host share the id %q", c)
	}

	// No readable host file: the id derives from the home file alone.
	hostMachineIDFile = filepath.Join(hosts, "absent")
	if id, err := MachineID(homeA); err != nil || id == "" || id == a1 {
		t.Fatalf("without a host file: %q %v", id, err)
	}
}
