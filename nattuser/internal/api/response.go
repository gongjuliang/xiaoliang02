// Package api 提供统一的API响应格式和分页支持。
// 定义标准JSON响应结构体（code/message/data/request_id）、
// 分页请求/响应封装和业务状态码常量。
package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/gin-gonic/gin"
)

const requestIDKey = "request_id"

type Response struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`
}

type PageRequest struct {
	Page     int `form:"page"`
	PageSize int `form:"page_size"`
}

type PageResponse struct {
	Items    any   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

const (
	CodeOK            = 0
	CodeBadRequest    = 40001
	CodeUnauthorized  = 40101
	CodeForbidden     = 40301
	CodeNotFound      = 40401
	CodeConflict      = 40901
	CodeTooMany       = 42901
	CodeInternalError = 50001
)

func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = newRequestID()
		}
		c.Set(requestIDKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Response{
		Code:      CodeOK,
		Message:   "ok",
		Data:      data,
		RequestID: RequestID(c),
	})
}

func Fail(c *gin.Context, httpStatus int, code int, message string) {
	c.JSON(httpStatus, Response{
		Code:      code,
		Message:   message,
		Data:      nil,
		RequestID: RequestID(c),
	})
}

func NewPageResponse(items any, total int64, page PageRequest) PageResponse {
	page.Normalize()
	return PageResponse{
		Items:    items,
		Total:    total,
		Page:     page.Page,
		PageSize: page.PageSize,
	}
}

func (p *PageRequest) Normalize() {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 {
		p.PageSize = 20
	}
	if p.PageSize > 100 {
		p.PageSize = 100
	}
}

func (p PageRequest) Limit() int {
	if p.PageSize < 1 {
		return 20
	}
	if p.PageSize > 100 {
		return 100
	}
	return p.PageSize
}

func (p PageRequest) Offset() int {
	page := p.Page
	if page < 1 {
		page = 1
	}
	return (page - 1) * p.Limit()
}

func RequestID(c *gin.Context) string {
	value, ok := c.Get(requestIDKey)
	if !ok {
		return ""
	}
	requestID, _ := value.(string)
	return requestID
}

func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(buf[:])
}
