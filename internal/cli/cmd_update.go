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
	"path/filepath"
	"runtime"
	"strconv"
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
	defaultListAPI  = "https://api.github.com/repos/" + releaseRepo + "/releases?per_page=100"
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
binary. A satelled beside it is replaced the same way with the satelled release
of the highest version (serve-v<version> tags, compared numerically), or with
serve-v<version> when --version is given. Every download is checked before
anything is replaced, so a refusal leaves both binaries as they were. A running
satelled keeps the old binary until it is restarted ('satelle service restart').`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), downloadTimeout)
			defer cancel()
			out := cmd.OutOrStdout()
			target, err := updateTarget()
			if err != nil {
				return fmt.Errorf("locate the running binary: %w", err)
			}
			pin := strings.TrimPrefix(strings.TrimSpace(version), "v")
			tag := "v" + pin
			if pin == "" {
				if tag, err = latestTag(ctx); err != nil {
					return err
				}
			}
			// Plan both, fetch every asset that is not current, then replace.
			old := buildinfo.Version
			var cliBin []byte
			if strings.TrimPrefix(tag, "v") != old {
				if cliBin, err = fetchAsset(ctx, tag, buildinfo.CLIName); err != nil {
					return err
				}
			}
			daemonPath := filepath.Join(filepath.Dir(target), buildinfo.DaemonName+exeSuffix())
			var daemonBin []byte
			daemonOld, daemonNew := "", ""
			if _, err := os.Stat(daemonPath); err == nil {
				serveTag := serveTagPrefix + "v" + pin
				if pin == "" {
					if serveTag, err = latestServeTag(ctx); err != nil {
						return err
					}
				}
				daemonNew = strings.TrimPrefix(serveTag, serveTagPrefix+"v")
				daemonOld = daemonVersion(ctx, daemonPath)
				if daemonOld != daemonNew {
					if daemonBin, err = fetchAsset(ctx, serveTag, buildinfo.DaemonName); err != nil {
						if pin != "" {
							return fmt.Errorf("satelled %s has no usable asset for %s/%s (nothing was replaced): %w", serveTag, runtime.GOOS, runtime.GOARCH, err)
						}
						return err
					}
				}
			}
			if cliBin == nil {
				fmt.Fprintf(out, "satelle %s is current\n", old)
			} else {
				if err := replaceFile(target, cliBin); err != nil {
					return err
				}
				fmt.Fprintf(out, "satelle %s → %s\n", old, strings.TrimPrefix(tag, "v"))
			}
			switch {
			case daemonNew == "":
			case daemonBin == nil:
				fmt.Fprintf(out, "satelled %s is current\n", daemonOld)
			default:
				if err := replaceFile(daemonPath, daemonBin); err != nil {
					return err
				}
				fmt.Fprintf(out, "satelled %s → %s (restart it to run the new one: satelle service restart)\n", daemonOld, daemonNew)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "install this version of satelle, and of satelled (serve-v<version>) beside it, instead of the latest")
	return cmd
}

// daemonVersion is the version field of satelled's identity line; "unknown" when it
// cannot say.
func daemonVersion(ctx context.Context, path string) string {
	line, err := daemonIdentity(ctx, path)
	if f := strings.Fields(line); err == nil && len(f) >= 2 {
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
	tags := make([]string, len(rs))
	for i, r := range rs {
		tags[i] = r.Tag
	}
	if tag, ok := newestServeTag(tags); ok {
		return tag, nil
	}
	return "", errors.New("no satelled release found")
}

// newestServeTag is the serve-v tag of the highest version among tags, in
// whatever order they come (GitHub's list is not ordered by version). A tag
// whose version is not dotted integers is never chosen.
func newestServeTag(tags []string) (string, bool) {
	best, bestV := "", []int(nil)
	for _, t := range tags {
		v, ok := strings.CutPrefix(t, serveTagPrefix+"v")
		if !ok {
			continue
		}
		parts, ok := parseReleaseVersion(v)
		if !ok {
			continue
		}
		if bestV == nil || releaseVersionLess(bestV, parts) {
			best, bestV = t, parts
		}
	}
	return best, bestV != nil
}

// parseReleaseVersion splits a dotted version into integers.
func parseReleaseVersion(v string) ([]int, bool) {
	fields := strings.Split(v, ".")
	out := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// releaseVersionLess compares versions field by field; a missing field is 0.
func releaseVersionLess(a, b []int) bool {
	for i := range max(len(a), len(b)) {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// fetchAsset downloads name's asset of tag for this platform and checks its
// sha256; nothing is written.
func fetchAsset(ctx context.Context, tag, name string) ([]byte, error) {
	version := strings.TrimPrefix(tag, serveTagPrefix)
	asset := fmt.Sprintf("%s-%s-%s-%s%s", name, version, runtime.GOOS, runtime.GOARCH, exeSuffix())
	url := strings.TrimRight(envOr(envReleaseBase, defaultBase), "/") + "/" + tag + "/" + asset
	bin, err := releaseGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	sum, err := releaseGet(ctx, url+".sha256")
	if err != nil {
		return nil, fmt.Errorf("download %s.sha256: %w", asset, err)
	}
	f := strings.Fields(string(sum))
	got := sha256.Sum256(bin)
	if len(f) == 0 || !strings.EqualFold(f[0], hex.EncodeToString(got[:])) {
		return nil, fmt.Errorf("%s: checksum mismatch; nothing was replaced", asset)
	}
	return bin, nil
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
