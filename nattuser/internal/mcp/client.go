package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/logger"
	"nattuser/internal/model"

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
)

type clientHandler struct {
	database *sql.DB
	log      *logger.Logger
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

type pageResult struct {
	Items    any   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

type networkInterface struct {
	Name         string   `json:"name"`
	Index        int      `json:"index"`
	MTU          int      `json:"mtu"`
	Flags        string   `json:"flags"`
	HardwareAddr string   `json:"hardware_addr"`
	Addrs        []string `json:"addrs"`
}

type networkStatus struct {
	Hostname   string             `json:"hostname"`
	Interfaces []networkInterface `json:"interfaces"`
}

type localTunnelStatus struct {
	ServerConnectionID int64                        `json:"server_connection_id"`
	ServerName         string                       `json:"server_name"`
	ServerHost         string                       `json:"server_host"`
	ServerPort         int                          `json:"server_port"`
	DataPort           int                          `json:"data_port"`
	RemotePort         int                          `json:"remote_port"`
	Status             model.ServerConnectionStatus `json:"status"`
	LastError          string                       `json:"last_error"`
	UpdatedAt          string                       `json:"updated_at"`
}

func NewClientRouter(cfg config.MCPConfig, database *sql.DB, log *logger.Logger) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	if database != nil {
		_ = db.UpsertSetting(context.Background(), database, "mcp.enabled", strconv.FormatBool(cfg.Enabled))
		if strings.TrimSpace(cfg.AccessToken) != "" {
			_ = db.UpsertSetting(context.Background(), database, "mcp.access_token", cfg.AccessToken)
		}
	}
	RegisterClientRoutes(router, database, log)

	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
	})
	return router
}

func RegisterClientRoutes(router *gin.Engine, database *sql.DB, log *logger.Logger) {
	handler := &clientHandler{
		database: database,
		log:      log,
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

func (h *clientHandler) handleJSONRPC(c *gin.Context) {
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
		writeJSONRPCResult(c, req.ID, gin.H{"tools": clientTools()})
	case "tools/call":
		h.handleToolCall(c, req.ID, req.Params)
	default:
		writeJSONRPCError(c, req.ID, jsonRPCMethodMissing, "未知 MCP 方法")
	}
}

func (h *clientHandler) initialize(raw json.RawMessage) gin.H {
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
			"name":    "nattuser",
			"version": "1.0.0",
		},
		"capabilities": gin.H{
			"tools": gin.H{},
		},
	}
}

