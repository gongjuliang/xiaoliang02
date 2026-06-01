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

type TunnelRuntime interface {
	StartTunnel(ctx context.Context, id int64) (model.Tunnel, error)
	StopTunnel(ctx context.Context, id int64) (model.Tunnel, error)
}

type serverHandler struct {
	tunnelCfg config.TunnelConfig
	database  *sql.DB
	log       *logger.Logger
	runtime   TunnelRuntime
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
	RegisterServerRoutes(router, database, log, runtime, tunnelCfg...)

	router.NoRoute(func(c *gin.Context) {
		writeFail(c, http.StatusNotFound, "resource not found")
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

	group := router.Group("/mcp")
	group.GET("/health", func(c *gin.Context) {
		writeOK(c, gin.H{"status": "ok"})
	})

	protected := group.Group("")
	protected.Use(tokenAuthMiddleware(database))
	protected.POST("/tools/call", handler.callTool)
}

func (h *serverHandler) callTool(c *gin.Context) {
	var req mcpRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Tool) == "" {
		writeFail(c, http.StatusBadRequest, "invalid MCP tool request")
		return
	}

	// Dispatch is explicit rather than reflective, which keeps the MCP surface
	// limited to the documented server.* tools.
	switch strings.TrimSpace(req.Tool) {
	case "server.list_tunnels":
		h.listTunnels(c, req.Params)
	case "server.create_tunnel":
		h.createTunnel(c, req.Params)
	case "server.start_tunnel":
		h.startTunnel(c, req.Params)
	case "server.stop_tunnel":
		h.stopTunnel(c, req.Params)
	case "server.delete_tunnel":
		h.deleteTunnel(c, req.Params)
	case "server.get_dashboard":
		h.getDashboard(c)
	default:
		writeFail(c, http.StatusBadRequest, "unknown MCP tool")
	}
}

func (h *serverHandler) listTunnels(c *gin.Context, raw json.RawMessage) {
	params, ok := bindPageParams(c, raw)
	if !ok {
		return
	}
	tunnels, total, err := db.ListTunnels(c.Request.Context(), h.database, 0, params.limit(), params.offset())
	if err != nil {
		h.writeError(c, err, "list tunnels failed")
		return
	}
	writeOK(c, pageResult{Items: tunnels, Total: total, Page: params.Page, PageSize: params.PageSize})
}

func (h *serverHandler) getDashboard(c *gin.Context) {
	summary, err := db.GetDashboardSummary(c.Request.Context(), h.database)
	if err != nil {
		h.writeError(c, err, "get dashboard failed")
		return
	}
	writeOK(c, summary)
}

func (h *serverHandler) createTunnel(c *gin.Context, raw json.RawMessage) {
	var params tunnelParams
	if !bindParams(c, raw, &params) {
		return
	}
	if !h.validateTunnelParams(c, &params) {
		return
	}
	tunnel, err := db.CreateTunnel(c.Request.Context(), h.database, db.CreateTunnelParams{
		Name:       params.Name,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: params.RemoteHost,
		RemotePort: params.RemotePort,
		AutoStart:  params.AutoStart,
		Remark:     params.Remark,
	})
	if err != nil {
		h.writeError(c, err, "create tunnel failed")
		return
	}
	secret, secretHash, secretHint, err := buildMCPSecret()
	if err != nil {
		h.writeError(c, err, "generate tunnel secret failed")
		return
	}
	key, err := db.CreateTunnelKey(c.Request.Context(), h.database, db.CreateTunnelKeyParams{TunnelID: tunnel.ID, SecretHash: secretHash, SecretHint: secretHint})
	if err != nil {
		h.writeError(c, err, "create tunnel key failed")
		return
	}
	h.audit(c, "mcp_tunnel_create", tunnel.ID, fmt.Sprintf("mcp created tunnel %s", tunnel.Name))
	writeOK(c, gin.H{"tunnel": tunnel, "key": key, "secret": secret})
}

func (h *serverHandler) startTunnel(c *gin.Context, raw json.RawMessage) {
	h.runTunnelAction(c, raw, h.startTunnelByID, "mcp_tunnel_start", "mcp started tunnel")
}

func (h *serverHandler) stopTunnel(c *gin.Context, raw json.RawMessage) {
	h.runTunnelAction(c, raw, h.stopTunnelByID, "mcp_tunnel_stop", "mcp stopped tunnel")
}

func (h *serverHandler) deleteTunnel(c *gin.Context, raw json.RawMessage) {
	var params idParams
	if !bindParams(c, raw, &params) {
		return
	}
	if params.ID <= 0 {
		writeFail(c, http.StatusBadRequest, "id is required")
		return
	}
	if h.runtime != nil {
		if tunnel, err := db.GetTunnelByID(c.Request.Context(), h.database, params.ID); err == nil && tunnel.Status == model.TunnelStatusRunning {
			if _, err := h.runtime.StopTunnel(c.Request.Context(), params.ID); err != nil {
				h.writeError(c, err, "stop tunnel before delete failed")
				return
			}
		}
	}
	tunnel, err := db.DeleteTunnel(c.Request.Context(), h.database, params.ID)
	if err != nil {
		h.writeError(c, err, "delete tunnel failed")
		return
	}
	h.audit(c, "mcp_tunnel_delete", tunnel.ID, fmt.Sprintf("mcp deleted tunnel %s", tunnel.Name))
	writeOK(c, tunnel)
}

func (h *serverHandler) runTunnelAction(c *gin.Context, raw json.RawMessage, actionFn func(context.Context, int64) (model.Tunnel, error), action string, contentPrefix string) {
	var params idParams
	if !bindParams(c, raw, &params) {
		return
	}
	if params.ID <= 0 {
		writeFail(c, http.StatusBadRequest, "id is required")
		return
	}
	tunnel, err := actionFn(c.Request.Context(), params.ID)
	if err != nil {
		h.writeError(c, err, "tunnel runtime action failed")
		return
	}
	h.audit(c, action, tunnel.ID, fmt.Sprintf("%s %s", contentPrefix, tunnel.Name))
	writeOK(c, tunnel)
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
	return db.SetTunnelStatus(ctx, h.database, id, model.TunnelStatusStopped, "")
}

func (h *serverHandler) validateTunnelParams(c *gin.Context, params *tunnelParams) bool {
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
		writeFail(c, http.StatusBadRequest, "name is required")
	case params.Protocol != string(model.TunnelProtocolTCP):
		writeFail(c, http.StatusBadRequest, "only tcp protocol is supported")
	case !validPort(params.RemotePort):
		writeFail(c, http.StatusBadRequest, "remote_port must be between 1 and 65535")
	case params.RemotePort < h.tunnelCfg.RemotePortMin || params.RemotePort > h.tunnelCfg.RemotePortMax:
		writeFail(c, http.StatusBadRequest, fmt.Sprintf("remote_port must be between %d and %d", h.tunnelCfg.RemotePortMin, h.tunnelCfg.RemotePortMax))
	default:
		return true
	}
	return false
}

func (h *serverHandler) audit(c *gin.Context, action string, tunnelID int64, content string) {
	_ = db.InsertAuditLog(c.Request.Context(), h.database, "mcp", action, "tunnel", strconv.FormatInt(tunnelID, 10), content, c.ClientIP())
}

func (h *serverHandler) writeError(c *gin.Context, err error, fallback string) {
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
