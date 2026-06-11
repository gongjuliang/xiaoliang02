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

const (
	jsonRPCVersion       = "2.0"
	mcpProtocolLatest    = "2025-11-25"
	mcpProtocolFallback  = "2025-06-18"
	jsonRPCParseError    = -32700
	jsonRPCInvalidReq    = -32600
	jsonRPCMethodMissing = -32601
	jsonRPCInvalidParams = -32602
	jsonRPCInternalError = -32603
)

type TunnelRuntime interface {
	StartTunnel(ctx context.Context, id int64) (model.Tunnel, error)
	StopTunnel(ctx context.Context, id int64) (model.Tunnel, error)
	DisconnectTunnel(id int64)
}

type serverHandler struct {
	tunnelCfg config.TunnelConfig
	database  *sql.DB
	log       *logger.Logger
	runtime   TunnelRuntime
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpToolResult struct {
	Content           []mcpContent    `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type pageParams struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

type idParams struct {
	ID int64 `json:"id"`
}

type tunnelParams struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	AutoStart  bool   `json:"auto_start"`
	Remark     string `json:"remark"`
}

type pageResult struct {
	Items    any   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

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

func (h *serverHandler) getDashboard(ctx context.Context) (any, error) {
	summary, err := db.GetDashboardSummary(ctx, h.database)
	if err != nil {
		return nil, translateDBError(err, "get dashboard failed")
	}
	return summary, nil
}

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
		AutoStart:  params.AutoStart,
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

func (h *serverHandler) startTunnelByID(ctx context.Context, id int64) (model.Tunnel, error) {
	if h.runtime != nil {
		return h.runtime.StartTunnel(ctx, id)
	}
	return db.SetTunnelStatus(ctx, h.database, id, model.TunnelStatusRunning, "")
}

func (h *serverHandler) stopTunnelByID(ctx context.Context, id int64) (model.Tunnel, error) {
	if h.runtime != nil {
		return h.runtime.StopTunnel(ctx, id)
	}
	return db.SetTunnelStopped(ctx, h.database, id, "")
}

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

func (h *serverHandler) audit(ctx context.Context, action string, tunnelID int64, content string) {
	_ = db.InsertAuditLog(ctx, h.database, "mcp", action, "tunnel", strconv.FormatInt(tunnelID, 10), content, "")
}

func (h *serverHandler) auditMCP(c *gin.Context, action string, target string, raw json.RawMessage) {
	if h.database == nil {
		return
	}
	content := fmt.Sprintf("MCP JSON-RPC action=%s target=%s params=%s", action, target, sanitizeMCPParams(raw))
	_ = db.InsertAuditLog(c.Request.Context(), h.database, "mcp", action, "mcp_tool", target, content, c.ClientIP())
}

func resolveTunnelConfig(values []config.TunnelConfig) config.TunnelConfig {
	if len(values) > 0 {
		return values[0]
	}
	return config.Default().Tunnel
}

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

func extractToken(c *gin.Context) string {
	authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return strings.TrimSpace(c.GetHeader("X-MCP-Token"))
}

func bindPageParams(raw json.RawMessage) (pageParams, error) {
	var params pageParams
	if err := bindParams(raw, &params); err != nil {
		return pageParams{}, err
	}
	params.normalize()
	return params, nil
}

func bindParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("invalid MCP tool parameters")
	}
	return nil
}

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

func (p pageParams) limit() int {
	if p.PageSize < 1 {
		return 20
	}
	if p.PageSize > 100 {
		return 100
	}
	return p.PageSize
}

func (p pageParams) offset() int {
	page := p.Page
	if page < 1 {
		page = 1
	}
	return (page - 1) * p.limit()
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}

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

func writeJSONRPCResult(c *gin.Context, id json.RawMessage, result any) {
	c.JSON(http.StatusOK, jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: id, Result: result})
}

func writeJSONRPCError(c *gin.Context, id json.RawMessage, code int, message string) {
	c.JSON(http.StatusOK, jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: id, Error: &jsonRPCError{Code: code, Message: message}})
}

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

func toolErrorResult(message string) mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: message}},
		IsError: true,
	}
}

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

func pageSchema() map[string]any {
	return objectSchema(map[string]any{
		"page":      map[string]any{"type": "integer", "minimum": 1},
		"page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
	}, nil)
}

func idSchema() map[string]any {
	return objectSchema(map[string]any{
		"id": map[string]any{"type": "integer", "minimum": 1},
	}, []string{"id"})
}

func tunnelCreateSchema() map[string]any {
	return objectSchema(map[string]any{
		"name":        map[string]any{"type": "string"},
		"protocol":    map[string]any{"type": "string", "enum": []string{"tcp"}},
		"remote_host": map[string]any{"type": "string"},
		"remote_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"auto_start":  map[string]any{"type": "boolean"},
		"remark":      map[string]any{"type": "string"},
	}, []string{"name", "remote_port"})
}

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

func isSensitiveMCPKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "secret") || strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "key")
}
