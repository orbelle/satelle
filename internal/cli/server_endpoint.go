package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
)

// EnvServerEndpoint is the CLI override for [service] endpoint discovery and
// push (sty_5aa08259). The name and its parser live in internal/config so the
// serve-side reconciler honours the same off-switch (sty_e6e467fe).
const EnvServerEndpoint = config.EnvServerEndpoint

// HeaderSatelleInstance is the serve /healthz response header carrying the
// home-derived instance id (sty_5aa08259).
const HeaderSatelleInstance = "X-Satelle-Instance"

// resolveServerEndpointEnv inspects SATELLE_SERVER_ENDPOINT via the shared
// parser. disabled=true means discovery and push must not run. When disabled is
// false and endpoint is non-empty, callers should use it instead of config /
// probe.
func resolveServerEndpointEnv() (endpoint string, disabled bool, set bool) {
	return config.ServerEndpointEnv()
}

// healthzInstanceOK reports whether url (…/healthz) answers 200 with body
// containing "ok" and, when wantInstance is non-empty, X-Satelle-Instance
// matches. Missing header when wantInstance is set is a mismatch (fail closed
// for auto-bootstrap — foreign or pre-identity serves are not adopted).
func healthzInstanceOK(url, wantInstance string) bool {
	a := cachedHealthz(url)
	if !a.OK || wantInstance == "" {
		return a.OK
	}
	return a.Instance != "" && a.Instance == wantInstance
}

// healthzCacheTTL is how long one /healthz answer serves every later CLI
// process on the machine. The statusline and each hook run are separate
// processes that each probed satelled once (sty_a003a459).
const healthzCacheTTL = 10 * time.Second

// healthzNow is the cache's clock; tests move it past the TTL.
var healthzNow = time.Now

// healthzAnswer is one /healthz answer as the cache file records it.
type healthzAnswer struct {
	OK       bool      `json:"ok"`
	Instance string    `json:"instance,omitempty"`
	At       time.Time `json:"at"`
}

// healthzCachePath is the machine-local cache file, or "" when the global dir
// cannot be resolved (the probe then runs live).
func healthzCachePath() (path string) {
	defer func() {
		if recover() != nil {
			path = ""
		}
	}()
	return filepath.Join(config.GlobalDir(), "run", "healthz.json")
}

// cachedHealthz returns url's /healthz answer: one another process recorded
// within healthzCacheTTL, else a live probe, recorded for the next process. A
// probe that got no HTTP answer is not recorded, so a service that has just
// started is seen at once. Best-effort: any cache failure falls back to live.
func cachedHealthz(url string) healthzAnswer {
	path := healthzCachePath()
	entries := map[string]healthzAnswer{}
	now := healthzNow()
	fresh := func(a healthzAnswer) bool { age := now.Sub(a.At); return age >= 0 && age < healthzCacheTTL }
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &entries)
		}
		if a, ok := entries[url]; ok && fresh(a) {
			return a
		}
	}
	a, answered := probeHealthz(url)
	if !answered || path == "" {
		return a
	}
	a.At = now
	entries[url] = a
	for k, v := range entries {
		if !fresh(v) {
			delete(entries, k)
		}
	}
	if b, err := json.Marshal(entries); err == nil && os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		if tmp, err := os.CreateTemp(filepath.Dir(path), ".healthz-*"); err == nil {
			_, werr := tmp.Write(b)
			if cerr := tmp.Close(); werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
				_ = os.Remove(tmp.Name())
			}
		}
	}
	return a
}

// probeHealthz GETs url once. answered is false when no HTTP response came
// back; a closed port fails fast on the dial.
func probeHealthz(url string) (a healthzAnswer, answered bool) {
	host := strings.TrimPrefix(url, "http://")
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	conn, err := net.DialTimeout("tcp", host, 250*time.Millisecond)
	if err != nil {
		return a, false
	}
	_ = conn.Close()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return a, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return a, true
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	a.OK = strings.Contains(strings.ToLower(string(buf[:n])), "ok")
	a.Instance = strings.TrimSpace(resp.Header.Get(HeaderSatelleInstance))
	return a, true
}

// seedSkipNotice is the operator/test-facing message when registration
// succeeded but the mirror was not seeded (exit 0 — sty_5aa08259 AC3).
func seedSkipNotice(reason string) string {
	return fmt.Sprintf("workspace add: registered; seed skipped (%s) — project will not appear on the landing until seeded", reason)
}

// instanceMismatchReason names why auto-bootstrap refused a live serve.
func instanceMismatchReason(candidate, want string) string {
	return fmt.Sprintf(
		"serve at %s did not match this SATELLE_HOME instance (want X-Satelle-Instance=%s) — not auto-adopting a foreign mirror",
		candidate, want)
}
