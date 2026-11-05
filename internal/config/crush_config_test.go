package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadToml writes body as a repo's .satelle/satelle.toml and loads it.
func loadToml(t *testing.T, body string) (Config, error) {
	t.Helper()
	satelleDir := filepath.Join(t.TempDir(), ".satelle")
	if err := os.MkdirAll(satelleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(satelleDir, ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	return cfg, err
}

// TestCrushConfigValidatedAtLoad: nonsensical values refuse the load, naming
// the field.
func TestCrushConfigValidatedAtLoad(t *testing.T) {
	base := "[output.crush]\nenabled = true\nmin_items = 5\nmax_kept = 60\n"
	for name, tc := range map[string]struct{ extra, wantInErr string }{
		"fraction above 1":   {"first_fraction = 1.5\n", "first_fraction"},
		"fraction below 0":   {"rare_status_fraction = -0.1\n", "rare_status_fraction"},
		"first+last above 1": {"first_fraction = 0.7\nlast_fraction = 0.6\n", "first_fraction + last_fraction"},
		"negative max_kept":  {"", "max_kept"},
	} {
		t.Run(name, func(t *testing.T) {
			body := base + tc.extra
			if name == "negative max_kept" {
				body = strings.Replace(base, "max_kept = 60", "max_kept = -1", 1)
			}
			_, err := loadToml(t, body)
			if err == nil || !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("Load error = %v, want one naming %q", err, tc.wantInErr)
			}
		})
	}
}
