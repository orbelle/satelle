package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/logfile"
)

// slowThreshold is the request duration above which a completed request logs at
// WARN instead of INFO — the signal for the heavy paths (e.g. the Projects landing) when they
// degrade. A var, not a const, so tests can lower it; a mechanism, not a process
// opinion, so it stays in code rather than a config knob (a follow-up can add one).
var slowThreshold = 1 * time.Second

// RequestLog wraps next so every request appends one grep-friendly line to the
// server log at logPath through the shared rotating writer (internal/logfile), with
// the same daily+size rotation as the other flat evidence logs. Best-effort: a log
// write never affects the response. It is wired at the serve listener (not web.New),
// so httptest servers stay log-free and each serve process logs to its own repo's
// runtime logs/server.log (reached from the repo via the .satelle/logs pointer).
//
// Line: <rfc3339>\t<LEVEL>\t<method>\t<path>\t<status>\t<duration_ms>[\t<note>].
// LEVEL is INFO, WARN when slow (SSE /events exempt — it is open for its lifetime),
// or ERROR for status >= 500 or a recovered panic. Only r.URL.Path is logged (no
// query string) to keep lines short and avoid logging anything sensitive-ish.
//
// http.ErrAbortHandler is re-panicked (not recovered), so the net/http server
// aborts and closes the connection as designed — recovering it would return a
// mid-stream (aborted-SSE) connection to the keep-alive pool, hanging the next
// request that reuses it (sty_fa24469a).
func RequestLog(next http.Handler, logPath string, cfg logfile.Config, notify func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLogsPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			dur := time.Since(start)
			if rec := recover(); rec != nil {
				// http.ErrAbortHandler is the sentinel the net/http server uses to
				// abort AND close the connection silently. httputil.ReverseProxy
				// raises it when a proxied stream (a child's /<slug>/events SSE)
				// aborts — e.g. a browser tab closes. Recovering it would return the
				// connection to the keep-alive pool mid-stream, so the next request
				// reusing it hangs (the browser times out on an unrelated GET /).
				// Re-panic BEFORE any write so the server closes it as designed; an
				// aborted stream is normal client behaviour, so it is not logged.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				if !sr.wrote {
					sr.ResponseWriter.WriteHeader(http.StatusInternalServerError)
				}
				logServer(logPath, cfg, start, "ERROR", r.Method, r.URL.Path,
					http.StatusInternalServerError, dur, fmt.Sprintf("panic: %v", rec))
				if notify != nil {
					notify()
				}
				return
			}
			level := "INFO"
			switch {
			case sr.status >= 500:
				level = "ERROR"
			case dur >= slowThreshold && r.URL.Path != "/events":
				level = "WARN"
			}
			logServer(logPath, cfg, start, level, r.Method, r.URL.Path, sr.status, dur, "")
			if notify != nil {
				notify()
			}
		}()
		next.ServeHTTP(sr, r)
	})
}

func isLogsPath(p string) bool {
	return p == "/logs" || p == "/fragment/logs"
}

// logServer appends one line, best-effort (a write error is ignored, matching the
// oplog/reviewer callers).
func logServer(path string, cfg logfile.Config, start time.Time, level, method, urlPath string, status int, dur time.Duration, note string) {
	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%d",
		start.UTC().Format(time.RFC3339), level, method, urlPath, status, dur.Milliseconds())
	if note != "" {
		line += "\t" + strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(note)
	}
	_ = logfile.Append(time.Now(), path, cfg, line)
}

// statusRecorder captures the response status for logging. It embeds the
// ResponseWriter and re-exposes Flush, so the recorder ALWAYS satisfies
// http.Flusher and delegates — the seam SSE (/events) and the supervisor's
// streaming reverse proxy depend on; hiding Flusher would silently break live
// refresh. No handler hijacks the connection today, so http.Hijacker is not
// re-exposed (add it here if that changes).
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Flush delegates to the underlying writer when it supports flushing, keeping SSE
// and reverse-proxy streaming working through the wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