func (h *clientHandler) handleToolCall(c *gin.Context, id json.RawMessage, raw json.RawMessage) {
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

func (h *clientHandler) executeTool(ctx context.Context, tool string, raw json.RawMessage) (any, error) {
	switch tool {
	case "client.list_tunnel_connections", "client.list_servers":
		return h.listServers(ctx, raw)
	case "client.connect_tunnel", "client.connect_server":
		return h.setServerStatus(ctx, raw, model.ServerConnectionStatusConnected, "mcp_server_connect", "mcp connected server")
	case "client.disconnect_tunnel", "client.disconnect_server":
		return h.setServerStatus(ctx, raw, model.ServerConnectionStatusStopped, "mcp_server_disconnect", "mcp disconnected server")
	case "client.list_tunnels":
		return h.listTunnels(ctx, raw)
	case "client.get_network_status":
		return collectNetworkStatus()
	default:
		return nil, fmt.Errorf("unknown MCP tool")
	}
}

func (h *clientHandler) listServers(ctx context.Context, raw json.RawMessage) (any, error) {
	params, err := bindPageParams(raw)
	if err != nil {
		return nil, err
	}
	servers, total, err := db.ListServerConnections(ctx, h.database, params.limit(), params.offset())
	if err != nil {
		return nil, translateDBError(err, "list servers failed")
	}
	return pageResult{Items: servers, Total: total, Page: params.Page, PageSize: params.PageSize}, nil
}

func (h *clientHandler) setServerStatus(ctx context.Context, raw json.RawMessage, status model.ServerConnectionStatus, action string, contentPrefix string) (any, error) {
	var params idParams
	if err := bindParams(raw, &params); err != nil {
		return nil, err
	}
	if params.ID <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	connection, err := db.SetServerConnectionStatus(ctx, h.database, params.ID, status, "")
	if err != nil {
		return nil, translateDBError(err, "set server connection status failed")
	}
	h.audit(ctx, action, connection.ID, fmt.Sprintf("%s %s", contentPrefix, connection.Name))
	return connection, nil
}

func (h *clientHandler) listTunnels(ctx context.Context, raw json.RawMessage) (any, error) {
	params, err := bindPageParams(raw)
	if err != nil {
		return nil, err
	}
	connections, total, err := db.ListServerConnections(ctx, h.database, params.limit(), params.offset())
	if err != nil {
		return nil, translateDBError(err, "list local tunnels failed")
	}
	items := make([]localTunnelStatus, 0, len(connections))
	for _, connection := range connections {
		items = append(items, localTunnelStatus{
			ServerConnectionID: connection.ID,
			ServerName:         connection.Name,
			ServerHost:         connection.ServerHost,
			ServerPort:         connection.ServerPort,
			DataPort:           connection.DataPort,
			RemotePort:         connection.RemotePort,
			Status:             connection.Status,
			LastError:          connection.LastError,
			UpdatedAt:          connection.UpdatedAt,
		})
	}
	return pageResult{Items: items, Total: total, Page: params.Page, PageSize: params.PageSize}, nil
}

func (h *clientHandler) audit(ctx context.Context, action string, connectionID int64, content string) {
	_ = db.InsertAuditLog(ctx, h.database, "mcp", action, "server_connection", strconv.FormatInt(connectionID, 10), content, "")
}

func (h *clientHandler) auditMCP(c *gin.Context, action string, target string, raw json.RawMessage) {
	if h.database == nil {
		return
	}
	content := fmt.Sprintf("MCP JSON-RPC action=%s target=%s params=%s", action, target, sanitizeMCPParams(raw))
	_ = db.InsertAuditLog(c.Request.Context(), h.database, "mcp", action, "mcp_tool", target, content, c.ClientIP())
}

func collectNetworkStatus() (networkStatus, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return networkStatus{}, err
	}
	rawInterfaces, err := net.Interfaces()
	if err != nil {
		return networkStatus{}, err
	}

	interfaces := make([]networkInterface, 0, len(rawInterfaces))
	for _, item := range rawInterfaces {
		addrs, _ := item.Addrs()
		addrStrings := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			addrStrings = append(addrStrings, addr.String())
		}
		interfaces = append(interfaces, networkInterface{
			Name:         item.Name,
			Index:        item.Index,
			MTU:          item.MTU,
			Flags:        item.Flags.String(),
			HardwareAddr: item.HardwareAddr.String(),
			Addrs:        addrStrings,
		})
	}
	return networkStatus{Hostname: hostname, Interfaces: interfaces}, nil
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

func clientTools() []mcpTool {
	return []mcpTool{
		{Name: "client.list_tunnel_connections", Description: "List configured NATT server tunnel connections.", InputSchema: pageSchema()},
		{Name: "client.list_servers", Description: "Alias of client.list_tunnel_connections.", InputSchema: pageSchema()},
		{Name: "client.connect_tunnel", Description: "Mark one tunnel connection as connected.", InputSchema: idSchema()},
		{Name: "client.connect_server", Description: "Alias of client.connect_tunnel.", InputSchema: idSchema()},
		{Name: "client.disconnect_tunnel", Description: "Stop one tunnel connection.", InputSchema: idSchema()},
		{Name: "client.disconnect_server", Description: "Alias of client.disconnect_tunnel.", InputSchema: idSchema()},
		{Name: "client.list_tunnels", Description: "List local tunnel status summaries.", InputSchema: pageSchema()},
		{Name: "client.get_network_status", Description: "Get local host network interface status.", InputSchema: objectSchema(nil, nil)},
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
