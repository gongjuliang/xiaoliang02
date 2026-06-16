// Package mcp 提供NATT服务端的MCP（Model Context Protocol）Streamable HTTP JSON-RPC接口。
// 支持Codex等AI工具自动发现和调用，提供客户端管理、隧道管理、仪表盘查询等工具。
// 使用Bearer Token鉴权，工具调用和发现操作写入审计日志。
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/logger"
	"nattserver/internal/model"

	"github.com/gin-gonic/gin"
)

// JSON-RPC 2.0和MCP协议相关常量。
const (
	jsonRPCVersion       = "2.0"        // JSON-RPC协议版本
	mcpProtocolLatest    = "2025-11-25" // 支持的最新MCP协议版本
	mcpProtocolFallback  = "2025-06-18" // 向后兼容的MCP协议版本
	jsonRPCParseError    = -32700       // JSON解析错误
	jsonRPCInvalidReq    = -32600       // 无效请求
	jsonRPCMethodMissing = -32601       // 方法未找到
	jsonRPCInvalidParams = -32602       // 无效参数
	jsonRPCInternalError = -32603       // 内部错误
)

// TunnelRuntime 隧道运行时管理接口，抽象出启动、停止和断开连接的运行时能力。
type TunnelRuntime interface {
	StartTunnel(ctx context.Context, id int64) (model.Tunnel, error)
	StopTunnel(ctx context.Context, id int64) (model.Tunnel, error)
	DisconnectTunnel(id int64)
}

// serverHandler MCP服务端处理器，封装隧道配置、数据库、日志和运行时控制接口。
type serverHandler struct {
	tunnelCfg config.TunnelConfig // 隧道端口范围配置
	database  *sql.DB             // 数据库连接
	log       *logger.Logger      // 日志记录器
	runtime   TunnelRuntime       // 运行时隧道控制器
}

// jsonRPCRequest JSON-RPC 2.0请求结构体。
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`          // JSON-RPC版本号
	ID      json.RawMessage `json:"id,omitempty"`     // 请求ID（通知消息可省略）
	Method  string          `json:"method"`           // 调用的方法名
	Params  json.RawMessage `json:"params,omitempty"` // 方法参数（JSON）
}

// jsonRPCResponse JSON-RPC 2.0响应结构体。
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`          // JSON-RPC版本号
	ID      json.RawMessage `json:"id,omitempty"`     // 对应的请求ID
	Result  any             `json:"result,omitempty"` // 成功时的结果
	Error   *jsonRPCError   `json:"error,omitempty"`  // 失败时的错误信息
}

// jsonRPCError JSON-RPC错误对象。
type jsonRPCError struct {
	Code    int    `json:"code"`    // 错误码
	Message string `json:"message"` // 错误描述
}

// toolCallParams MCP工具调用参数（tools/call）。
type toolCallParams struct {
	Name      string          `json:"name"`      // 要调用的工具名称
	Arguments json.RawMessage `json:"arguments"` // 工具参数（JSON）
}

// mcpTool MCP工具定义，用于tools/list的响应。
type mcpTool struct {
	Name        string         `json:"name"`        // 工具名称
	Description string         `json:"description"` // 工具功能描述
	InputSchema map[string]any `json:"inputSchema"` // 输入参数JSON Schema
}

// mcpToolResult MCP工具调用结果。
type mcpToolResult struct {
	Content           []mcpContent    `json:"content"`                     // 文本内容列表
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"` // 结构化JSON数据
	IsError           bool            `json:"isError"`                     // 是否为错误结果
}

// mcpContent MCP内容块，承载工具返回的文本信息。
type mcpContent struct {
	Type string `json:"type"` // 内容类型（通常为"text"）
	Text string `json:"text"` // 文本内容
}

// pageParams MCP工具的分页请求参数。
type pageParams struct {
	Page     int `json:"page"`      // 页码（≥1，默认1）
	PageSize int `json:"page_size"` // 每页条数（1-100，默认20）
}

// idParams MCP工具的ID查询参数。
type idParams struct {
	ID int64 `json:"id"` // 资源唯一标识
}

// tunnelParams MCP创建隧道工具的参数。
type tunnelParams struct {
	Name       string `json:"name"`        // 隧道名称（必填）
	Protocol   string `json:"protocol"`    // 协议类型
	RemoteHost string `json:"remote_host"` // 公网监听地址
	RemotePort int    `json:"remote_port"` // 公网监听端口（必填）
	AutoStart  *bool  `json:"auto_start"`  // 是否自动启动（nil时默认true）
	Remark     string `json:"remark"`      // 备注信息
}

