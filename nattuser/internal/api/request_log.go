// Package api 提供HTTP请求日志中间件。
// 记录每个HTTP请求的方法、路径、状态码、响应时间等信息，
// 便于问题排查和性能分析。
package api

import (
	"time"

	"nattuser/internal/logger"

	"github.com/gin-gonic/gin"
)

func RequestLogMiddleware(log *logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()

		if log == nil {
			return
		}
		log.Infof(
			"http request request_id=%s method=%s path=%s status=%d latency_ms=%d client_ip=%s",
			RequestID(c),
			c.Request.Method,
			c.Request.URL.Path,
			c.Writer.Status(),
			time.Since(startedAt).Milliseconds(),
			c.ClientIP(),
		)
	}
}
