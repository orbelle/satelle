package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoVendorStorageSDK: the CLI binary links no vendor storage SDK; a ledger
// store is an external command (sto_eb93dfd8).
func TestNoVendorStorageSDK(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	deps := strings.Fields(string(out))
	found := false
	for _, d := range deps {
		if d == "github.com/bobmcallan/satelle/internal/ledgerstore" {
			found = true
		}
		for _, vendor := range []string{"github.com/aws/", "github.com/Azure/", "cloud.google.com/go/", "google.golang.org/api/"} {
			if strings.HasPrefix(d, vendor) {
				t.Errorf("satelle links the vendor storage package %s", d)
			}
		}
	}
	if !found {
		t.Errorf("the dependency list does not name internal/ledgerstore; it is not the CLI's list (%d entries)", len(deps))
	}
}