// pageResult MCP分页查询的统一返回格式。
type pageResult struct {
	Items    any   `json:"items"`     // 数据列表
	Total    int64 `json:"total"`     // 总记录数
	Page     int   `json:"page"`      // 当前页码
	PageSize int   `json:"page_size"` // 每页条数
}

// NewServerRouter 创建MCP服务的Gin路由器，配置Token鉴权中间件和JSON-RPC处理器。
// 参数cfg：MCP配置（启用状态和访问令牌）。
// 参数database/runtime：数据库和运行时依赖。
// 参数tunnelCfg：可选的隧道端口配置。
// 返回值：配置好的Gin Engine实例。
func NewServerRouter(cfg config.MCPConfig, database *sql.DB, log *logger.Logger, runtime TunnelRuntime, tunnelCfg ...config.TunnelConfig) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	if database != nil {
		_ = db.UpsertSetting(context.Background(), database, "mcp.enabled", strconv.FormatBool(cfg.Enabled))
		if strings.TrimSpace(cfg.AccessToken) != "" {
			_ = db.UpsertSetting(context.Background(), database, "mcp.access_token", cfg.AccessToken)
		}
	}
	RegisterServerRoutes(router, database, log, runtime, tunnelCfg...)

	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
	})
	return router
}

// RegisterServerRoutes 在Gin路由器上注册MCP端点：POST /mcp接受JSON-RPC请求，其他方法返回405。
func RegisterServerRoutes(router *gin.Engine, database *sql.DB, log *logger.Logger, runtime TunnelRuntime, tunnelCfg ...config.TunnelConfig) {
	handler := &serverHandler{
		tunnelCfg: resolveTunnelConfig(tunnelCfg),
		database:  database,
		log:       log,
		runtime:   runtime,
	}

	protected := router.Group("")
	protected.Use(tokenAuthMiddleware(database))
	protected.POST("/mcp", handler.handleJSONRPC)
	protected.GET("/mcp", methodNotAllowed)
	protected.DELETE("/mcp", methodNotAllowed)
	protected.PUT("/mcp", methodNotAllowed)
	protected.PATCH("/mcp", methodNotAllowed)
}

func methodNotAllowed(c *gin.Context) {
	c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
}

// handleJSONRPC 处理JSON-RPC请求分发：initialize/notifications/initialized/ping/tools/list/tools/call。
func (h *serverHandler) handleJSONRPC(c *gin.Context) {
	var req jsonRPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeJSONRPCError(c, nil, jsonRPCParseError, "JSON-RPC 请求格式错误")
		return
	}
	if req.JSONRPC != jsonRPCVersion || strings.TrimSpace(req.Method) == "" {
		writeJSONRPCError(c, req.ID, jsonRPCInvalidReq, "JSON-RPC 请求无效")
		return
	}

	switch req.Method {
	case "initialize":
		writeJSONRPCResult(c, req.ID, h.initialize(req.Params))
	case "notifications/initialized":
		if len(req.ID) == 0 {
			c.Status(http.StatusAccepted)
			return
		}
		writeJSONRPCResult(c, req.ID, gin.H{})
	case "ping":
		writeJSONRPCResult(c, req.ID, gin.H{})
	case "tools/list":
		h.auditMCP(c, "mcp_tools_list", "tools/list", req.Params)
		writeJSONRPCResult(c, req.ID, gin.H{"tools": serverTools()})
	case "tools/call":
		h.handleToolCall(c, req.ID, req.Params)
	default:
		writeJSONRPCError(c, req.ID, jsonRPCMethodMissing, "未知 MCP 方法")
	}
}

// initialize 处理MCP握手，协商协议版本并返回服务端能力（含tools支持）。
func (h *serverHandler) initialize(raw json.RawMessage) gin.H {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(raw, &params)
	version := mcpProtocolLatest
	if params.ProtocolVersion == mcpProtocolFallback {
		version = mcpProtocolFallback
	}
	return gin.H{
		"protocolVersion": version,
		"serverInfo": gin.H{
			"name":    "nattserver",
			"version": "1.0.0",
		},
		"capabilities": gin.H{
			"tools": gin.H{},
		},
	}
}

