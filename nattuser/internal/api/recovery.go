// Package api 提供Gin的Panic恢复中间件。
// 捕获处理请求过程中发生的panic，返回500错误响应，
// 防止单个请求的异常导致整个服务崩溃。
package api

import (
	"net/http"
	"runtime/debug"

	"nattuser/internal/logger"

	"github.com/gin-gonic/gin"
)

func RecoveryMiddleware(log *logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if log != nil {
					log.Errorf(
						"panic recovered request_id=%s method=%s path=%s panic=%v stack=%s",
						RequestID(c),
						c.Request.Method,
						c.Request.URL.Path,
						recovered,
						debug.Stack(),
					)
				}
				if !c.Writer.Written() {
					Fail(c, http.StatusInternalServerError, CodeInternalError, "internal server error")
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}
