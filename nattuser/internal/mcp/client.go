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

type clientHandler struct {
	database *sql.DB
	log      *logger.Logger
}

type mcpRequest struct {
	Tool   string          `json:"tool"`
	Params json.RawMessage `json:"params"`
}

type mcpResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
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
		writeFail(c, http.StatusNotFound, "resource not found")
	})
	return router
}

func RegisterClientRoutes(router *gin.Engine, database *sql.DB, log *logger.Logger) {
	handler := &clientHandler{
		database: database,
		log:      log,
	}

	group := router.Group("/mcp")
	group.GET("/health", func(c *gin.Context) {
		writeOK(c, gin.H{"status": "ok"})
	})

	protected := group.Group("")
	protected.Use(tokenAuthMiddleware(database))
	protected.POST("/tools/call", handler.callTool)
}

func (h *clientHandler) callTool(c *gin.Context) {
	var req mcpRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Tool) == "" {
		writeFail(c, http.StatusBadRequest, "invalid MCP tool request")
		h.auditToolCall(c, "invalid_request", nil)
		return
	}
	tool := strings.TrimSpace(req.Tool)
	defer h.auditToolCall(c, tool, req.Params)

	// Dispatch is explicit rather than reflective, which keeps the MCP surface
	// limited to the documented client.* tools.
	switch tool {
	case "client.list_tunnel_connections", "client.list_servers":
		h.listServers(c, req.Params)
	case "client.connect_tunnel", "client.connect_server":
		h.connectServer(c, req.Params)
	case "client.disconnect_tunnel", "client.disconnect_server":
		h.disconnectServer(c, req.Params)
	case "client.list_tunnels":
		h.listTunnels(c, req.Params)
	case "client.get_network_status":
		h.getNetworkStatus(c)
	default:
		writeFail(c, http.StatusBadRequest, "unknown MCP tool")
	}
}

func (h *clientHandler) listServers(c *gin.Context, raw json.RawMessage) {
	params, ok := bindPageParams(c, raw)
	if !ok {
		return
	}
	servers, total, err := db.ListServerConnections(c.Request.Context(), h.database, params.limit(), params.offset())
	if err != nil {
		h.writeError(c, err, "list servers failed")
		return
	}
	writeOK(c, pageResult{Items: servers, Total: total, Page: params.Page, PageSize: params.PageSize})
}

func (h *clientHandler) getNetworkStatus(c *gin.Context) {
	status, err := collectNetworkStatus()
	if err != nil {
		h.writeError(c, err, "get network status failed")
		return
	}
	writeOK(c, status)
}

func (h *clientHandler) connectServer(c *gin.Context, raw json.RawMessage) {
	h.setServerStatus(c, raw, model.ServerConnectionStatusConnected, "mcp_server_connect", "mcp connected server")
}

func (h *clientHandler) disconnectServer(c *gin.Context, raw json.RawMessage) {
	h.setServerStatus(c, raw, model.ServerConnectionStatusStopped, "mcp_server_disconnect", "mcp disconnected server")
}

func (h *clientHandler) setServerStatus(c *gin.Context, raw json.RawMessage, status model.ServerConnectionStatus, action string, contentPrefix string) {
	var params idParams
	if !bindParams(c, raw, &params) {
		return
	}
	if params.ID <= 0 {
		writeFail(c, http.StatusBadRequest, "id is required")
		return
	}
	connection, err := db.SetServerConnectionStatus(c.Request.Context(), h.database, params.ID, status, "")
	if err != nil {
		h.writeError(c, err, "set server connection status failed")
		return
	}
	h.audit(c, action, connection.ID, fmt.Sprintf("%s %s", contentPrefix, connection.Name))
	writeOK(c, connection)
}

