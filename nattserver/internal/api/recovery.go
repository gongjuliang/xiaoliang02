// Package api 提供Panic恢复中间件，用于捕获HTTP处理器中的未处理panic，
// 记录详细错误信息和调用栈，并返回安全的500错误响应，避免服务进程崩溃。
package api

import (
	// net/http 提供HTTP状态码常量。
	"net/http"
	// runtime/debug 提供Stack()函数获取当前goroutine的调用栈信息。
	"runtime/debug"

	// nattserver/internal/logger 项目内部日志包。
	"nattserver/internal/logger"

	// github.com/gin-gonic/gin Gin Web框架。
	"github.com/gin-gonic/gin"
)

// RecoveryMiddleware 创建Panic恢复中间件。
// 当请求处理过程中发生panic时，捕获异常并记录调用栈日志，
// 然后返回500错误响应，避免整个服务进程崩溃。
// 参数log：日志记录器，用于输出panic详细信息和调用栈。
// 返回值：Gin中间件处理函数。
func RecoveryMiddleware(log *logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// defer在函数返回前执行，用于捕获可能的panic
		defer func() {
			// recover()捕获panic值，若无panic则返回nil
			if recovered := recover(); recovered != nil {
				// 如果有日志记录器，记录panic的详细信息
				if log != nil {
					log.Errorf(
						"panic recovered request_id=%s method=%s path=%s panic=%v stack=%s",
						RequestID(c),       // 请求ID
						c.Request.Method,   // HTTP方法
						c.Request.URL.Path, // 请求路径
						recovered,          // panic值
						debug.Stack(),      // 完整调用栈
					)
				}
				// 如果响应尚未写入（避免重复写入），返回500错误
				if !c.Writer.Written() {
					Fail(c, http.StatusInternalServerError, CodeInternalError, "internal server error")
				}
				// 中止后续中间件和处理器执行
				c.Abort()
			}
		}()
		// 执行下一个中间件/处理器
		c.Next()
	}
}
