// Command hmr runs the local incremental build and hot-reload server.
//
// It watches a configurable set of workspace directories, coalesces bursts
// of editor saves into incremental dependency-ordered builds, stores
// artifacts atomically and pushes update events to connected clients over
// Server-Sent Events.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/engine"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/hub"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/server"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/watcher"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hmr:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "hmr.json", "path to config file")
	addr := flag.String("addr", "", "override listen address (e.g. :9090)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.Server.Addr = *addr
	}

	// Dependency graph.
	decls := make([]graph.ModuleDecl, 0, len(cfg.Modules))
	specs := make([]builder.Spec, 0, len(cfg.Modules))
	names := make([]string, 0, len(cfg.Modules))
	for _, m := range cfg.Modules {
		decls = append(decls, graph.ModuleDecl{Name: m.Name, Deps: m.Deps})
		specs = append(specs, builder.Spec{
			Name: m.Name, Root: filepath.Join(cfg.Workspace, m.Root),
			Command: m.Command, Args: m.Args, Artifact: m.Artifact,
		})
		names = append(names, m.Name)
	}
	g, err := graph.New(decls)
	if err != nil {
		return err
	}

	st := state.NewStore(names)
	artDir := cfg.ArtifactDir
	if !filepath.IsAbs(artDir) {
		artDir = filepath.Join(cfg.Workspace, artDir)
	}
	art, err := store.NewArtifactStore(artDir)
	if err != nil {
		return err
	}
	h := hub.New()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	eng := engine.New(engine.Options{
		Config:    *cfg,
		Graph:     g,
		Builder:   builder.NewCommandBuilder(specs),
		State:     st,
		Artifacts: art,
		Debounce:  cfg.Watch.Debounce.Duration,
		OnEvent: func(ev engine.Event) {
			payload, _ := json.Marshal(ev)
			h.Broadcast(payload)
		},
	})

	// Filesystem watcher feeding merged change notifications.
	watchRoots := cfg.Watch.Roots
	if len(watchRoots) == 0 {
		watchRoots = make([]string, 0, len(cfg.Modules))
		for _, m := range cfg.Modules {
			watchRoots = append(watchRoots, m.Root)
		}
	}
	w := watcher.New(watcher.Options{
		Workspace: cfg.Workspace,
		Roots:     watchRoots,
		Include:   cfg.Watch.Include,
		Exclude:   append(cfg.Watch.Exclude, cfg.ArtifactDir, ".git"),
		Interval:  cfg.Watch.PollInterval.Duration,
	})
	changes := w.Run(ctx)
	go func() {
		<-w.Ready()
		log.Println("watcher ready; watching", watchRoots)
	}()

	engDone := make(chan struct{})
	go func() {
		if err := eng.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Println("engine stopped:", err)
		}
		close(engDone)
	}()

	// Pump file changes straight into the engine; the engine's coalescer
	// owns the debounce window, so no extra batching is needed here.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ch, ok := <-changes:
				if !ok {
					return
				}
				eng.NotifyChanged([]string{ch.Path})
			}
		}
	}()

	srv := server.New(*cfg, st, art, h, eng)
	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("hmr listening on %s", cfg.Server.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println("http:", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Println("http shutdown:", err)
	}
	<-engDone
	h.Stop()
	return nil
}