// handleToolCall 处理tools/call：解析工具名→执行工具→返回MCP结果（含审计日志）。
func (h *serverHandler) handleToolCall(c *gin.Context, id json.RawMessage, raw json.RawMessage) {
	var params toolCallParams
	if err := json.Unmarshal(raw, &params); err != nil || strings.TrimSpace(params.Name) == "" {
		h.auditMCP(c, "mcp_tool_call", "invalid_request", raw)
		writeJSONRPCError(c, id, jsonRPCInvalidParams, "MCP 工具调用参数无效")
		return
	}
	tool := strings.TrimSpace(params.Name)
	result, err := h.executeTool(c.Request.Context(), tool, params.Arguments)
	if err != nil {
		h.auditMCP(c, "mcp_tool_call", tool, params.Arguments)
		writeJSONRPCResult(c, id, toolErrorResult(err.Error()))
		return
	}
	h.auditMCP(c, "mcp_tool_call", tool, params.Arguments)
	writeJSONRPCResult(c, id, toolSuccessResult(result))
}

// executeTool 根据工具名称路由到对应的处理函数（list_clients/get_client/list_tunnels等）。
func (h *serverHandler) executeTool(ctx context.Context, tool string, raw json.RawMessage) (any, error) {
	switch tool {
	case "server.list_clients":
		return h.listClients(ctx, raw)
	case "server.get_client":
		return h.getClient(ctx, raw)
	case "server.list_tunnels":
		return h.listTunnels(ctx, raw)
	case "server.create_tunnel":
		return h.createTunnel(ctx, raw)
	case "server.start_tunnel":
		return h.runTunnelAction(ctx, raw, h.startTunnelByID, "mcp_tunnel_start", "mcp started tunnel")
	case "server.stop_tunnel":
		return h.runTunnelAction(ctx, raw, h.stopTunnelByID, "mcp_tunnel_stop", "mcp stopped tunnel")
	case "server.delete_tunnel":
		return h.deleteTunnel(ctx, raw)
	case "server.get_dashboard":
		return h.getDashboard(ctx)
	default:
		return nil, fmt.Errorf("unknown MCP tool")
	}
}

// listClients 处理server.list_clients工具：分页查询客户端列表。
func (h *serverHandler) listClients(ctx context.Context, raw json.RawMessage) (any, error) {
	params, err := bindPageParams(raw)
	if err != nil {
		return nil, err
	}
	clients, total, err := db.ListClients(ctx, h.database, params.limit(), params.offset())
	if err != nil {
		return nil, translateDBError(err, "list clients failed")
	}
	return pageResult{Items: clients, Total: total, Page: params.Page, PageSize: params.PageSize}, nil
}

// getClient 处理server.get_client工具：按ID查询单个客户端。
func (h *serverHandler) getClient(ctx context.Context, raw json.RawMessage) (any, error) {
	var params idParams
	if err := bindParams(raw, &params); err != nil {
		return nil, err
	}
	if params.ID <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	client, err := db.GetClientByID(ctx, h.database, params.ID)
	if err != nil {
		return nil, translateDBError(err, "get client failed")
	}
	return client, nil
}

// listTunnels 处理server.list_tunnels工具：分页查询隧道列表（含流量统计数据）。
func (h *serverHandler) listTunnels(ctx context.Context, raw json.RawMessage) (any, error) {
	params, err := bindPageParams(raw)
	if err != nil {
		return nil, err
	}
	tunnels, total, err := db.ListTunnels(ctx, h.database, 0, params.limit(), params.offset())
	if err != nil {
		return nil, translateDBError(err, "list tunnels failed")
	}
	return pageResult{Items: tunnels, Total: total, Page: params.Page, PageSize: params.PageSize}, nil
}

// getDashboard 处理server.get_dashboard工具：获取在线客户端/隧道数和流量摘要。
func (h *serverHandler) getDashboard(ctx context.Context) (any, error) {
	summary, err := db.GetDashboardSummary(ctx, h.database)
	if err != nil {
		return nil, translateDBError(err, "get dashboard failed")
	}
	return summary, nil
}

