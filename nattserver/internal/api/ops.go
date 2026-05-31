package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/logger"

	"github.com/gin-gonic/gin"
)

type OpsHandler struct {
	database *sql.DB
	log      *logger.Logger
	cfg      *config.Config
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
		database: database,
		log:      log,
		cfg:      cfg,
	}
}

func (h *OpsHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/dashboard", h.dashboard)
	group.GET("/audit-logs", h.auditLogs)
	group.GET("/config", h.getConfig)
	group.PUT("/config", h.updateConfig)
}

func (h *OpsHandler) dashboard(c *gin.Context) {
	summary, err := db.GetDashboardSummary(c.Request.Context(), h.database)
	if err != nil {
		h.writeError(c, err, "load dashboard failed")
		return
	}
	OK(c, summary)
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
			"protocol": gin.H{
				"control_host": h.cfg.Protocol.ControlHost,
				"control_port": h.cfg.Protocol.ControlPort,
				"data_host":    h.cfg.Protocol.DataHost,
				"data_port":    h.cfg.Protocol.DataPort,
				"tls": gin.H{
					"enabled":   h.cfg.Protocol.TLS.Enabled,
					"cert_file": h.cfg.Protocol.TLS.CertFile,
					"key_file":  h.cfg.Protocol.TLS.KeyFile,
				},
			},
			"tunnel": gin.H{
				"remote_port_min": h.cfg.Tunnel.RemotePortMin,
				"remote_port_max": h.cfg.Tunnel.RemotePortMax,
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

	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "config_update", "config", "server", fmt.Sprintf("updated %d config setting(s)", len(results)), c.ClientIP())
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
	case "tunnel.remote_port_min":
		port, err := parsePortValue(key, value)
		if err != nil {
			return configUpdateResult{}, err
		}
		if port > h.cfg.Tunnel.RemotePortMax {
			return configUpdateResult{}, fmt.Errorf("tunnel.remote_port_min cannot be greater than tunnel.remote_port_max")
		}
		h.cfg.Tunnel.RemotePortMin = port
		return configUpdateResult{Key: key, Value: strconv.Itoa(port), HotReloaded: true, RestartRequired: false}, nil
	case "tunnel.remote_port_max":
		port, err := parsePortValue(key, value)
		if err != nil {
			return configUpdateResult{}, err
		}
		if port < h.cfg.Tunnel.RemotePortMin {
			return configUpdateResult{}, fmt.Errorf("tunnel.remote_port_max cannot be less than tunnel.remote_port_min")
		}
		h.cfg.Tunnel.RemotePortMax = port
		return configUpdateResult{Key: key, Value: strconv.Itoa(port), HotReloaded: true, RestartRequired: false}, nil
	case "http.host", "http.port",
		"protocol.control_host", "protocol.control_port", "protocol.data_host", "protocol.data_port",
		"protocol.tls.enabled", "protocol.tls.cert_file", "protocol.tls.key_file",
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

func editableConfigKeys() []gin.H {
	return []gin.H{
		{"key": "log.level", "hot_reload": true},
		{"key": "tunnel.remote_port_min", "hot_reload": true},
		{"key": "tunnel.remote_port_max", "hot_reload": true},
		{"key": "http.host", "hot_reload": false},
		{"key": "http.port", "hot_reload": false},
		{"key": "protocol.control_host", "hot_reload": false},
		{"key": "protocol.control_port", "hot_reload": false},
		{"key": "protocol.data_host", "hot_reload": false},
		{"key": "protocol.data_port", "hot_reload": false},
		{"key": "protocol.tls.enabled", "hot_reload": false},
		{"key": "protocol.tls.cert_file", "hot_reload": false},
		{"key": "protocol.tls.key_file", "hot_reload": false},
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
	case "http.port", "protocol.control_port", "protocol.data_port":
		_, err := parsePortValue(key, value)
		return err
	case "auth.access_token_ttl_minutes", "auth.refresh_token_ttl_minutes", "auth.login_rate_limit_per_minute":
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("%s must be greater than 0", key)
		}
	case "protocol.tls.enabled":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("protocol.tls.enabled must be true or false")
		}
	}
	return nil
}
