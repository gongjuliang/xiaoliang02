package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nattuser/internal/api"
	"nattuser/internal/config"
	"nattuser/internal/control"
	"nattuser/internal/db"
	"nattuser/internal/httpserver"
	"nattuser/internal/logger"
	"nattuser/internal/startup"
)

func main() {
	configPath := flag.String("config", configFlagDefault(), "path to config file (default "+config.DefaultPath+"; explicit YAML compatible: "+config.LegacyYAMLPath+")")
	flag.Parse()

	if err := enterExecutableDirForDefaultStartup(*configPath); err != nil {
		panic(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*configPath)
	if err != nil {
		if *configPath == "" && errors.Is(err, config.ErrDefaultConfigMissing) {
			cfg, err = startup.RunInitialization(ctx, config.Default())
		}
		if err != nil {
			panic(err)
		}
	}
	cfg, err = ensureConsoleInitialized(ctx, *configPath, cfg)
	if err != nil {
		panic(err)
	}

	if err := startup.CheckPorts(startupPortChecks(cfg)); err != nil {
		panic(err)
	}

	log, err := logger.New(cfg.Log.Dir, cfg.Log.Level)
	if err != nil {
		panic(err)
	}
	defer log.Close()

	database, err := db.Open(ctx, cfg.Database.Path, log)
	if err != nil {
		log.Errorf("database initialization failed: %v", err)
		os.Exit(1)
	}
	defer database.Close()

	router := api.NewRouter(cfg, database, log)
	httpServer := httpserver.New(cfg.HTTP, router, log)
	controlManager := control.NewManager(cfg, database, log)

	runners := buildServiceRunners(httpServer.Run, controlManager.Run)
	if err := runServices(ctx, log, runners...); err != nil {
		log.Errorf("nattuser stopped with error: %v", err)
		os.Exit(1)
	}
	log.Infof("nattuser stopped")
}

func configFlagDefault() string {
	return ""
}

func ensureConsoleInitialized(ctx context.Context, configPath string, cfg *config.Config) (*config.Config, error) {
	needsInit, err := needsConsoleInitialization(ctx, cfg)
	if err != nil {
		if strings.TrimSpace(configPath) == "" {
			return startup.RunInitialization(ctx, cfg)
		}
		return nil, fmt.Errorf("检查控制台初始化状态失败: %w", err)
	}
	if !needsInit {
		return cfg, nil
	}
	if strings.TrimSpace(configPath) == "" {
		return startup.RunInitialization(ctx, cfg)
	}
	return nil, fmt.Errorf("控制台账号尚未初始化，请使用默认启动进入初始化页面")
}

func needsConsoleInitialization(ctx context.Context, cfg *config.Config) (bool, error) {
	database, err := db.Open(ctx, cfg.Database.Path, nil)
	if err != nil {
		return true, err
	}
	defer database.Close()
	count, err := db.CountUsers(ctx, database)
	if err != nil {
		return true, err
	}
	return count == 0, nil
}

func enterExecutableDirForDefaultStartup(configPath string) error {
	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取程序路径失败: %w", err)
	}
	dir, shouldChange, err := defaultStartupWorkingDirectory(configPath, executablePath)
	if err != nil {
		return err
	}
	if !shouldChange {
		return nil
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("切换工作目录到程序目录失败: %w", err)
	}
	return nil
}

func defaultStartupWorkingDirectory(configPath string, executablePath string) (string, bool, error) {
	if strings.TrimSpace(configPath) != "" {
		return "", false, nil
	}
	if strings.TrimSpace(executablePath) == "" {
		return "", false, fmt.Errorf("程序路径为空")
	}
	dir := filepath.Dir(executablePath)
	if dir == "." || dir == "" {
		return "", false, nil
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false, fmt.Errorf("解析程序目录失败: %w", err)
	}
	return absDir, true, nil
}

func buildServiceRunners(httpRunner func(context.Context) error, controlRunner func(context.Context) error) []func(context.Context) error {
	return []func(context.Context) error{httpRunner, controlRunner}
}

func startupPortChecks(cfg *config.Config) []startup.PortCheck {
	return []startup.PortCheck{
		{Name: "HTTP管理端口", Host: cfg.HTTP.Host, Port: cfg.HTTP.Port},
	}
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
