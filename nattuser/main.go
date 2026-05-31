package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nattuser/internal/api"
	"nattuser/internal/config"
	"nattuser/internal/control"
	"nattuser/internal/db"
	"nattuser/internal/httpserver"
	"nattuser/internal/logger"
	"nattuser/internal/mcp"
)

func main() {
	configPath := flag.String("config", configFlagDefault(), "path to config file (default "+config.DefaultPath+"; fallback "+config.LegacyYAMLPath+")")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		panic(err)
	}

	log, err := logger.New(cfg.Log.Dir, cfg.Log.Level)
	if err != nil {
		panic(err)
	}
	defer log.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.Database.Path, log)
	if err != nil {
		log.Errorf("database initialization failed: %v", err)
		os.Exit(1)
	}
	defer database.Close()

	router := api.NewRouter(cfg, database, log)
	httpServer := httpserver.New(cfg.HTTP, router, log)
	controlManager := control.NewManager(cfg, database, log)

	var mcpRunner func(context.Context) error
	if cfg.MCP.Enabled {
		mcpRouter := mcp.NewClientRouter(cfg.MCP, database, log)
		mcpServer := httpserver.New(mcpHTTPConfig(cfg.MCP, cfg.HTTP), mcpRouter, log)
		mcpRunner = mcpServer.Run
	}

	runners := buildServiceRunners(cfg.MCP.Enabled, httpServer.Run, controlManager.Run, mcpRunner)
	if err := runServices(ctx, log, runners...); err != nil {
		log.Errorf("nattuser stopped with error: %v", err)
		os.Exit(1)
	}
	log.Infof("nattuser stopped")
}

func configFlagDefault() string {
	return ""
}

func buildServiceRunners(mcpEnabled bool, httpRunner func(context.Context) error, controlRunner func(context.Context) error, mcpRunner func(context.Context) error) []func(context.Context) error {
	runners := []func(context.Context) error{httpRunner, controlRunner}
	if mcpEnabled && mcpRunner != nil {
		runners = append(runners, mcpRunner)
	}
	return runners
}

func mcpHTTPConfig(mcpCfg config.MCPConfig, base config.HTTPConfig) config.HTTPConfig {
	base.Host = mcpCfg.Host
	base.Port = mcpCfg.Port
	return base
}

func runServices(ctx context.Context, log *logger.Logger, runners ...func(context.Context) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(runners))
	for _, runner := range runners {
		go func(run func(context.Context) error) {
			errCh <- run(runCtx)
		}(runner)
	}

	var firstErr error
	completed := 0
	select {
	case <-ctx.Done():
	case err := <-errCh:
		completed = 1
		if err != nil {
			firstErr = err
		}
	}
	cancel()

	for completed < len(runners) {
		select {
		case err := <-errCh:
			completed++
			if firstErr == nil && err != nil {
				firstErr = err
			}
		case <-time.After(15 * time.Second):
			if log != nil {
				log.Errorf("timed out waiting for service shutdown")
			}
			return firstErr
		}
	}
	return firstErr
}