func (h *clientHandler) listTunnels(c *gin.Context, raw json.RawMessage) {
	params, ok := bindPageParams(c, raw)
	if !ok {
		return
	}
	connections, total, err := db.ListServerConnections(c.Request.Context(), h.database, params.limit(), params.offset())
	if err != nil {
		h.writeError(c, err, "list local tunnels failed")
		return
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
	writeOK(c, pageResult{Items: items, Total: total, Page: params.Page, PageSize: params.PageSize})
}

func (h *clientHandler) audit(c *gin.Context, action string, connectionID int64, content string) {
	_ = db.InsertAuditLog(c.Request.Context(), h.database, "mcp", action, "server_connection", strconv.FormatInt(connectionID, 10), content, c.ClientIP())
}

func (h *clientHandler) auditToolCall(c *gin.Context, tool string, raw json.RawMessage) {
	if h.database == nil {
		return
	}
	status := c.Writer.Status()
	outcome := "成功"
	if status < 200 || status >= 300 {
		outcome = "失败"
	}
	content := fmt.Sprintf("MCP 工具调用%s tool=%s status=%d params=%s", outcome, tool, status, sanitizeMCPParams(raw))
	_ = db.InsertAuditLog(c.Request.Context(), h.database, "mcp", "mcp_tool_call", "mcp_tool", tool, content, c.ClientIP())
}

func (h *clientHandler) writeError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		writeFail(c, http.StatusNotFound, "resource not found")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		writeFail(c, http.StatusConflict, "resource conflict")
		return
	}
	if h.log != nil {
		h.log.Errorf("mcp %s: %v", fallback, err)
	}
	writeFail(c, http.StatusInternalServerError, fallback)
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
		addrs := []string{}
		if rawAddrs, err := item.Addrs(); err == nil {
			for _, addr := range rawAddrs {
				addrs = append(addrs, addr.String())
			}
		}
		interfaces = append(interfaces, networkInterface{
			Name:         item.Name,
			Index:        item.Index,
			MTU:          item.MTU,
			Flags:        item.Flags.String(),
			HardwareAddr: item.HardwareAddr.String(),
			Addrs:        addrs,
		})
	}
	return networkStatus{Hostname: hostname, Interfaces: interfaces}, nil
}

func tokenAuthMiddleware(database *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		enabled, err := db.GetSetting(c.Request.Context(), database, "mcp.enabled")
		if errors.Is(err, db.ErrNotFound) || !strings.EqualFold(enabled, "true") {
			writeFail(c, http.StatusForbidden, "mcp disabled")
			c.Abort()
			return
		}
		if err != nil {
			writeFail(c, http.StatusInternalServerError, "load mcp settings failed")
			c.Abort()
			return
		}
		accessToken, err := db.GetSetting(c.Request.Context(), database, "mcp.access_token")
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeFail(c, http.StatusUnauthorized, "unauthorized")
			} else {
				writeFail(c, http.StatusInternalServerError, "load mcp settings failed")
			}
			c.Abort()
			return
		}
		if strings.TrimSpace(accessToken) == "" || extractToken(c) != accessToken {
			writeFail(c, http.StatusUnauthorized, "unauthorized")
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

func bindPageParams(c *gin.Context, raw json.RawMessage) (pageParams, bool) {
	var params pageParams
	if !bindParams(c, raw, &params) {
		return pageParams{}, false
	}
	params.normalize()
	return params, true
}

func bindParams(c *gin.Context, raw json.RawMessage, target any) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	if err := json.Unmarshal(raw, target); err != nil {
		writeFail(c, http.StatusBadRequest, "invalid MCP tool parameters")
		return false
	}
	return true
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

func writeOK(c *gin.Context, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		writeFail(c, http.StatusInternalServerError, "encode MCP response failed")
		return
	}
	c.JSON(http.StatusOK, mcpResponse{Success: true, Message: "ok", Data: raw})
}

func writeFail(c *gin.Context, status int, message string) {
	c.JSON(status, mcpResponse{Success: false, Message: message, Data: nil})
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
