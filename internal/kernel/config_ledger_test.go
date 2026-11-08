package kernel

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/ledgerstore"
)

// loadLedger writes satelle.toml with extra appended to a fresh init and loads it.
func loadLedger(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".satelle")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if extra != "" {
		write(t, filepath.Join(root, SettingsFile), "[ledger]\n"+extra+"\n")
	}
	return LoadConfig(root)
}

func TestLedgerStoreLoads(t *testing.T) {
	c, err := loadLedger(t, `store = { command = "satelle-store-azure", args = ["--account", "a", "--container", "c"] }`)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Settings.Ledger.Store
	if s.Command != "satelle-store-azure" || !slices.Equal(s.Args, []string{"--account", "a", "--container", "c"}) || !s.External() {
		t.Errorf("table form: %+v", s)
	}
	for _, kind := range []string{ledgerstore.KindSatelleDev, ledgerstore.KindLocal} {
		c, err := loadLedger(t, `store = "`+kind+`"`)
		if err != nil || c.Settings.Ledger.Store.Kind != kind || c.Settings.Ledger.Store.External() {
			t.Errorf("%s: %+v %v", kind, c.Settings.Ledger.Store, err)
		}
	}
	c, err = loadLedger(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Settings.Ledger.Store; s.Kind != "" || s.Command != "" || s.Args != nil {
		t.Errorf("no [ledger]: %+v, want the zero value", s)
	}
	if _, err := loadLedger(t, "# no store key"); err != nil {
		t.Errorf("[ledger] without store: %v", err)
	}
}

func TestLedgerStoreRefusals(t *testing.T) {
	for extra, want := range map[string]string{
		`store = "s3"`: `"satelle.dev" or "local"`,
		`store = { command = "x", bucket = "b" }`: "unknown key bucket",
		`store = { command = "" }`:                "command is required",
		`other = 1`:                               "unknown key ledger.other",
	} {
		if _, err := loadLedger(t, extra); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", extra, err, want)
		}
	}
}
