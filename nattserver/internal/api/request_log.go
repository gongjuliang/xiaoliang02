// Package api 提供HTTP请求日志中间件，记录每个请求的关键信息。
// 包括请求ID、HTTP方法、路径、响应状态码、延迟时间和客户端IP，
// 为问题排查和性能监控提供数据基础。
package api

import (
	// time 提供时间测量功能，计算请求处理耗时。
	"time"

	// nattserver/internal/logger 项目内部日志包。
	"nattserver/internal/logger"

	// github.com/gin-gonic/gin Gin Web框架。
	"github.com/gin-gonic/gin"
)

// RequestLogMiddleware 创建请求日志中间件。
// 在处理请求之前记录开始时间，在处理完成后记录请求方法、路径、
// 响应状态码、处理耗时和客户端IP等信息到日志。
// 参数log：日志记录器实例。
// 返回值：Gin中间件处理函数。
func RequestLogMiddleware(log *logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 记录请求开始时间，用于计算处理耗时
		startedAt := time.Now()
		// 先执行后续中间件和业务处理器
		c.Next()

		// 日志记录器为nil时跳过日志输出
		if log == nil {
			return
		}
		// 记录HTTP请求的关键信息到Info级别日志
		log.Infof(
			"http request request_id=%s method=%s path=%s status=%d latency_ms=%d client_ip=%s",
			RequestID(c),                         // 请求唯一ID
			c.Request.Method,                     // HTTP方法（GET/POST/PUT/DELETE等）
			c.Request.URL.Path,                   // 请求URL路径
			c.Writer.Status(),                    // HTTP响应状态码
			time.Since(startedAt).Milliseconds(), // 请求处理耗时（毫秒）
			c.ClientIP(),                         // 客户端IP地址
		)
	}
}
