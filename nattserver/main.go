// Package main 是 nattserver（NATT内网穿透服务端）的程序入口。
// 负责启动HTTP管理后台、控制通道和数据通道三大核心服务，
// 提供客户端授权、隧道管理、流量监控和MCP等功能。
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

	// nattserver/internal/api HTTP API路由和处理器。
	"nattserver/internal/api"
	// nattserver/internal/config 应用配置加载和管理。
	"nattserver/internal/config"
	// nattserver/internal/control 控制通道和数据通道的服务端逻辑。
	"nattserver/internal/control"
	// nattserver/internal/db SQLite数据库操作层。
	"nattserver/internal/db"
	// nattserver/internal/httpserver HTTP服务器封装。
	"nattserver/internal/httpserver"
	// nattserver/internal/logger 日志记录器。
	"nattserver/internal/logger"
	// nattserver/internal/startup 首次启动初始化向导和端口检测。
	"nattserver/internal/startup"
)

// main 是 nattserver 程序的入口函数。
// 执行流程：解析命令行参数→切换工作目录→加载配置→初始化控制台→
// 端口检测→初始化日志→初始化数据库→创建控制服务器→注册路由→
// 启动HTTP和控制服务→等待关闭信号→优雅退出。
func main() {
	// 第一步：解析命令行参数，支持-config指定配置文件路径
	configPath := flag.String("config", configFlagDefault(), "path to config file (default "+config.DefaultPath+"; explicit YAML compatible: "+config.LegacyYAMLPath+")")
	flag.Parse()

	// 第二步：切换到程序所在目录（默认启动时以程序目录为工作目录）
	if err := enterExecutableDirForDefaultStartup(*configPath); err != nil {
		panic(err)
	}

	// 第三步：创建可取消的上下文，监听系统中断信号（Ctrl+C和SIGTERM）
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 第四步：加载配置文件，如果默认配置不存在则进入初始化向导
	cfg, err := config.Load(*configPath)
	if err != nil {
		// 默认启动且配置文件不存在时，运行初始化向导
		if *configPath == "" && errors.Is(err, config.ErrDefaultConfigMissing) {
			cfg, err = startup.RunInitialization(ctx, config.Default())
		}
		if err != nil {
			panic(err)
		}
	}
	// 第五步：确保控制台账号已初始化（数据库中至少有一个用户）
	cfg, err = ensureConsoleInitialized(ctx, *configPath, cfg)
	if err != nil {
		panic(err)
	}

	// 第六步：检查所需端口是否可用（HTTP管理端口、控制端口、数据端口）
	if err := startup.CheckPorts(startupPortChecks(cfg)); err != nil {
		panic(err)
	}

	// 第七步：初始化日志记录器
	log, err := logger.New(cfg.Log.Dir, cfg.Log.Level)
	if err != nil {
		panic(err)
	}
	defer log.Close()

	// 第八步：打开并初始化SQLite数据库（创建表结构、执行迁移）
	database, err := db.Open(ctx, cfg.Database.Path, log)
	if err != nil {
		log.Errorf("database initialization failed: %v", err)
		os.Exit(1)
	}
	defer database.Close()

	// 第九步：创建控制通道服务器（管理客户端连接、隧道和数据转发）
	controlServer := control.NewServer(cfg.Protocol, database, log)
	// 第十步：注册HTTP API路由（含运行时控制能力）
	router := api.NewRouterWithRuntime(cfg, database, log, controlServer)
	// 第十一步：创建HTTP服务器
	httpServer := httpserver.New(cfg.HTTP, router, log)

	// 第十二步：构建服务运行器列表并启动所有服务
	runners := buildServerRunners(httpServer.Run, controlServer.Run)
	if err := runServers(ctx, log, runners...); err != nil {
		log.Errorf("nattserver stopped with error: %v", err)
		os.Exit(1)
	}
	log.Infof("nattserver stopped")
}

// configFlagDefault 返回配置文件路径的命令行默认值。
// 返回空字符串表示使用默认路径（xiaoliang02_server/config/config.json）。
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
	// 检查数据库中是否需要初始化控制台账号
	needsInit, err := needsConsoleInitialization(ctx, cfg)
	if err != nil {
		// 检查失败时，若为默认启动模式则直接进入初始化向导
		if strings.TrimSpace(configPath) == "" {
			return startup.RunInitialization(ctx, cfg)
		}
		return nil, fmt.Errorf("检查控制台初始化状态失败: %w", err)
	}
	// 不需要初始化则直接返回当前配置
	if !needsInit {
		return cfg, nil
	}
	// 默认启动模式下自动进入初始化向导
	if strings.TrimSpace(configPath) == "" {
		return startup.RunInitialization(ctx, cfg)
	}
	// 指定配置文件启动时，若未初始化则提示用户使用默认启动
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
	// 查询数据库中的用户总数
	count, err := db.CountUsers(ctx, database)
	if err != nil {
		return true, err
	}
	// 用户数为0表示需要初始化
	return count == 0, nil
}

