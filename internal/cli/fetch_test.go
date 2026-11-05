package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rateLimitStub answers every request with status and headers.
func rateLimitStub(t *testing.T, status int, headers map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPGetRateLimit(t *testing.T) {
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)

	t.Run("403 with exhausted remaining and a reset epoch", func(t *testing.T) {
		srv := rateLimitStub(t, http.StatusForbidden, map[string]string{
			"X-RateLimit-Remaining": "0",
			"X-RateLimit-Reset":     strconv.FormatInt(reset.Unix(), 10),
		})
		_, err := httpGetBytes(context.Background(), srv.URL, "")
		var rl *rateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("want *rateLimitError, got %T: %v", err, err)
		}
		if !rl.reset.Equal(reset) {
			t.Errorf("reset = %v, want %v", rl.reset, reset)
		}
		msg := err.Error()
		if !strings.Contains(msg, "rate limited") || !strings.Contains(msg, reset.Local().Format(time.RFC3339)) {
			t.Errorf("message lacks the rate limit or the reset time: %s", msg)
		}
		for _, banned := range []string{"GitHub", "GH_TOKEN", "GITHUB_TOKEN"} {
			if strings.Contains(msg, banned) {
				t.Errorf("shared helper message names %q: %s", banned, msg)
			}
		}
	})

	t.Run("429 with Retry-After", func(t *testing.T) {
		srv := rateLimitStub(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "60"})
		before := time.Now()
		_, err := httpGetBytes(context.Background(), srv.URL, "")
		var rl *rateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("want *rateLimitError, got %T: %v", err, err)
		}
		if d := rl.reset.Sub(before); d < 59*time.Second || d > 62*time.Second {
			t.Errorf("reset is %v from now, want about 60s", d)
		}
	})

	t.Run("rate limit without a usable reset omits the clause", func(t *testing.T) {
		srv := rateLimitStub(t, http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"})
		_, err := httpGetBytes(context.Background(), srv.URL, "")
		var rl *rateLimitError
		if !errors.As(err, &rl) || strings.Contains(err.Error(), "resets at") {
			t.Fatalf("want a rate limit error with no reset clause, got %v", err)
		}
	})

	t.Run("403 without rate limit headers stays a bare status", func(t *testing.T) {
		srv := rateLimitStub(t, http.StatusForbidden, nil)
		_, err := httpGetBytes(context.Background(), srv.URL, "")
		var rl *rateLimitError
		if err == nil || errors.As(err, &rl) {
			t.Fatalf("want a plain error, got %v", err)
		}
		if want := fmt.Sprintf("%s: 403 Forbidden", srv.URL); err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})
}

func TestHTTPGetBytesSendsNoAuthorization(t *testing.T) {
	t.Setenv("GH_TOKEN", "some-token")
	t.Setenv("GITHUB_TOKEN", "some-token")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	if _, err := httpGetBytes(context.Background(), srv.URL, ""); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("httpGetBytes sent Authorization %q; only the GitHub API caller carries a token", got)
	}
}
