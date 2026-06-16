// Package api 提供HTTP API层的统一响应格式、请求ID追踪、分页请求/响应封装。
// 定义了标准的JSON响应结构体和业务状态码，以及请求ID的生成和传递机制。
package api

import (
	// crypto/rand 提供密码学安全随机数用于生成唯一请求ID。
	"crypto/rand"
	// encoding/hex 提供十六进制编码，将随机字节转为可读的请求ID字符串。
	"encoding/hex"
	// net/http 提供HTTP状态码常量。
	"net/http"

	// github.com/gin-gonic/gin Gin Web框架，提供HTTP请求上下文和路由功能。
	"github.com/gin-gonic/gin"
)

// requestIDKey 在Gin上下文中存储请求ID的键名。
const requestIDKey = "request_id"

// Response 统一API响应结构体，所有HTTP API响应均使用此格式。
type Response struct {
	// Code 业务状态码，0表示成功，其他值表示具体错误类型。
	Code int `json:"code"`
	// Message 人类可读的响应消息。
	Message string `json:"message"`
	// Data 响应数据载荷，成功时包含业务数据，失败时为nil。
	Data any `json:"data"`
	// RequestID 请求唯一标识，用于日志追踪和问题排查。
	RequestID string `json:"request_id"`
}

// PageRequest 分页请求参数结构体，从URL查询参数解析。
type PageRequest struct {
	// Page 请求的页码，从1开始（form:"page"从查询参数绑定）。
	Page int `form:"page"`
	// PageSize 每页记录数（form:"page_size"从查询参数绑定）。
	PageSize int `form:"page_size"`
}

// PageResponse 分页响应结构体，返回分页数据及分页元信息。
type PageResponse struct {
	// Items 当前页的数据项列表。
	Items any `json:"items"`
	// Total 符合条件的总记录数。
	Total int64 `json:"total"`
	// Page 当前页码。
	Page int `json:"page"`
	// PageSize 每页记录数。
	PageSize int `json:"page_size"`
}

// 业务状态码常量：定义了API层使用的标准化错误码。
const (
	// CodeOK 操作成功。
	CodeOK = 0
	// CodeBadRequest 请求格式或参数错误（HTTP 400对应的业务码）。
	CodeBadRequest = 40001
	// CodeUnauthorized 未认证或Token无效（HTTP 401对应的业务码）。
	CodeUnauthorized = 40101
	// CodeForbidden 权限不足（HTTP 403对应的业务码）。
	CodeForbidden = 40301
	// CodeNotFound 资源未找到（HTTP 404对应的业务码）。
	CodeNotFound = 40401
	// CodeConflict 资源冲突（HTTP 409对应的业务码）。
	CodeConflict = 40901
	// CodeTooMany 请求频率过高（HTTP 429对应的业务码）。
	CodeTooMany = 42901
	// CodeInternalError 服务器内部错误（HTTP 500对应的业务码）。
	CodeInternalError = 50001
)

// RequestIDMiddleware 请求ID中间件：为每个HTTP请求分配唯一ID。
// 优先使用客户端传入的X-Request-ID头，否则自动生成一个新的随机ID。
// ID被存入Gin上下文并添加到响应头中，实现请求的全链路追踪。
// 返回值：Gin中间件处理函数。
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 优先从请求头获取客户端传入的请求ID
		requestID := c.GetHeader("X-Request-ID")
		// 如果客户端未传入，自动生成一个新的16字节随机ID
		if requestID == "" {
			requestID = newRequestID()
		}
		// 将请求ID存入Gin上下文，供后续处理器和中间件获取
		c.Set(requestIDKey, requestID)
		// 将请求ID写入响应头X-Request-ID，方便客户端追踪
		c.Header("X-Request-ID", requestID)
		// 继续执行下一个处理器
		c.Next()
	}
}

