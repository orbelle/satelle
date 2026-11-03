// Package serve runs the push-fed mirror UI HTTP server (sty_80233c10).
// Shared by the dedicated satelled binary and the deprecated `satelle serve`
// CLI alias so behaviour cannot drift.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/mirror"
	"github.com/bobmcallan/satelle/internal/web"
)

// Options configures the mirror HTTP server.
type Options struct {
	Addr string // host, e.g. 127.0.0.1 or 0.0.0.0
	Port int    // 0 → global service config → DefaultWebPort
}

// Run opens the machine-wide mirror store and serves the RO UI until ctx ends.
func Run(ctx context.Context, opts Options) error {
	gc, _ := config.LoadGlobal()
	port := opts.Port
	if port == 0 {
		port = gc.Service.ResolvePort()
	}
	addr := opts.Addr
	if addr == "" {
		addr = gc.Service.ResolveAddr()
	}
	listenAddr := fmt.Sprintf("%s:%d", addr, port)

	mirrorPath := mirror.DefaultPath(config.GlobalDir())
	ms, err := mirror.Open(mirrorPath)
	if err != nil {
		return fmt.Errorf("open mirror store: %w", err)
	}
	defer ms.Close()

	serverLog := mirror.ServerLogPath(config.GlobalDir())
	var publishLogs func()
	logLine := newLogLine(serverLog, func() {
		if publishLogs != nil {
			publishLogs()
		}
	})
	// Hook must be installed before Listen: an ingest on the first accepted
	// connection has to find Notify already wired (sty_c526753a).
	home := config.GlobalDir()
	pusher := &Pusher{
		Resolve: resolvePushPath(ms, home),
		Keys:    pushKeys(ms),
		Log:     logLine,
		Record:  recordPushFunc(home),
		PushOff: config.MachinePushOff,
	}
	// Repair loop for pushes that never landed (sty_e6e467fe). Runs beside the
	// server, off the request path, and stops with ctx.
	rec := &Reconciler{
		Targets: mirrorTargets(ms, home),
		Log:     logLine,
		Record:  recordSnapshotFunc(home),
		PushOff: config.MachinePushOff,
	}
	// A local change both dirties the push and lifts a snapshot's conflict
	// backoff for the repo (sty_0367d20c); a hosted-origin one does neither.
	onIngest := func(repoKey string, origin mirror.IngestOrigin) {
		pusher.OnIngest(repoKey, origin)
		if origin != mirror.OriginHosted {
			rec.Touch(repoKey)
		}
	}
	webSrv := web.NewMirrorHooks(ms, config.SafeCurrentInstanceID(), onIngest)
	webSrv.FetchStory = fetchViaCLI
	publishLogs = func() { webSrv.Publish("logs") }
	handler := pprofLocal(web.RequestLog(webSrv.Handler, serverLog, serverLogConfig, publishLogs))

	srv := &http.Server{Addr: listenAddr, Handler: handler}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	// Bind before serving so the repair loop's first pass — which re-requests
	// snapshots through this very endpoint — cannot race the listener.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}

	go rec.Loop(ctx)
	go pusher.Run(ctx)

	fmt.Printf("satelled (push-fed mirror) http://%s  mirror=%s  (Ctrl-C to stop)\n", listenAddr, mirrorPath)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// serverLogConfig is the rotation of satelled's log file.
var serverLogConfig = logfile.Config{MaxSizeBytes: config.DefaultLogsMaxSizeKB * 1024, MaxFiles: config.DefaultLogsMaxFiles}

// newLogLine returns satelled's line writer: each line goes to stderr and to
// serverLog (mirror.ServerLogPath), then publish runs so the UI's log view
// refreshes. publish may be nil.
func newLogLine(serverLog string, publish func()) func(format string, args ...any) {
	return func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Fprintln(os.Stderr, "satelled: "+line)
		_ = logfile.Append(time.Now(), serverLog, serverLogConfig, line)
		if publish != nil {
			publish()
		}
	}
}

// pprofLocal serves net/http/pprof under /debug/pprof/ to loopback clients only
// (sty_0367d20c), so satelled's own work can be profiled on this machine
// whatever address it listens on. Any other client gets 404 there; every other
// path goes to next.
func pprofLocal(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/debug/pprof/") {
			next.ServeHTTP(w, r)
			return
		}
		if !loopbackClient(r.RemoteAddr) {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// loopbackClient reports whether a request's RemoteAddr is a loopback address.
func loopbackClient(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
