// Package main 是 nattuser（NATT内网穿透客户端）的程序入口。
// 负责启动HTTP管理后台和控制连接管理器两大核心服务，
// 提供隧道连接管理、本地目标绑定、服务端连接控制和MCP等功能。
package main

import (
	// context 提供上下文传递和取消信号，用于优雅关闭服务。
	"context"
	// errors 提供错误类型判断（如errors.Is）。
	"errors"
	// flag 提供命令行参数解析。
	"flag"
	// fmt 提供错误信息的格式化输出。
	"fmt"
	// os 提供操作系统信号和进程退出控制。
	"os"
	// os/signal 提供系统信号捕获（如Ctrl+C中断）。
	"os/signal"
	// path/filepath 提供文件路径操作（如获取程序所在目录）。
	"path/filepath"
	// strings 提供字符串处理（如TrimSpace）。
	"strings"
	// syscall 提供系统调用常量（如SIGTERM信号）。
	"syscall"
	// time 提供超时和等待时间设置。
	"time"

	// nattuser/internal/api HTTP API路由和处理器。
	"nattuser/internal/api"
	// nattuser/internal/config 应用配置加载和管理。
	"nattuser/internal/config"
	// nattuser/internal/control 控制连接管理器和数据通道的客户端逻辑。
	"nattuser/internal/control"
	// nattuser/internal/db SQLite数据库操作层。
	"nattuser/internal/db"
	// nattuser/internal/httpserver HTTP服务器封装。
	"nattuser/internal/httpserver"
	// nattuser/internal/logger 日志记录器。
	"nattuser/internal/logger"
	// nattuser/internal/startup 首次启动初始化向导和端口检测。
	"nattuser/internal/startup"
)

// main 是 nattuser 客户端的入口函数。
// 执行流程：解析命令行参数→切换工作目录→加载配置→初始化控制台→
// 端口检测→初始化日志→初始化数据库→注册路由→启动HTTP和控制服务→
// 等待关闭信号→优雅退出。
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

// configFlagDefault 返回配置文件路径的命令行默认值。
// 返回空字符串表示使用默认路径（xiaoliang02_user/config/config.json）。
func configFlagDefault() string {
	return ""
}

// ensureConsoleInitialized 确保控制台管理账号已初始化。
// 如果数据库中没有任何用户，则进入初始化向导页面。
// 参数ctx：上下文传递。
// 参数configPath：配置文件路径（空字符串表示默认启动模式）。
// 参数cfg：当前加载的配置。
// 返回值：可能需要更新的配置和错误。
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

// needsConsoleInitialization 检查是否需要初始化控制台管理账号。
// 通过查询数据库中的用户数量来判断：用户数为0则需要初始化。
// 参数ctx：上下文传递。
// 参数cfg：当前配置（用于获取数据库路径）。
// 返回值：是否需要初始化（true=需要）和错误。
func needsConsoleInitialization(ctx context.Context, cfg *config.Config) (bool, error) {
	// 临时打开数据库连接查询用户数
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

// enterExecutableDirForDefaultStartup 在默认启动模式下切换到程序所在目录。
// 确保程序以自身所在目录为工作目录，以便正确读取相对路径下的配置和数据文件。
// 当指定了配置文件路径时（非默认启动），不切换目录。
// 参数configPath：命令行指定的配置文件路径（空字符串表示默认启动）。
// 返回值：可能的错误。
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

// defaultStartupWorkingDirectory 计算默认启动时应使用的工作目录。
// 仅在未指定配置文件路径（默认启动）时，才返回程序所在目录作为工作目录。
// 参数configPath：命令行指定的配置文件路径。
// 参数executablePath：可执行文件的路径。
// 返回值：工作目录路径、是否需要切换、错误。
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

// buildServiceRunners 将HTTP服务和控制器服务的运行函数组装为运行器列表。
// 参数httpRunner：HTTP服务运行函数。
// 参数controlRunner：控制连接管理器运行函数。
// 返回值：包含所有服务运行函数的切片。
func buildServiceRunners(httpRunner func(context.Context) error, controlRunner func(context.Context) error) []func(context.Context) error {
	return []func(context.Context) error{httpRunner, controlRunner}
}

// startupPortChecks 构建启动时需要检测的端口列表。
// 客户端只需要检测HTTP管理端口。
// 参数cfg：应用配置。
// 返回值：端口检测项列表。
func startupPortChecks(cfg *config.Config) []startup.PortCheck {
	return []startup.PortCheck{
		{Name: "HTTP管理端口", Host: cfg.HTTP.Host, Port: cfg.HTTP.Port},
	}
}

// runServices 并发启动所有服务运行器，并等待关闭信号。
// 所有服务在各自的goroutine中运行，任意一个服务出错或收到关闭信号时，
// 取消所有服务的上下文并等待它们优雅退出（最长等待15秒）。
// 参数ctx：上下文（收到中断信号时Done）。
// 参数log：日志记录器。
// 参数runners：可变参数的服务运行函数列表。
// 返回值：第一个发生的错误（如果有）。
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
