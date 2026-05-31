package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

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
	group.GET("/tunnels", h.localTunnels)
	group.GET("/status", h.status)
	group.GET("/audit-logs", h.auditLogs)
	group.GET("/config", h.getConfig)
	group.PUT("/config", h.updateConfig)
}

func (h *OpsHandler) localTunnels(c *gin.Context) {
	var page PageRequest
	if err := c.ShouldBindQuery(&page); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid pagination parameters")
		return
	}
	page.Normalize()
	OK(c, NewPageResponse([]localTunnelStatus{}, 0, page))
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
			"mcp": gin.H{
				"enabled": h.cfg.MCP.Enabled,
				"host":    h.cfg.MCP.Host,
				"port":    h.cfg.MCP.Port,
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
		"auth.access_token_ttl_minutes", "auth.refresh_token_ttl_minutes", "auth.login_rate_limit_per_minute",
		"mcp.enabled", "mcp.host", "mcp.port":
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
		{"key": "mcp.enabled", "hot_reload": false},
		{"key": "mcp.host", "hot_reload": false},
		{"key": "mcp.port", "hot_reload": false},
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
	case "http.port", "mcp.port":
		_, err := parsePortValue(key, value)
		return err
	case "auth.access_token_ttl_minutes", "auth.refresh_token_ttl_minutes", "auth.login_rate_limit_per_minute":
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("%s must be greater than 0", key)
		}
	case "mcp.enabled":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("mcp.enabled must be true or false")
		}
	}
	return nil
}
