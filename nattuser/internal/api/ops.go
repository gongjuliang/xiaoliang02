package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/logger"

	"github.com/gin-gonic/gin"
)

type OpsHandler struct {
	database  *sql.DB
	log       *logger.Logger
	cfg       *config.Config
	startedAt time.Time
}

type localTunnelStatus struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	ServerID     int64  `json:"server_id"`
	LocalHost    string `json:"local_host"`
	LocalPort    int    `json:"local_port"`
	RemoteHost   string `json:"remote_host"`
	RemotePort   int    `json:"remote_port"`
	Status       string `json:"status"`
	LastError    string `json:"last_error"`
	SyncedAt     string `json:"synced_at"`
	ControlPhase string `json:"control_phase"`
}

type localTunnelRequest struct {
	Name               string `json:"name" binding:"required"`
	ServerConnectionID int64  `json:"server_connection_id"`
	ServerTunnelID     int64  `json:"server_tunnel_id"`
	LocalHost          string `json:"local_host" binding:"required"`
	LocalPort          int    `json:"local_port"`
	Enabled            *bool  `json:"enabled"`
	Remark             string `json:"remark"`
}

type updateConfigRequest struct {
	Settings map[string]string `json:"settings" binding:"required"`
}

type configUpdateResult struct {
	Key             string `json:"key"`
	Value           string `json:"value"`
	HotReloaded     bool   `json:"hot_reloaded"`
	RestartRequired bool   `json:"restart_required"`
}

func NewOpsHandler(database *sql.DB, log *logger.Logger, cfg *config.Config) *OpsHandler {
	return &OpsHandler{
		database:  database,
		log:       log,
		cfg:       cfg,
		startedAt: time.Now(),
	}
}

func (h *OpsHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/status", h.status)
	group.GET("/tunnels", h.localTunnels)
	group.POST("/tunnels", h.createLocalTunnel)
	group.PUT("/tunnels/:id", h.updateLocalTunnel)
	group.DELETE("/tunnels/:id", h.deleteLocalTunnel)
	group.GET("/audit-logs", h.auditLogs)
	group.GET("/config", h.getConfig)
	group.PUT("/config", h.updateConfig)
	group.GET("/mcp-config", h.getMCPConfig)
	group.PUT("/mcp-config", h.updateMCPConfig)
	group.GET("/mcp-config/reveal-token", h.revealMCPToken)
	group.POST("/mcp-config/rotate-token", h.rotateMCPToken)
}

func (h *OpsHandler) localTunnels(c *gin.Context) {
	var page PageRequest
	if err := c.ShouldBindQuery(&page); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "分页参数不正确")
		return
	}
	page.Normalize()
	tunnels, total, err := db.ListLocalTunnels(c.Request.Context(), h.database, page.Limit(), page.Offset())
	if err != nil {
		h.writeError(c, err, "list local tunnels failed")
		return
	}
	OK(c, NewPageResponse(tunnels, total, page))
}

