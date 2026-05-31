package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"nattserver/internal/config"
	"nattserver/internal/logger"

	"github.com/gin-gonic/gin"
)

func NewRouter(cfg *config.Config, database *sql.DB, log *logger.Logger) *gin.Engine {
	return NewRouterWithRuntime(cfg, database, log, nil)
}

type Runtime interface {
	TunnelRuntime
	ClientConnectionCloser
}

func NewRouterWithRuntime(cfg *config.Config, database *sql.DB, log *logger.Logger, runtime Runtime) *gin.Engine {
	if cfg.App.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.Use(RequestLogMiddleware(log))
	router.Use(RecoveryMiddleware(log))
	router.Use(corsMiddleware())
	registerFrontendRoutes(router)
	router.GET("/health", healthHandler(database))

	authCfg := cfg.Auth
	authCfg.AllowPlaintextPassword = authCfg.AllowPlaintextPassword || cfg.App.Environment == "development"
	authHandler, err := NewAuthHandler(authCfg, database, log)
	if err != nil {
		panic(err)
	}

	v1 := router.Group("/api/server/v1")
	{
		v1.GET("/health", healthHandler(database))
		authHandler.RegisterRoutes(v1)

		protected := v1.Group("")
		protected.Use(authHandler.JWTMiddleware())
		NewClientHandler(database, log, runtime).RegisterRoutes(protected)
		NewTunnelHandler(database, log, &cfg.Tunnel, runtime).RegisterRoutes(protected)
		NewOpsHandler(database, log, cfg).RegisterRoutes(protected)
	}

	router.NoRoute(func(c *gin.Context) {
		Fail(c, http.StatusNotFound, 40401, "resource not found")
	})

	if log != nil {
		log.Infof("server routes registered")
	}
	return router
}

func healthHandler(database *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		dbStatus := "ok"
		if err := database.PingContext(ctx); err != nil {
			dbStatus = "error"
		}

		OK(c, gin.H{
			"status":   "ok",
			"database": dbStatus,
			"time":     time.Now().Format(time.RFC3339),
		})
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Request-ID")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
