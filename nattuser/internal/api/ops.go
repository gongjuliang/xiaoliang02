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
	ServerConnectionID int64  `json:"server_connection_id" binding:"required"`
	ServerTunnelID     int64  `json:"server_tunnel_id" binding:"required"`
	LocalHost          string `json:"local_host" binding:"required"`
	LocalPort          int    `json:"local_port" binding:"required"`
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
	group.GET("/audit-logs", h.auditLogs)
	group.GET("/config", h.getConfig)
	group.PUT("/config", h.updateConfig)
	group.GET("/mcp-config", h.getMCPConfig)
	group.PUT("/mcp-config", h.updateMCPConfig)
	group.POST("/mcp-config/rotate-token", h.rotateMCPToken)
}

func (h *OpsHandler) localTunnels(c *gin.Context) {
	var page PageRequest
	if err := c.ShouldBindQuery(&page); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid pagination parameters")
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
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid pagination parameters")
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
				"use_tls":      h.cfg.ServerDefaults.UseTLS,
			},
		},
		"persisted_settings": settings,
		"editable_keys":      editableConfigKeys(),
	})
}

func (h *OpsHandler) updateConfig(c *gin.Context) {
	var req updateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "settings is required")
		return
	}
	if len(req.Settings) == 0 {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "settings cannot be empty")
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
			return configUpdateResult{}, fmt.Errorf("log.level must be debug, info, or error")
		}
		h.cfg.Log.Level = level
		if h.log != nil {
			h.log.SetLevel(level)
		}
		return configUpdateResult{Key: key, Value: level, HotReloaded: true, RestartRequired: false}, nil
	case "server_defaults.server_host":
		if value == "" {
			return configUpdateResult{}, fmt.Errorf("server_defaults.server_host is required")
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
	case "server_defaults.use_tls":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return configUpdateResult{}, fmt.Errorf("server_defaults.use_tls must be true or false")
		}
		h.cfg.ServerDefaults.UseTLS = parsed
		return configUpdateResult{Key: key, Value: strconv.FormatBool(parsed), HotReloaded: true, RestartRequired: false}, nil
	case "http.host", "http.port",
		"auth.access_token_ttl_minutes", "auth.refresh_token_ttl_minutes", "auth.login_rate_limit_per_minute":
		if err := validateRestartSetting(key, value); err != nil {
			return configUpdateResult{}, err
		}
		return configUpdateResult{Key: key, Value: value, HotReloaded: false, RestartRequired: true}, nil
	default:
		return configUpdateResult{}, fmt.Errorf("unsupported config key: %s", key)
	}
}

func (h *OpsHandler) writeError(c *gin.Context, err error, fallback string) {
	if h.log != nil {
		h.log.Errorf("%s: %v", fallback, err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, fallback)
}

func (h *OpsHandler) bindAndValidateLocalTunnel(c *gin.Context, req *localTunnelRequest) bool {
	if err := c.ShouldBindJSON(req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid local tunnel parameters")
		return false
	}
	req.Name = strings.TrimSpace(req.Name)
	req.LocalHost = strings.TrimSpace(req.LocalHost)
	req.Remark = strings.TrimSpace(req.Remark)
	switch {
	case req.Name == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "name is required")
	case req.ServerConnectionID <= 0:
		Fail(c, http.StatusBadRequest, CodeBadRequest, "server_connection_id is required")
	case req.ServerTunnelID <= 0:
		Fail(c, http.StatusBadRequest, CodeBadRequest, "server_tunnel_id is required")
	case req.LocalHost == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_host is required")
	case !validPort(req.LocalPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_port must be between 1 and 65535")
	default:
		return true
	}
	return false
}

func (h *OpsHandler) writeLocalTunnelDBError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "resource not found")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		Fail(c, http.StatusConflict, CodeConflict, "local tunnel conflict")
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
		{"key": "server_defaults.use_tls", "hot_reload": true},
		{"key": "http.host", "hot_reload": false},
		{"key": "http.port", "hot_reload": false},
		{"key": "auth.access_token_ttl_minutes", "hot_reload": false},
		{"key": "auth.refresh_token_ttl_minutes", "hot_reload": false},
		{"key": "auth.login_rate_limit_per_minute", "hot_reload": false},
	}
}

func parsePortValue(key string, value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be between 1 and 65535", key)
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
			return fmt.Errorf("%s must be greater than 0", key)
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
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid mcp config")
		return
	}
	token := strings.TrimSpace(req.AccessToken)
	if req.Enabled && token == "" {
		existing, err := db.GetSetting(c.Request.Context(), h.database, "mcp.access_token")
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			h.writeError(c, err, "load mcp token failed")
			return
		}
		token = strings.TrimSpace(existing)
	}
	if req.Enabled && token == "" {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "access_token is required when mcp is enabled")
		return
	}
	if err := db.UpsertSetting(c.Request.Context(), h.database, "mcp.enabled", strconv.FormatBool(req.Enabled)); err != nil {
		h.writeError(c, err, "save mcp enabled failed")
		return
	}
	if strings.TrimSpace(req.AccessToken) != "" {
		if err := db.UpsertSetting(c.Request.Context(), h.database, "mcp.access_token", token); err != nil {
			h.writeError(c, err, "save mcp token failed")
			return
		}
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "mcp_config_update", "mcp", "client", "updated mcp config", c.ClientIP())
	OK(c, gin.H{"enabled": req.Enabled, "access_token_hint": auth.SecretHint(token), "has_access_token": token != ""})
}

func (h *OpsHandler) rotateMCPToken(c *gin.Context) {
	token, err := auth.GenerateClientSecret()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "generate mcp token failed")
		return
	}
	if err := db.UpsertSetting(c.Request.Context(), h.database, "mcp.access_token", token); err != nil {
		h.writeError(c, err, "save mcp token failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "mcp_token_rotate", "mcp", "client", "rotated mcp token", c.ClientIP())
	OK(c, gin.H{"access_token": token, "access_token_hint": auth.SecretHint(token)})
}