// enterExecutableDirForDefaultStartup 在默认启动模式下切换到程序所在目录。
// 确保程序以自身所在目录为工作目录，以便正确读取相对路径下的配置和数据文件。
// 当指定了配置文件路径时（非默认启动），不切换目录。
// 参数configPath：命令行指定的配置文件路径（空字符串表示默认启动）。
// 返回值：可能的错误。
func enterExecutableDirForDefaultStartup(configPath string) error {
	// 获取当前可执行文件的绝对路径
	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取程序路径失败: %w", err)
	}
	// 判断是否需要切换目录以及目标目录路径
	dir, shouldChange, err := defaultStartupWorkingDirectory(configPath, executablePath)
	if err != nil {
		return err
	}
	// 不需要切换目录时直接返回
	if !shouldChange {
		return nil
	}
	// 切换工作目录到程序所在目录
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
	// 指定了配置文件路径时，不需要切换目录（用户已明确运行环境）
	if strings.TrimSpace(configPath) != "" {
		return "", false, nil
	}
	// 可执行文件路径不能为空
	if strings.TrimSpace(executablePath) == "" {
		return "", false, fmt.Errorf("程序路径为空")
	}
	// 获取可执行文件所在目录
	dir := filepath.Dir(executablePath)
	// 当前目录或空目录不需要切换
	if dir == "." || dir == "" {
		return "", false, nil
	}
	// 将目录解析为绝对路径
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false, fmt.Errorf("解析程序目录失败: %w", err)
	}
	return absDir, true, nil
}

// buildServerRunners 将HTTP服务和控制服务的运行函数组装为运行器列表。
// 参数httpRunner：HTTP服务运行函数。
// 参数controlRunner：控制通道服务运行函数。
// 返回值：包含所有服务运行函数的切片。
func buildServerRunners(httpRunner func(context.Context) error, controlRunner func(context.Context) error) []func(context.Context) error {
	return []func(context.Context) error{httpRunner, controlRunner}
}

// startupPortChecks 构建启动时需要检测的端口列表。
// 服务端需要检测三个端口：HTTP管理端口、控制通道端口和数据通道端口。
// 参数cfg：应用配置。
// 返回值：端口检测项列表。
func startupPortChecks(cfg *config.Config) []startup.PortCheck {
	return []startup.PortCheck{
		{Name: "HTTP管理端口", Host: cfg.HTTP.Host, Port: cfg.HTTP.Port},                   // Web后台管理端口（默认25510）
		{Name: "控制端口", Host: cfg.Protocol.ControlHost, Port: cfg.Protocol.ControlPort}, // 控制通道端口（默认25511）
		{Name: "数据端口", Host: cfg.Protocol.DataHost, Port: cfg.Protocol.DataPort},       // 数据通道端口（默认25512）
	}
}

// runServers 并发启动所有服务运行器，并等待关闭信号。
// 所有服务在各自的goroutine中运行，任意一个服务出错或收到关闭信号时，
// 取消所有服务的上下文并等待它们优雅退出（最长等待15秒）。
// 参数ctx：上下文（收到中断信号时Done）。
// 参数log：日志记录器。
// 参数runners：可变参数的服务运行函数列表。
// 返回值：第一个发生的错误（如果有）。
func runServers(ctx context.Context, log *logger.Logger, runners ...func(context.Context) error) error {
	// 创建可取消的子上下文，用于通知所有服务关闭
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 创建错误通道（缓冲大小等于运行器数量，避免goroutine泄漏）
	errCh := make(chan error, len(runners))
	// 在各自的goroutine中启动每个服务
	for _, runner := range runners {
		go func(run func(context.Context) error) {
			errCh <- run(runCtx)
		}(runner)
	}

	// 等待第一个服务结束或收到关闭信号
	var firstErr error
	completed := 0
	select {
	case <-ctx.Done():
		// 收到系统中断信号
	case err := <-errCh:
		completed = 1
		if err != nil {
			firstErr = err // 记录第一个错误
		}
	}
	// 通知所有服务关闭
	cancel()

	// 等待所有剩余服务退出（最长等待15秒超时）
	for completed < len(runners) {
		select {
		case err := <-errCh:
			completed++
			if firstErr == nil && err != nil {
				firstErr = err
			}
		case <-time.After(15 * time.Second):
			// 超时保护，避免无限等待
			if log != nil {
				log.Errorf("timed out waiting for service shutdown")
			}
			return firstErr
		}
	}
	return firstErr
}