// createTunnel 处理server.create_tunnel工具：创建隧道→生成密钥→返回隧道信息和密钥明文。
func (h *serverHandler) createTunnel(ctx context.Context, raw json.RawMessage) (any, error) {
	var params tunnelParams
	if err := bindParams(raw, &params); err != nil {
		return nil, err
	}
	if err := h.validateTunnelParams(&params); err != nil {
		return nil, err
	}
	tunnel, err := db.CreateTunnel(ctx, h.database, db.CreateTunnelParams{
		Name:       params.Name,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: params.RemoteHost,
		RemotePort: params.RemotePort,
		AutoStart:  params.autoStartValue(),
		Remark:     params.Remark,
	})
	if err != nil {
		return nil, translateDBError(err, "create tunnel failed")
	}
	secret, secretHash, secretHint, err := buildMCPSecret()
	if err != nil {
		return nil, err
	}
	key, err := db.CreateTunnelKey(ctx, h.database, db.CreateTunnelKeyParams{TunnelID: tunnel.ID, SecretHash: secretHash, SecretHint: secretHint, SecretPlain: secret})
	if err != nil {
		return nil, translateDBError(err, "create tunnel key failed")
	}
	h.audit(ctx, "mcp_tunnel_create", tunnel.ID, fmt.Sprintf("mcp created tunnel %s", tunnel.Name))
	return gin.H{"tunnel": tunnel, "key": key, "secret": secret}, nil
}

// deleteTunnel 处理server.delete_tunnel工具：禁用密钥→停止运行时→断开连接→删除记录。
func (h *serverHandler) deleteTunnel(ctx context.Context, raw json.RawMessage) (any, error) {
	var params idParams
	if err := bindParams(raw, &params); err != nil {
		return nil, err
	}
	if params.ID <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	tunnel, err := db.GetTunnelByID(ctx, h.database, params.ID)
	if err != nil {
		return nil, translateDBError(err, "load tunnel before delete failed")
	}
	if _, err := db.SetTunnelKeyStatus(ctx, h.database, params.ID, model.TunnelKeyStatusDisabled); err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, translateDBError(err, "disable tunnel key before delete failed")
	}
	if h.runtime != nil {
		if _, err := h.runtime.StopTunnel(ctx, params.ID); err != nil {
			return nil, translateDBError(err, "stop tunnel before delete failed")
		}
		h.runtime.DisconnectTunnel(params.ID)
	}
	deleted, err := db.DeleteTunnel(ctx, h.database, params.ID)
	if err != nil {
		return nil, translateDBError(err, "delete tunnel failed")
	}
	h.audit(ctx, "mcp_tunnel_delete", tunnel.ID, fmt.Sprintf("mcp deleted tunnel %s", tunnel.Name))
	return deleted, nil
}

// runTunnelAction 执行隧道运行时操作的通用方法（启动/停止），解析ID参数并记录审计日志。
func (h *serverHandler) runTunnelAction(ctx context.Context, raw json.RawMessage, actionFn func(context.Context, int64) (model.Tunnel, error), action string, contentPrefix string) (any, error) {
	var params idParams
	if err := bindParams(raw, &params); err != nil {
		return nil, err
	}
	if params.ID <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	tunnel, err := actionFn(ctx, params.ID)
	if err != nil {
		return nil, translateDBError(err, "tunnel runtime action failed")
	}
	h.audit(ctx, action, tunnel.ID, fmt.Sprintf("%s %s", contentPrefix, tunnel.Name))
	return tunnel, nil
}

// startTunnelByID 按ID启动隧道（优先使用运行时控制器，否则仅更新数据库状态）。
func (h *serverHandler) startTunnelByID(ctx context.Context, id int64) (model.Tunnel, error) {
	if h.runtime != nil {
		return h.runtime.StartTunnel(ctx, id)
	}
	return db.SetTunnelStatus(ctx, h.database, id, model.TunnelStatusRunning, "")
}

// stopTunnelByID 按ID停止隧道（优先使用运行时控制器，否则仅更新数据库状态）。
func (h *serverHandler) stopTunnelByID(ctx context.Context, id int64) (model.Tunnel, error) {
	if h.runtime != nil {
		return h.runtime.StopTunnel(ctx, id)
	}
	return db.SetTunnelStopped(ctx, h.database, id, "")
}

// validateTunnelParams 校验隧道创建参数：名称必填、协议仅支持tcp、端口在配置范围内。
func (h *serverHandler) validateTunnelParams(params *tunnelParams) error {
	params.Name = strings.TrimSpace(params.Name)
	params.Protocol = strings.ToLower(strings.TrimSpace(params.Protocol))
	params.RemoteHost = strings.TrimSpace(params.RemoteHost)
	if params.Protocol == "" {
		params.Protocol = string(model.TunnelProtocolTCP)
	}
	if params.RemoteHost == "" {
		params.RemoteHost = "0.0.0.0"
	}

	switch {
	case params.Name == "":
		return fmt.Errorf("name is required")
	case params.Protocol != string(model.TunnelProtocolTCP):
		return fmt.Errorf("only tcp protocol is supported")
	case !validPort(params.RemotePort):
		return fmt.Errorf("remote_port must be between 1 and 65535")
	case params.RemotePort < h.tunnelCfg.RemotePortMin || params.RemotePort > h.tunnelCfg.RemotePortMax:
		return fmt.Errorf("remote_port must be between %d and %d", h.tunnelCfg.RemotePortMin, h.tunnelCfg.RemotePortMax)
	default:
		return nil
	}
}