// OK 返回成功响应（HTTP 200 + Code=0）。
// 参数c：Gin请求上下文。
// 参数data：要返回给客户端的业务数据。
func OK(c *gin.Context, data any) {
	// 以JSON格式返回成功响应，含请求ID和业务数据
	c.JSON(http.StatusOK, Response{
		Code:      CodeOK,       // 业务成功码0
		Message:   "ok",         // 成功消息
		Data:      data,         // 业务数据
		RequestID: RequestID(c), // 从上下文获取请求ID
	})
}

// Fail 返回失败响应，可指定HTTP状态码、业务错误码和错误消息。
// 参数c：Gin请求上下文。
// 参数httpStatus：HTTP状态码（如400、401、500）。
// 参数code：业务错误码。
// 参数message：人类可读的错误描述。
func Fail(c *gin.Context, httpStatus int, code int, message string) {
	// 以JSON格式返回失败响应
	c.JSON(httpStatus, Response{
		Code:      code,         // 业务错误码
		Message:   message,      // 错误描述
		Data:      nil,          // 失败时数据为nil
		RequestID: RequestID(c), // 从上下文获取请求ID
	})
}

// NewPageResponse 根据分页数据和分页请求构建标准的分页响应。
// 参数items：当前页的数据项。
// 参数total：总数。
// 参数page：分页请求参数（会自动规范化）。
// 返回值：填充好的PageResponse。
func NewPageResponse(items any, total int64, page PageRequest) PageResponse {
	// 先规范化分页参数（页码和每页条数的合理范围）
	page.Normalize()
	// 构建分页响应
	return PageResponse{
		Items:    items,         // 数据列表
		Total:    total,         // 总记录数
		Page:     page.Page,     // 当前页码
		PageSize: page.PageSize, // 每页记录数
	}
}

// Normalize 规范化分页请求参数到合理范围：
// - 页码不能小于1（默认1）
// - 每页条数范围1-100（默认20）
func (p *PageRequest) Normalize() {
	// 页码最小为1
	if p.Page < 1 {
		p.Page = 1
	}
	// 每页条数最小为1
	if p.PageSize < 1 {
		p.PageSize = 20
	}
	// 每页条数最大为100（防止过度查询）
	if p.PageSize > 100 {
		p.PageSize = 100
	}
}

// Limit 返回标准化的每页记录数（用于SQL LIMIT子句）。
// 返回值：有效的每页记录数（1-100之间）。
func (p PageRequest) Limit() int {
	// 小于1时使用默认值20
	if p.PageSize < 1 {
		return 20
	}
	// 大于100时截断为100
	if p.PageSize > 100 {
		return 100
	}
	return p.PageSize
}

// Offset 计算SQL查询的偏移量（用于OFFSET子句）。
// 返回值：从第几条记录开始查询。
func (p PageRequest) Offset() int {
	// 规范化页码
	page := p.Page
	if page < 1 {
		page = 1
	}
	// 偏移量 = (页码-1) * 每页条数
	return (page - 1) * p.Limit()
}

// RequestID 从Gin上下文中获取当前请求的唯一ID。
// 参数c：Gin请求上下文。
// 返回值：请求ID字符串，不存在时返回空字符串。
func RequestID(c *gin.Context) string {
	// 从上下文中取出RequestIDMiddleware存入的值
	value, ok := c.Get(requestIDKey)
	if !ok {
		// 无请求ID时返回空字符串
		return ""
	}
	// 类型断言为string
	requestID, _ := value.(string)
	return requestID
}

// newRequestID 生成一个新的16字节随机请求ID（32位十六进制字符串）。
// 使用crypto/rand确保加密安全性，生成失败时返回"unknown"作为降级处理。
// 返回值：32字符的十六进制请求ID。
func newRequestID() string {
	// 创建16字节的随机数缓冲区
	var buf [16]byte
	// 使用密码学安全随机数填充
	if _, err := rand.Read(buf[:]); err != nil {
		// 填充失败时返回降级值
		return "unknown"
	}
	// 转为十六进制字符串返回
	return hex.EncodeToString(buf[:])
}
