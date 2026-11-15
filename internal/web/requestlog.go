package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/logfile"
)

var slowThreshold = 1 * time.Second

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

func logServer(path string, cfg logfile.Config, start time.Time, level, method, urlPath string, status int, dur time.Duration, note string) {
	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%d",
		start.UTC().Format(time.RFC3339), level, method, urlPath, status, dur.Milliseconds())
	if note != "" {
		line += "\t" + strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(note)
	}
	_ = logfile.Append(time.Now(), path, cfg, line)
}

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

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
