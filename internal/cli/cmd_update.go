package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/buildinfo"
)

// Release locations; each can be overridden from the environment (the same
// names scripts/install.sh and scripts/hermetic.sh use).
const (
	envReleaseAPI     = "SATELLE_RELEASE_API"      // JSON of the latest release
	envReleaseListAPI = "SATELLE_RELEASE_LIST_API" // JSON list of releases, newest first
	envReleaseBase    = "SATELLE_RELEASE_BASE"     // <base>/<tag>/<asset>

	releaseRepo     = "bobmcallan/satelle"
	defaultAPI      = "https://api.github.com/repos/" + releaseRepo + "/releases/latest"
	defaultListAPI  = "https://api.github.com/repos/" + releaseRepo + "/releases?per_page=30"
	defaultBase     = "https://github.com/" + releaseRepo + "/releases/download"
	serveTagPrefix  = "serve-" // satelled releases are tagged serve-v<version>
	downloadTimeout = 5 * time.Minute
)

// updateTarget is the binary update replaces; tests point it elsewhere.
var updateTarget = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func updateCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Replace satelle (and satelled beside it) with a GitHub release",
		Long: `update downloads the latest satelle release (or --version) for this OS and
architecture, checks it against the release's .sha256, and replaces the running
binary. A satelled beside it is replaced with the latest satelled release the
same way. A running satelled keeps the old binary until it is restarted
('satelle service restart').`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), downloadTimeout)
			defer cancel()
			out := cmd.OutOrStdout()
			target, err := updateTarget()
			if err != nil {
				return fmt.Errorf("locate the running binary: %w", err)
			}
			tag := "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
			if version == "" {
				if tag, err = latestTag(ctx); err != nil {
					return err
				}
			}
			old := buildinfo.Version
			if strings.TrimPrefix(tag, "v") == old {
				fmt.Fprintf(out, "satelle %s is current\n", old)
			} else {
				if err := installAsset(ctx, tag, buildinfo.CLIName, target); err != nil {
					return err
				}
				fmt.Fprintf(out, "satelle %s → %s\n", old, strings.TrimPrefix(tag, "v"))
			}
			return updateDaemon(ctx, out, filepath.Join(filepath.Dir(target), buildinfo.DaemonName+exeSuffix()))
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "install this satelle version instead of the latest")
	return cmd
}

// updateDaemon replaces the satelled at path, when there is one, with the
// newest satelled release.
func updateDaemon(ctx context.Context, out io.Writer, path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	tag, err := latestServeTag(ctx)
	if err != nil {
		return err
	}
	newV := strings.TrimPrefix(tag, serveTagPrefix+"v")
	old := daemonVersion(ctx, path)
	if old == newV {
		fmt.Fprintf(out, "satelled %s is current\n", old)
		return nil
	}
	if err := installAsset(ctx, tag, buildinfo.DaemonName, path); err != nil {
		return err
	}
	fmt.Fprintf(out, "satelled %s → %s (restart it to run the new one: satelle service restart)\n", old, newV)
	return nil
}

// daemonVersion asks satelled for its version; "unknown" when it cannot say.
func daemonVersion(ctx context.Context, path string) string {
	b, err := exec.CommandContext(ctx, path, "--version").Output()
	if f := strings.Fields(string(b)); err == nil && len(f) >= 2 {
		return f[1]
	}
	return "unknown"
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func releaseGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "satelle/"+buildinfo.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return body, nil
}

type ghRelease struct {
	Tag string `json:"tag_name"`
}

func latestTag(ctx context.Context) (string, error) {
	body, err := releaseGet(ctx, envOr(envReleaseAPI, defaultAPI))
	if err != nil {
		return "", fmt.Errorf("find the latest release: %w", err)
	}
	var r ghRelease
	if err := json.Unmarshal(body, &r); err != nil || r.Tag == "" {
		return "", fmt.Errorf("find the latest release: no tag_name in the answer")
	}
	return r.Tag, nil
}

func latestServeTag(ctx context.Context) (string, error) {
	body, err := releaseGet(ctx, envOr(envReleaseListAPI, defaultListAPI))
	if err != nil {
		return "", fmt.Errorf("list releases: %w", err)
	}
	var rs []ghRelease
	if err := json.Unmarshal(body, &rs); err != nil {
		return "", fmt.Errorf("list releases: %w", err)
	}
	for _, r := range rs {
		if strings.HasPrefix(r.Tag, serveTagPrefix+"v") {
			return r.Tag, nil
		}
	}
	return "", errors.New("no satelled release found")
}

// installAsset downloads name's asset of tag for this platform, checks its
// sha256, and moves it over path.
func installAsset(ctx context.Context, tag, name, path string) error {
	version := strings.TrimPrefix(tag, serveTagPrefix)
	asset := fmt.Sprintf("%s-%s-%s-%s%s", name, version, runtime.GOOS, runtime.GOARCH, exeSuffix())
	url := strings.TrimRight(envOr(envReleaseBase, defaultBase), "/") + "/" + tag + "/" + asset
	bin, err := releaseGet(ctx, url)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	sum, err := releaseGet(ctx, url+".sha256")
	if err != nil {
		return fmt.Errorf("download %s.sha256: %w", asset, err)
	}
	f := strings.Fields(string(sum))
	got := sha256.Sum256(bin)
	if len(f) == 0 || !strings.EqualFold(f[0], hex.EncodeToString(got[:])) {
		return fmt.Errorf("%s: checksum mismatch; nothing was replaced", asset)
	}
	return replaceFile(path, bin)
}

// replaceFile writes body beside path and renames it into place. Windows
// cannot replace a running executable, so the old one is first moved aside.
func replaceFile(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".new-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := path + ".old"
		_ = os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}
