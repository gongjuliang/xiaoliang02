// Package api 提供HTTP API路由的注册和组织。
// 负责组装Gin路由引擎、注册中间件（日志/恢复/请求ID/CORS）、
// 初始化认证/隧道/运维等功能模块的处理器，并挂载所有API端点。
package api

import (
	// context 提供上下文传递和超时控制。
	"context"
	// database/sql 提供SQL数据库连接操作。
	"database/sql"
	// net/http 提供HTTP状态码常量。
	"net/http"
	// time 提供时间格式化和超时设置。
	"time"

	// nattserver/internal/config 应用配置包。
	"nattserver/internal/config"
	// nattserver/internal/db 数据库操作包。
	"nattserver/internal/db"
	// nattserver/internal/logger 日志包。
	"nattserver/internal/logger"
	// nattserver/internal/mcp MCP服务注册包。
	"nattserver/internal/mcp"

	// github.com/gin-gonic/gin Gin Web框架。
	"github.com/gin-gonic/gin"
)

// NewRouter 创建并配置Gin路由引擎（不使用运行时依赖的简化版本）。
// 参数cfg：应用配置。
// 参数database：数据库连接。
// 参数log：日志记录器。
// 返回值：配置好的Gin路由引擎。
func NewRouter(cfg *config.Config, database *sql.DB, log *logger.Logger) *gin.Engine {
	// 委托给完整版，runtime传nil（适用于不需要运行时控制的场景）
	return NewRouterWithRuntime(cfg, database, log, nil)
}

// Runtime 运行时接口，定义了隧道运行时的操作能力。
// 通过接口实现解耦，API层可注入控制层（control包）的运行时实例。
type Runtime interface {
	// TunnelRuntime 嵌入隧道运行时接口（来自tunnels.go定义的接口）
	TunnelRuntime
}

// NewRouterWithRuntime 创建并配置Gin路由引擎（完整版本，支持运行时注入）。
// 配置流程：设置生产模式→注册中间件→配置审计日志→注册前端和健康检查路由→
// 创建认证处理器→注册V1 API路由组（认证+受保护的隧道和运维接口）→注册MCP路由。
// 参数cfg：应用配置。
// 参数database：数据库连接。
// 参数log：日志记录器。
// 参数runtime：控制层运行时实例（可为nil）。
// 返回值：配置好的Gin路由引擎。
func NewRouterWithRuntime(cfg *config.Config, database *sql.DB, log *logger.Logger, runtime Runtime) *gin.Engine {
	// 生产环境设置为Release模式（关闭调试信息，提高性能）
	if cfg.App.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	// 创建Gin引擎（使用New()而非Default()，手动添加中间件）
	router := gin.New()
	// 注册请求ID中间件（为每个请求分配唯一标识）
	router.Use(RequestIDMiddleware())
	// 注册请求日志中间件（记录请求耗时和状态）
	router.Use(RequestLogMiddleware(log))
	// 注册Panic恢复中间件（捕获异常防止服务崩溃）
	router.Use(RecoveryMiddleware(log))
	// 注册CORS跨域中间件
	router.Use(corsMiddleware())
	// 配置审计日志目录到数据库
	if err := db.ConfigureAuditLogDir(context.Background(), database, cfg.Log.Dir); err != nil {
		panic(err) // 审计日志目录配置失败则终止启动
	}
	// 注册前端页面路由
	registerFrontendRoutes(router)
	// 注册健康检查路由（根路径）
	router.GET("/health", healthHandler(database))

	// 构建认证配置，开发环境下允许明文密码传输
	authCfg := cfg.Auth
	// 开发环境（Environment=="development"）自动启用明文密码模式
	authCfg.AllowPlaintextPassword = authCfg.AllowPlaintextPassword || cfg.App.Environment == "development"
	// 创建认证处理器（含JWT服务、SM2加密器和限流器）
	authHandler, err := NewAuthHandler(authCfg, database, log)
	if err != nil {
		panic(err) // 认证处理器初始化失败终止启动
	}

	// 创建V1 API路由组（路径前缀：/api/server/v1）
	v1 := router.Group("/api/server/v1")
	{
		// 健康检查端点（导航组内也注册一个）
		v1.GET("/health", healthHandler(database))
		// 注册认证相关路由（登录、注册、刷新令牌、SM2公钥等）
		authHandler.RegisterRoutes(v1)

		// 创建受JWT保护的路由子组
		protected := v1.Group("")
		// 对保护组内的所有路由应用JWT认证中间件
		protected.Use(authHandler.JWTMiddleware())
		// 注册隧道管理路由（列表、创建、启停等）
		NewTunnelHandler(database, log, &cfg.Tunnel, runtime).RegisterRoutes(protected)
		// 注册运维管理路由（仪表盘、系统设置、用户管理等）
		NewOpsHandler(database, log, cfg).RegisterRoutes(protected)
	}
	// 注册MCP（Model Context Protocol）服务路由
	mcp.RegisterServerRoutes(router, database, log, runtime, cfg.Tunnel)

	// 处理未匹配的路由，返回404 JSON响应
	router.NoRoute(func(c *gin.Context) {
		Fail(c, http.StatusNotFound, 40401, "resource not found")
	})

	// 记录路由注册完成日志
	if log != nil {
		log.Infof("server routes registered")
	}
	return router
}

// healthHandler 创建健康检查端点处理器。
// 检查服务整体状态和数据库连接状态，返回JSON格式的健康信息。
// 参数database：数据库连接，用于Ping检测。
// 返回值：Gin处理函数。
func healthHandler(database *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 创建2秒超时的上下文用于数据库Ping
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		// 默认数据库状态为"ok"
		dbStatus := "ok"
		// Ping数据库，失败则标记为"error"
		if err := database.PingContext(ctx); err != nil {
			dbStatus = "error"
		}

		// 返回健康检查结果
		OK(c, gin.H{
			"status":   "ok",                            // 服务整体状态
			"database": dbStatus,                        // 数据库连接状态
			"time":     time.Now().Format(time.RFC3339), // 当前时间（RFC3339格式）
		})
	}
}

// corsMiddleware 创建CORS（跨域资源共享）中间件。
// 允许所有来源的跨域请求，配置允许的HTTP方法和请求头，
// 并正确处理OPTIONS预检请求。
// 返回值：Gin中间件处理函数。
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 允许所有来源的跨域请求（*表示不限制来源）
		c.Header("Access-Control-Allow-Origin", "*")
		// 允许的HTTP方法列表
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		// 允许的自定义请求头列表
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Request-ID")
		// OPTIONS预检请求直接返回204 No Content
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		// 非OPTIONS请求继续正常处理
		c.Next()
	}
}
