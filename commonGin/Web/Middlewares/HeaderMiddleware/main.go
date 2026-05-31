package HeaderMiddleware

import "github.com/gin-gonic/gin"

func Hearder() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Methods", "POST, GET")
		c.Next()
	}
}

func StreamHearder() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 设置 SSE 相关的响应头
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")

	}
}
