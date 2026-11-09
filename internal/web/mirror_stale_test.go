package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/mirror"
)

// seedFresh creates a partition whose last confirmed ingest is at.
func seedFresh(t *testing.T, s *mirror.Store, repoKey, slug string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.TouchPartition(ctx, repoKey, slug, at); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFresh(ctx, repoKey, at); err != nil {
		t.Fatal(err)
	}
}

// TestFreshnessShownForEveryPartition: a view that was not confirmed recently
// is never indistinguishable from one that was. The elapsed time says it on
// EVERY row (sty_226a661e), and a partition past StaleAfter also carries the
// stale marker, on the landing row and the project header, while one inside it
// carries none. The marker does not repeat the age; its hover names since when
// and asks after satelle-sync.timer, or says it was never confirmed
// (sto_128803b3).
func TestFreshnessShownForEveryPartition(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	stale := time.Now().UTC().Add(-mirror.StaleAfter - time.Hour)
	seedFresh(t, s, "rk-stale", "stale-repo", stale)
	// Just inside the threshold: the other side of it.
	seedFresh(t, s, "rk-fresh", "fresh-repo", time.Now().UTC().Add(-mirror.StaleAfter+time.Minute))
	// Never confirmed: a partition no ingest has committed to.
	if _, err := s.TouchPartition(context.Background(), "rk-never", "never-repo", time.Now()); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewMirror(s).Handler)
	t.Cleanup(srv.Close)

	stamp := stale.Local().Format("2006-01-02 15:04")
	landing := get(t, srv.URL+"/")

	// The stale marker is on the stale and never-confirmed rows only, and
	// reads just "stale": the <time> beside it already gives the age.
	if n := strings.Count(landing, `class="stale-flag"`); n != 2 {
		t.Errorf("want exactly two stale markers on the landing, got %d:\n%s", n, landing)
	}
	if strings.Count(landing, `>stale</span>`) != 2 || strings.Contains(landing, "stale · ") {
		t.Errorf("the stale marker must read just %q, not repeat the age:\n%s", "stale", landing)
	}
	if !strings.Contains(landing, `title="No confirmation since `+stamp+`; is satelle-sync.timer running?">stale</span>`) {
		t.Errorf("the stale marker's hover must name since when and ask after the timer:\n%s", landing)
	}
	if !strings.Contains(landing, `title="Never confirmed; is satelle-sync.timer running?">stale</span>`) {
		t.Errorf("a never-confirmed partition's hover must say so:\n%s", landing)
	}
	// AC1: a column, and a stamp on EVERY row — not only the stale one. Three
	// partitions were seeded, so three rel-times; the old badge would have been 1.
	if !strings.Contains(landing, "<th>Updated</th>") {
		t.Error("the landing needs an Updated column header")
	}
	if n := strings.Count(landing, `class="rel-time"`); n != 3 {
		t.Errorf("every row carries its freshness: want 3 rel-times, got %d:\n%s", n, landing)
	}
	// AC3: the absolute stamp survives, on hover.
	if !strings.Contains(landing, `title="Last confirmed against the repository at `+stamp) {
		t.Errorf("the absolute last-ingest time %q must remain available on hover:\n%s", stamp, landing)
	}
	// AC4: the ticker needs a machine-readable instant, not a formatted string.
	if !strings.Contains(landing, `datetime="`+stale.UTC().Format(time.RFC3339)) {
		t.Errorf("the stale row needs an absolute datetime for the client ticker:\n%s", landing)
	}
	// AC1, the point of the change: the row reads as elapsed time, not a flag.
	// The fixture is StaleAfter (6m) + 1h, so floor-hours is exactly 1.
	if !strings.Contains(landing, ">1 hr ago</time>") {
		t.Errorf("the stale partition should read %q:\n%s", "1 hr ago", landing)
	}
	// …and the fresh one is distinguishable from it below the threshold.
	inside := relTime(time.Now().Add(-mirror.StaleAfter+time.Minute), time.Now())
	if !strings.Contains(landing, ">"+inside+"</time>") {
		t.Errorf("the partition inside the threshold should read %q:\n%s", inside, landing)
	}

	// Second surface: both project pages show the freshness inline; only the
	// stale one shows the marker.
	for slug, wantStale := range map[string]bool{"stale-repo": true, "fresh-repo": false} {
		page := get(t, srv.URL+"/r/"+slug+"/")
		if got := strings.Contains(page, `class="stale-flag"`); got != wantStale {
			t.Errorf("%s project page stale marker = %v, want %v", slug, got, wantStale)
		}
		if !strings.Contains(page, `class="rel-time"`) {
			t.Errorf("%s project page must show when it was last updated:\n%s", slug, page)
		}
	}
}

// TestFreshnessFragmentCarriesTimestamp proves AC6's precondition: the SSE
// soft-refresh swaps in /fragment/projects markup, so the fragment — not just
// the full page — has to carry the datetime the ticker re-renders from.
func TestFreshnessFragmentCarriesTimestamp(t *testing.T) {
	s, err := mirror.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seedFresh(t, s, "rk", "repo", time.Now().UTC())
	srv := httptest.NewServer(NewMirror(s).Handler)
	t.Cleanup(srv.Close)

	frag := get(t, srv.URL+"/fragment/projects")
	if !strings.Contains(frag, `class="updated-cell"`) || !strings.Contains(frag, `class="rel-time"`) {
		t.Errorf("the live fragment must carry the Updated cell:\n%s", frag)
	}
	if !strings.Contains(frag, `datetime="`) {
		t.Errorf("the live fragment must carry a machine-readable instant:\n%s", frag)
	}
}

// TestRelTimeBuckets pins the Go half of the wording contract. Its twin,
// TestAppJSMirrorsRelTimeWording, fences the JS half — together they are what
// makes AC7 checkable rather than merely intended.
func TestRelTimeBuckets(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "just now"},
		{59 * time.Second, "just now"},
		{time.Minute, "1 min ago"},
		{119 * time.Second, "1 min ago"},
		{2 * time.Minute, "2 mins ago"},
		{59 * time.Minute, "59 mins ago"},
		{time.Hour, "1 hr ago"},
		{2 * time.Hour, "2 hrs ago"},
		{23*time.Hour + 59*time.Minute, "23 hrs ago"},
		{24 * time.Hour, "1 day ago"},
		{47 * time.Hour, "1 day ago"},
		{48 * time.Hour, "2 days ago"},
		{30 * 24 * time.Hour, "30 days ago"},
		// Clock skew between the ingesting repo and the serving tier: a stamp in
		// the future must never render as "in 3 minutes".
		{-5 * time.Minute, "just now"},
	} {
		if got := relTime(now.Add(-c.ago), now); got != c.want {
			t.Errorf("relTime(%s ago) = %q, want %q", c.ago, got, c.want)
		}
	}
	if got := relTime(time.Time{}, now); got != "never" {
		t.Errorf("a zero stamp renders %q, want %q", got, "never")
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	return string(raw)
}
