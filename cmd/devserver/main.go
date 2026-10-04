package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/hub"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/server"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/watcher"
)

func main() {
	configPath := flag.String("config", "devserver.json", "path to the JSON config file")
	addr := flag.String("addr", "", "override the configured listen address (e.g. :9000)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	if *addr != "" {
		cfg.Address = *addr
	}

	deps := make(map[string][]string, len(cfg.Modules))
	for _, m := range cfg.Modules {
		deps[m.Name] = m.Deps
	}
	g, err := graph.New(deps)
	if err != nil {
		log.Fatalf("invalid module graph: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	h := hub.New()
	o, err := builder.New(ctx, cfg, g, nil, builder.WithPublishCallback(h.Publish))
	if err != nil {
		log.Fatalf("initial build failed to start: %v", err)
	}
	<-o.Started // serve only after the first complete build committed
	// New subscribers must immediately receive the current complete state.
	h.Publish(o.Store().Current())

	watchDirs := watchDirs(cfg)
	w := watcher.New(watchDirs, cfg.WatchPollInterval.Std())
	changes := w.Run(ctx)
	go func() {
		for ch := range changes {
			o.NotifyChangedPaths([]string{ch.Path})
		}
	}()

	srv := server.New(cfg, o.Store(), h)
	log.Printf("dev server listening on %s (watching %d directories, %d modules, debounce %s, parallel %d)",
		cfg.Address, len(watchDirs), len(cfg.Modules), cfg.DebounceWindow.Std(), cfg.MaxParallelBuilds)

	serveErr := server.Serve(ctx, cfg, srv.Handler())

	// Graceful shutdown: stop accepting changes, finish bookkeeping.
	o.Close()
	h.Close()
	if serveErr != nil {
		log.Fatalf("server error: %v", serveErr)
	}
	log.Printf("server stopped")
}

func watchDirs(cfg *config.Config) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cfg.Workspace, dir)
		}
		dir = filepath.Clean(dir)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return
		}
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	for _, m := range cfg.Modules {
		add(m.Dir)
	}
	for _, dir := range cfg.WatchExtra {
		add(dir)
	}
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no existing directories to watch")
	}
	return dirs
}
