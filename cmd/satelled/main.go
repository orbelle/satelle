// Command satelled is the dedicated local-tier daemon (push-fed mirror UI)
// (sty_80233c10 / epic:serve-adoption; renamed sty_bd9de06d). It has no CLI
// verb surface and cannot mutate repo state — only ingest + read-only render.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/serve"
)

func main() {
	// Bare `go build` without ldflags still brands as satelled (Makefile /
	// release.yml stamp Name=satelled for release builds).
	if buildinfo.Name == buildinfo.CLIName || buildinfo.Name == "" {
		buildinfo.Name = buildinfo.DaemonName
	}
	addr := flag.String("addr", "", "bind address (default from global [service] addr)")
	port := flag.Int("port", 0, "listen port (default from global service config or 8787)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.Resolve().String())
		os.Exit(0)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := serve.Run(ctx, serve.Options{Addr: *addr, Port: *port}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