// autoStartValue 获取隧道auto_start值，nil时默认为true（创建后进入等待状态）。
func (p tunnelParams) autoStartValue() bool {
	if p.AutoStart == nil {
		return true
	}
	return *p.AutoStart
}

// audit 写入MCP操作的审计日志（以"mcp"为操作者）。
func (h *serverHandler) audit(ctx context.Context, action string, tunnelID int64, content string) {
	_ = db.InsertAuditLog(ctx, h.database, "mcp", action, "tunnel", strconv.FormatInt(tunnelID, 10), content, "")
}

// auditMCP 写入MCP工具调用的审计日志（含敏感参数脱敏处理）。
func (h *serverHandler) auditMCP(c *gin.Context, action string, target string, raw json.RawMessage) {
	if h.database == nil {
		return
	}
	content := fmt.Sprintf("MCP JSON-RPC action=%s target=%s params=%s", action, target, sanitizeMCPParams(raw))
	_ = db.InsertAuditLog(c.Request.Context(), h.database, "mcp", action, "mcp_tool", target, content, c.ClientIP())
}

// resolveTunnelConfig 解析隧道配置，未提供时使用默认值。
func resolveTunnelConfig(values []config.TunnelConfig) config.TunnelConfig {
	if len(values) > 0 {
		return values[0]
	}
	return config.Default().Tunnel
}

// tokenAuthMiddleware 创建MCP的Bearer Token鉴权中间件。
// 验证流程：检查MCP启用状态→读取数据库中的access_token→匹配请求中的Bearer或X-MCP-Token头。
func tokenAuthMiddleware(database *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		enabled, err := db.GetSetting(c.Request.Context(), database, "mcp.enabled")
		if errors.Is(err, db.ErrNotFound) || !strings.EqualFold(enabled, "true") {
			c.JSON(http.StatusForbidden, gin.H{"error": "mcp disabled"})
			c.Abort()
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load mcp settings failed"})
			c.Abort()
			return
		}
		accessToken, err := db.GetSetting(c.Request.Context(), database, "mcp.access_token")
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "load mcp settings failed"})
			}
			c.Abort()
			return
		}
		if strings.TrimSpace(accessToken) == "" || extractToken(c) != accessToken {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// extractToken 从请求中提取MCP Token：优先Authorization: Bearer头，其次X-MCP-Token头。
func extractToken(c *gin.Context) string {
	authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return strings.TrimSpace(c.GetHeader("X-MCP-Token"))
}

// bindPageParams 绑定并规范化MCP分页参数（默认page=1, pageSize=20）。
func bindPageParams(raw json.RawMessage) (pageParams, error) {
	var params pageParams
	if err := bindParams(raw, &params); err != nil {
		return pageParams{}, err
	}
	params.normalize()
	return params, nil
}

// bindParams 将JSON参数反序列化为目标结构体，空/Null参数不报错。
func bindParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("invalid MCP tool parameters")
	}
	return nil
}

// normalize 规范化分页参数：Page≥1，PageSize∈[1,100]，默认20。
func (p *pageParams) normalize() {
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

// limit 返回分页查询的limit值（1-100，默认20）。
func (p pageParams) limit() int {
	if p.PageSize < 1 {
		return 20
	}
	if p.PageSize > 100 {
		return 100
	}
	return p.PageSize
}

// offset 计算分页查询的offset值：(page-1)×pageSize。
func (p pageParams) offset() int {
	page := p.Page
	if page < 1 {
		page = 1
	}
	return (page - 1) * p.limit()
}

// validPort 检查端口号是否在有效范围内（1-65535）。
func validPort(port int) bool {
	return port > 0 && port <= 65535
}

// buildMCPSecret 生成MCP隧道密钥：随机明文→SM3加盐哈希→密钥提示摘要。
// 返回值：密钥明文、哈希、提示和错误。
func buildMCPSecret() (plain string, hash string, hint string, err error) {
	plain, err = auth.GenerateClientSecret()
	if err != nil {
		return "", "", "", err
	}
	hash, err = auth.HashPassword(plain)
	if err != nil {
		return "", "", "", err
	}
	return plain, hash, auth.SecretHint(plain), nil
}

// writeJSONRPCResult 写入JSON-RPC 2.0成功响应。
func writeJSONRPCResult(c *gin.Context, id json.RawMessage, result any) {
	c.JSON(http.StatusOK, jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: id, Result: result})
}