func (h *OpsHandler) createLocalTunnel(c *gin.Context) {
	var req localTunnelRequest
	if !h.bindAndValidateLocalTunnel(c, &req) {
		return
	}
	if _, err := db.GetServerConnectionByID(c.Request.Context(), h.database, req.ServerConnectionID); err != nil {
		h.writeLocalTunnelDBError(c, err, "load server connection failed")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tunnel, err := db.CreateLocalTunnel(c.Request.Context(), h.database, db.CreateLocalTunnelParams{
		Name:               req.Name,
		ServerConnectionID: req.ServerConnectionID,
		ServerTunnelID:     req.ServerTunnelID,
		LocalHost:          req.LocalHost,
		LocalPort:          req.LocalPort,
		Enabled:            enabled,
		Remark:             req.Remark,
	})
	if err != nil {
		h.writeLocalTunnelDBError(c, err, "create local tunnel failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "local_tunnel_create", "local_tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("created local tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, tunnel)
}

func (h *OpsHandler) updateLocalTunnel(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req localTunnelRequest
	if !h.bindAndValidateLocalTunnel(c, &req) {
		return
	}
	if _, err := db.GetServerConnectionByID(c.Request.Context(), h.database, req.ServerConnectionID); err != nil {
		h.writeLocalTunnelDBError(c, err, "load server connection failed")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tunnel, err := db.UpdateLocalTunnel(c.Request.Context(), h.database, id, db.UpdateLocalTunnelParams{
		Name:               req.Name,
		ServerConnectionID: req.ServerConnectionID,
		ServerTunnelID:     req.ServerTunnelID,
		LocalHost:          req.LocalHost,
		LocalPort:          req.LocalPort,
		Enabled:            enabled,
		Remark:             req.Remark,
	})
	if err != nil {
		h.writeLocalTunnelDBError(c, err, "update local tunnel failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "local_tunnel_update", "local_tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("updated local tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, tunnel)
}

func (h *OpsHandler) deleteLocalTunnel(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	tunnel, err := db.DeleteLocalTunnel(c.Request.Context(), h.database, id)
	if err != nil {
		h.writeLocalTunnelDBError(c, err, "delete local tunnel failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "local_tunnel_delete", "local_tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("deleted local tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, tunnel)
}

func (h *OpsHandler) status(c *gin.Context) {
	summary, err := db.GetClientStatusSummary(c.Request.Context(), h.database)
	if err != nil {
		h.writeError(c, err, "load client status failed")
		return
	}
	OK(c, gin.H{
		"status":         "ok",
		"app":            h.cfg.App.Name,
		"version":        h.cfg.App.Version,
		"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
		"runtime": gin.H{
			"goos":         runtime.GOOS,
			"goarch":       runtime.GOARCH,
			"goroutines":   runtime.NumGoroutine(),
			"go_version":   runtime.Version(),
			"current_time": time.Now().Format(time.RFC3339),
		},
		"server_connections": summary,
		"local_tunnels": gin.H{
			"total": 0,
			"note":  "local tunnel runtime state will be populated by the control connection phase",
		},
	})
}

func (h *OpsHandler) auditLogs(c *gin.Context) {
	var page PageRequest
	if err := c.ShouldBindQuery(&page); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "分页参数不正确")
		return
	}
	page.Normalize()
	logs, total, err := db.ListAuditLogs(c.Request.Context(), h.database, page.Limit(), page.Offset())
	if err != nil {
		h.writeError(c, err, "list audit logs failed")
		return
	}
	OK(c, NewPageResponse(logs, total, page))
}

func (h *OpsHandler) getConfig(c *gin.Context) {
	settings, err := db.ListSettings(c.Request.Context(), h.database)
	if err != nil {
		h.writeError(c, err, "list settings failed")
		return
	}
	OK(c, gin.H{
		"current": gin.H{
			"app": gin.H{
				"name":        h.cfg.App.Name,
				"version":     h.cfg.App.Version,
				"environment": h.cfg.App.Environment,
			},
			"http": gin.H{
				"host": h.cfg.HTTP.Host,
				"port": h.cfg.HTTP.Port,
			},
			"log": gin.H{
				"dir":   h.cfg.Log.Dir,
				"level": h.cfg.Log.Level,
			},
			"auth": gin.H{
				"access_token_ttl_minutes":    h.cfg.Auth.AccessTokenTTLMinutes,
				"refresh_token_ttl_minutes":   h.cfg.Auth.RefreshTokenTTLMinutes,
				"login_rate_limit_per_minute": h.cfg.Auth.LoginRateLimitPerMinute,
				"sm2_public_key_file":         h.cfg.Auth.SM2PublicKeyFile,
			},
			"server_defaults": gin.H{
				"server_host":  h.cfg.ServerDefaults.ServerHost,
				"control_port": h.cfg.ServerDefaults.ControlPort,
				"data_port":    h.cfg.ServerDefaults.DataPort,
			},
		},
		"persisted_settings": settings,
		"editable_keys":      editableConfigKeys(),
	})
}

func (h *OpsHandler) updateConfig(c *gin.Context) {
	var req updateConfigRequest
	if !bindJSONOrFail(c, &req, "settings 为必填项") {
		return
	}
	if len(req.Settings) == 0 {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "settings 不能为空")
		return
	}

	results := make([]configUpdateResult, 0, len(req.Settings))
	for key, value := range req.Settings {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		result, err := h.applyConfigSetting(key, value)
		if err != nil {
			Fail(c, http.StatusBadRequest, CodeBadRequest, err.Error())
			return
		}
		if err := db.UpsertSetting(c.Request.Context(), h.database, key, value); err != nil {
			h.writeError(c, err, "save config setting failed")
			return
		}
		results = append(results, result)
	}

	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "config_update", "config", "client", fmt.Sprintf("updated %d config setting(s)", len(results)), c.ClientIP())
	OK(c, gin.H{"updated": results})
}

func (h *OpsHandler) applyConfigSetting(key string, value string) (configUpdateResult, error) {
	switch key {
	case "log.level":
		level := strings.ToLower(value)
		if level != "debug" && level != "info" && level != "error" {
			return configUpdateResult{}, fmt.Errorf("log.level 必须是 debug、info 或 error")
		}
		h.cfg.Log.Level = level
		if h.log != nil {
			h.log.SetLevel(level)
		}
		return configUpdateResult{Key: key, Value: level, HotReloaded: true, RestartRequired: false}, nil
	case "server_defaults.server_host":
		if value == "" {
			return configUpdateResult{}, fmt.Errorf("server_defaults.server_host 为必填项")
		}
		h.cfg.ServerDefaults.ServerHost = value
		return configUpdateResult{Key: key, Value: value, HotReloaded: true, RestartRequired: false}, nil
	case "server_defaults.control_port":
		port, err := parsePortValue(key, value)
		if err != nil {
			return configUpdateResult{}, err
		}
		h.cfg.ServerDefaults.ControlPort = port
		return configUpdateResult{Key: key, Value: strconv.Itoa(port), HotReloaded: true, RestartRequired: false}, nil
	case "server_defaults.data_port":
		port, err := parsePortValue(key, value)
		if err != nil {
			return configUpdateResult{}, err
		}
		h.cfg.ServerDefaults.DataPort = port
		return configUpdateResult{Key: key, Value: strconv.Itoa(port), HotReloaded: true, RestartRequired: false}, nil
	case "http.host", "http.port",
		"auth.access_token_ttl_minutes", "auth.refresh_token_ttl_minutes", "auth.login_rate_limit_per_minute":
		return configUpdateResult{}, fmt.Errorf("该配置不支持热更新，请修改配置文件后重启服务")
	default:
		return configUpdateResult{}, fmt.Errorf("不支持的配置项：%s", key)
	}
}

func (h *OpsHandler) writeError(c *gin.Context, err error, fallback string) {
	if h.log != nil {
		h.log.Errorf("%s: %v", fallback, err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, fallback)
}

func (h *OpsHandler) bindAndValidateLocalTunnel(c *gin.Context, req *localTunnelRequest) bool {
	if !bindJSONOrFail(c, req, "本地隧道参数不正确") {
		return false
	}
	req.Name = strings.TrimSpace(req.Name)
	req.LocalHost = strings.TrimSpace(req.LocalHost)
	req.Remark = strings.TrimSpace(req.Remark)
	switch {
	case req.Name == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "name 为必填项")
	case req.ServerConnectionID <= 0:
		Fail(c, http.StatusBadRequest, CodeBadRequest, "server_connection_id 为必填项")
	case req.ServerTunnelID <= 0:
		Fail(c, http.StatusBadRequest, CodeBadRequest, "server_tunnel_id 为必填项")
	case req.LocalHost == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_host 为必填项")
	case !validPort(req.LocalPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_port 必须在 1 到 65535 之间")
	default:
		return true
	}
	return false
}

func (h *OpsHandler) writeLocalTunnelDBError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "资源不存在")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		Fail(c, http.StatusConflict, CodeConflict, "本地隧道配置冲突")
		return
	}
	h.writeError(c, err, fallback)
}

func editableConfigKeys() []gin.H {
	return []gin.H{
		{"key": "log.level", "hot_reload": true},
		{"key": "server_defaults.server_host", "hot_reload": true},
		{"key": "server_defaults.control_port", "hot_reload": true},
		{"key": "server_defaults.data_port", "hot_reload": true},
	}
}

func parsePortValue(key string, value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s 必须在 1 到 65535 之间", key)
	}
	return port, nil
}

func validateRestartSetting(key string, value string) error {
	switch key {
	case "http.port":
		_, err := parsePortValue(key, value)
		return err
	case "auth.access_token_ttl_minutes", "auth.refresh_token_ttl_minutes", "auth.login_rate_limit_per_minute":
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("%s 必须大于 0", key)
		}
	}
	return nil
}

type mcpConfigRequest struct {
	Enabled     bool   `json:"enabled"`
	AccessToken string `json:"access_token"`
}

func (h *OpsHandler) getMCPConfig(c *gin.Context) {
	enabledValue, err := db.GetSetting(c.Request.Context(), h.database, "mcp.enabled")
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		h.writeError(c, err, "load mcp config failed")
		return
	}
	token, err := db.GetSetting(c.Request.Context(), h.database, "mcp.access_token")
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		h.writeError(c, err, "load mcp token failed")
		return
	}
	enabled, _ := strconv.ParseBool(enabledValue)
	OK(c, gin.H{"enabled": enabled, "access_token_hint": auth.SecretHint(token), "has_access_token": strings.TrimSpace(token) != ""})
}

func (h *OpsHandler) updateMCPConfig(c *gin.Context) {
	var req mcpConfigRequest
	if !bindJSONOrFail(c, &req, "MCP 配置参数不正确") {
		return
	}
	if strings.TrimSpace(req.AccessToken) != "" {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "MCP Key 由系统生成，不允许自定义")
		return
	}
	token, ok := h.ensureMCPToken(c, req.Enabled)
	if !ok {
		return
	}
	if err := db.UpsertSetting(c.Request.Context(), h.database, "mcp.enabled", strconv.FormatBool(req.Enabled)); err != nil {
		h.writeError(c, err, "save mcp enabled failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "mcp_config_update", "mcp", "client", "updated mcp config", c.ClientIP())
	OK(c, gin.H{"enabled": req.Enabled, "access_token_hint": auth.SecretHint(token), "has_access_token": token != ""})
}

func (h *OpsHandler) revealMCPToken(c *gin.Context) {
	token, err := db.GetSetting(c.Request.Context(), h.database, "mcp.access_token")
	if errors.Is(err, db.ErrNotFound) || strings.TrimSpace(token) == "" {
		Fail(c, http.StatusNotFound, CodeNotFound, "MCP Key 尚未生成")
		return
	}
	if err != nil {
		h.writeError(c, err, "load mcp token failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "mcp_token_reveal", "mcp", "client", "revealed mcp token", c.ClientIP())
	OK(c, gin.H{"access_token": token, "access_token_hint": auth.SecretHint(token)})
}

func (h *OpsHandler) rotateMCPToken(c *gin.Context) {
	token, err := auth.GenerateClientSecret()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成 MCP 令牌失败")
		return
	}
	if err := db.UpsertSetting(c.Request.Context(), h.database, "mcp.access_token", token); err != nil {
		h.writeError(c, err, "save mcp token failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "mcp_token_rotate", "mcp", "client", "rotated mcp token", c.ClientIP())
	OK(c, gin.H{"access_token": token, "access_token_hint": auth.SecretHint(token)})
}

func (h *OpsHandler) ensureMCPToken(c *gin.Context, enabled bool) (string, bool) {
	existing, err := db.GetSetting(c.Request.Context(), h.database, "mcp.access_token")
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		h.writeError(c, err, "load mcp token failed")
		return "", false
	}
	token := strings.TrimSpace(existing)
	if !enabled || token != "" {
		return token, true
	}
	token, err = auth.GenerateClientSecret()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成 MCP Key 失败")
		return "", false
	}
	if err := db.UpsertSetting(c.Request.Context(), h.database, "mcp.access_token", token); err != nil {
		h.writeError(c, err, "save mcp token failed")
		return "", false
	}
	return token, true
}
