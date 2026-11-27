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
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
)

const (
	envReleaseAPI     = "SATELLE_RELEASE_API"
	envReleaseListAPI = "SATELLE_RELEASE_LIST_API"
	envReleaseBase    = "SATELLE_RELEASE_BASE"
	envNoService      = "SATELLE_NO_SERVICE"

	releaseRepo     = "bobmcallan/satelle"
	defaultAPI      = "https://api.github.com/repos/" + releaseRepo + "/releases/latest"
	defaultListAPI  = "https://api.github.com/repos/" + releaseRepo + "/releases?per_page=100"
	defaultBase     = "https://github.com/" + releaseRepo + "/releases/download"
	serveTagPrefix  = "serve-"
	downloadTimeout = 5 * time.Minute
)

var updateTarget = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

var updateRefresh = func(ctx context.Context, target string, stdout, stderr io.Writer) error {
	c := exec.CommandContext(ctx, target, "agents", "refresh")
	c.Stdout, c.Stderr = stdout, stderr
	return c.Run()
}

func hookFailure(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("the binaries are updated, but the harness files are not refreshed: %w; run 'satelle agents refresh'", err)
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func serviceOptOut(flag bool) (skip bool, why string) {
	if flag {
		return true, "--no-service"
	}
	if v := strings.TrimSpace(os.Getenv(envNoService)); v != "" && v != "0" {
		return true, envNoService + "=" + v
	}
	return false, ""
}

func updateCmd() *cobra.Command {
	var version string
	var noService bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Replace satelle (and satelled beside it) with a GitHub release",
		Long: `update downloads the latest satelle release (or --version) for this OS and
architecture, checks it against the release's .sha256, and replaces the running
binary. A satelled beside it is replaced the same way with the satelled release
of the highest version (serve-v<version> tags, compared numerically), or with
serve-v<version> when --version is given. Every download is checked before
anything is replaced, so a refusal leaves both binaries as they were.

Then update refreshes the service as 'satelle service refresh' does: it
installs a missing unit, rewrites a unit that differs from what satelle would
write (one naming another binary among them), and restarts satelled or
satelle-follow.service only when its unit was rewritten or it does not run the
installed binary, so a satelled and follow already running the current build
keep running; it starts the sync timer and prints 'satelle service status';
with no systemd user manager it prints the units and the autosync command. On macOS it does the same with the dev.satelle.satelled
and dev.satelle.sync launch agents. --no-service or SATELLE_NO_SERVICE=1 skips every unit
change; a running satelled then keeps the old build until
'satelle service restart'.

Before the service step, whatever --no-service says, update refreshes the
harness hook files of every registered project (satelle agents refresh) with
the installed binary: each harness in use gets satelle's current hooks, and a
hook naming a satelle command the binary lacks is removed. It names each file
git tracks and each repo whose .satelle/satelle.toml sets [hook] refresh =
false, and leaves them. A refresh that does not finish (a --version pin to a
build without agents refresh among the causes) makes update exit 1. The same
refresh brings satelle's herdr panel up to date where 'satelle herdr install'
put it; a herdr outcome never fails update.

Last, whether or not anything was replaced, update lists each registered
project whose .satelle is pre-1.0, with 'satelle upgrade --archive' to run in
it: this satelle cannot read them.`,
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
			old := buildinfo.Version
			var cliBin []byte
			if strings.TrimPrefix(tag, "v") != old {
				if cliBin, err = fetchAsset(ctx, tag, buildinfo.CLIName); err != nil {
					return err
				}
			}
			daemonPath := filepath.Join(filepath.Dir(target), buildinfo.DaemonName+buildinfo.ExeSuffix())
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
				buildinfo.Identify(ctx, config.GlobalDir(), target)
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
				buildinfo.Identify(ctx, config.GlobalDir(), daemonPath)
				fmt.Fprintf(out, "satelled %s → %s\n", daemonOld, daemonNew)
			}
			var hookErr error
			if cliBin != nil {
				hookErr = hookFailure(updateRefresh(ctx, target, out, cmd.ErrOrStderr()))
			} else if failed := refreshMachine(out); failed > 0 {
				hookErr = hookFailure(fmt.Errorf("%d harness file(s) or project(s) not refreshed", failed))
			}
			legacy := registeredLegacy()
			if skip, why := serviceOptOut(noService); skip {
				fmt.Fprintf(out, "service: not installed or changed (%s); a running satelled keeps its old build until 'satelle service restart'\n", why)
				printNeedsUpgrade(out, legacy)
				return hookErr
			}
			daemon := daemonPath
			if _, err := os.Stat(daemon); err != nil {
				daemon = resolveDaemon("")
			}
			err = refreshService(ctx, out, target, daemon)
			printNeedsUpgrade(out, legacy)
			if err != nil {
				return errors.Join(fmt.Errorf("the binaries are updated, but the service is not: %w; rerun 'satelle service refresh', or pass --no-service", err), hookErr)
			}
			return hookErr
		},
	}
	cmd.Flags().BoolVar(&noService, "no-service", false, "change no service unit: leave satelled's unit and the sync timer as they are (also "+envNoService+"=1)")
	cmd.Flags().StringVar(&version, "version", "", "install this version of satelle, and of satelled (serve-v<version>) beside it, instead of the latest")
	return cmd
}

func daemonVersion(ctx context.Context, path string) string {
	if v, ok := buildinfo.VersionOf(buildinfo.Identify(ctx, config.GlobalDir(), path)); ok {
		return v
	}
	return "unknown"
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

func fetchAsset(ctx context.Context, tag, name string) ([]byte, error) {
	version := strings.TrimPrefix(tag, serveTagPrefix)
	asset := fmt.Sprintf("%s-%s-%s-%s%s", name, version, runtime.GOOS, runtime.GOARCH, buildinfo.ExeSuffix())
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