// writeJSONRPCError 写入JSON-RPC 2.0错误响应。
func writeJSONRPCError(c *gin.Context, id json.RawMessage, code int, message string) {
	c.JSON(http.StatusOK, jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: id, Error: &jsonRPCError{Code: code, Message: message}})
}

// toolSuccessResult 将数据包装为MCP工具成功结果（JSON格式化后放入content）。
func toolSuccessResult(data any) mcpToolResult {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	return mcpToolResult{
		Content:           []mcpContent{{Type: "text", Text: string(raw)}},
		StructuredContent: raw,
		IsError:           false,
	}
}

// toolErrorResult 创建MCP工具错误结果（IsError=true）。
func toolErrorResult(message string) mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: message}},
		IsError: true,
	}
}

// translateDBError 将数据库层错误转换为用户友好的英文错误信息。
func translateDBError(err error, fallback string) error {
	switch {
	case errors.Is(err, db.ErrNotFound):
		return fmt.Errorf("resource not found")
	case errors.Is(err, db.ErrConflict):
		return fmt.Errorf("resource conflict")
	default:
		return fmt.Errorf("%s", fallback)
	}
}

// serverTools 返回服务端MCP工具列表定义（8个工具）。
func serverTools() []mcpTool {
	return []mcpTool{
		{Name: "server.list_clients", Description: "List registered NATT clients.", InputSchema: pageSchema()},
		{Name: "server.get_client", Description: "Get one NATT client by id.", InputSchema: idSchema()},
		{Name: "server.list_tunnels", Description: "List server TCP tunnels.", InputSchema: pageSchema()},
		{Name: "server.create_tunnel", Description: "Create a TCP tunnel on the server.", InputSchema: tunnelCreateSchema()},
		{Name: "server.start_tunnel", Description: "Start a server tunnel by id.", InputSchema: idSchema()},
		{Name: "server.stop_tunnel", Description: "Stop a server tunnel by id.", InputSchema: idSchema()},
		{Name: "server.delete_tunnel", Description: "Delete a server tunnel by id.", InputSchema: idSchema()},
		{Name: "server.get_dashboard", Description: "Get server dashboard status and traffic summary.", InputSchema: objectSchema(nil, nil)},
	}
}

// pageSchema 生成分页参数（page/page_size）的JSON Schema。
func pageSchema() map[string]any {
	return objectSchema(map[string]any{
		"page":      map[string]any{"type": "integer", "minimum": 1},
		"page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
	}, nil)
}

// idSchema 生成ID参数（必填）的JSON Schema。
func idSchema() map[string]any {
	return objectSchema(map[string]any{
		"id": map[string]any{"type": "integer", "minimum": 1},
	}, []string{"id"})
}

// tunnelCreateSchema 生成创建隧道参数的JSON Schema（name+remote_port必填）。
func tunnelCreateSchema() map[string]any {
	return objectSchema(map[string]any{
		"name":        map[string]any{"type": "string"},
		"protocol":    map[string]any{"type": "string", "enum": []string{"tcp"}},
		"remote_host": map[string]any{"type": "string"},
		"remote_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"auto_start":  map[string]any{"type": "boolean", "default": true, "description": "Omitted value defaults to true and creates a waiting tunnel."},
		"remark":      map[string]any{"type": "string"},
	}, []string{"name", "remote_port"})
}

// objectSchema 构建JSON Schema的object类型定义，可指定属性列表和必填字段。
func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// sanitizeMCPParams 审计日志中脱敏MCP调用参数（secret/token/password/key字段替换为[已脱敏]）。
func sanitizeMCPParams(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "参数无法解析"
	}
	sanitized := sanitizeMCPValue(value)
	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return "参数无法编码"
	}
	return string(encoded)
}

// sanitizeMCPValue 递归脱敏MCP参数值中的敏感字段（map递归处理，array逐元素处理）。
func sanitizeMCPValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveMCPKey(key) {
				out[key] = "[已脱敏]"
				continue
			}
			out[key] = sanitizeMCPValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeMCPValue(item))
		}
		return out
	default:
		return value
	}
}

// isSensitiveMCPKey 判断参数键名是否包含敏感词（secret/token/password/key）。
func isSensitiveMCPKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "secret") || strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "key")
}
