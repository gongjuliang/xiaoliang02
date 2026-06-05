package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"nattserver/internal/auth"
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
	group.GET("/mcp-config", h.getMCPConfig)
	group.PUT("/mcp-config", h.updateMCPConfig)
	group.GET("/mcp-config/reveal-token", h.revealMCPToken)
	group.POST("/mcp-config/rotate-token", h.rotateMCPToken)
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
			"protocol": gin.H{
				"control_host": h.cfg.Protocol.ControlHost,
				"control_port": h.cfg.Protocol.ControlPort,
				"data_host":    h.cfg.Protocol.DataHost,
				"data_port":    h.cfg.Protocol.DataPort,
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

	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "config_update", "config", "server", fmt.Sprintf("updated %d config setting(s)", len(results)), c.ClientIP())
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
	case "tunnel.remote_port_min":
		port, err := parsePortValue(key, value, true)
		if err != nil {
			return configUpdateResult{}, err
		}
		if port > h.cfg.Tunnel.RemotePortMax {
			return configUpdateResult{}, fmt.Errorf("tunnel.remote_port_min 不能大于 tunnel.remote_port_max")
		}
		h.cfg.Tunnel.RemotePortMin = port
		return configUpdateResult{Key: key, Value: strconv.Itoa(port), HotReloaded: true, RestartRequired: false}, nil
	case "tunnel.remote_port_max":
		port, err := parsePortValue(key, value, false)
		if err != nil {
			return configUpdateResult{}, err
		}
		if port < h.cfg.Tunnel.RemotePortMin {
			return configUpdateResult{}, fmt.Errorf("tunnel.remote_port_max 不能小于 tunnel.remote_port_min")
		}
		h.cfg.Tunnel.RemotePortMax = port
		return configUpdateResult{Key: key, Value: strconv.Itoa(port), HotReloaded: true, RestartRequired: false}, nil
	case "http.host", "http.port",
		"protocol.control_host", "protocol.control_port", "protocol.data_host", "protocol.data_port",
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

func editableConfigKeys() []gin.H {
	return []gin.H{
		{"key": "log.level", "hot_reload": true},
		{"key": "tunnel.remote_port_min", "hot_reload": true},
		{"key": "tunnel.remote_port_max", "hot_reload": true},
	}
}

func parsePortValue(key string, value string, allowZero bool) (int, error) {
	port, err := strconv.Atoi(value)
	min := 1
	if allowZero {
		min = 0
	}
	if err != nil || port < min || port > 65535 {
		if allowZero {
			return 0, fmt.Errorf("%s 必须在 0 到 65535 之间", key)
		}
		return 0, fmt.Errorf("%s 必须在 1 到 65535 之间", key)
	}
	return port, nil
}

func validateRestartSetting(key string, value string) error {
	switch key {
	case "http.port", "protocol.control_port", "protocol.data_port":
		_, err := parsePortValue(key, value, false)
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
	OK(c, gin.H{
		"enabled":           enabled,
		"access_token_hint": auth.SecretHint(token),
		"has_access_token":  strings.TrimSpace(token) != "",
	})
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
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "mcp_config_update", "mcp", "server", "updated mcp config", c.ClientIP())
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
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "mcp_token_reveal", "mcp", "server", "revealed mcp token", c.ClientIP())
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
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "mcp_token_rotate", "mcp", "server", "rotated mcp token", c.ClientIP())
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
