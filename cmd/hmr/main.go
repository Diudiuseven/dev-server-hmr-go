// Command hmr runs the local incremental build and hot-reload server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hmr:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "hmr.json", "path to config file")
	flag.Parse()

	_ = *cfgPath
	_ = config.Config{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ctx.Done():
		return errors.New("context done")
	case <-sigCh:
		return nil
	}
}
