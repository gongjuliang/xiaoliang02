// Package api 提供隧道(Tunnel)的Web API处理器。
// 包含隧道的CRUD操作（创建/查询/更新/删除）、生命周期管理（启动/停止）、
// 密钥管理（轮换/启用/禁用）等完整的REST API端点。
package api

import (
	"context"
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
	"nattserver/internal/model"

	"github.com/gin-gonic/gin"
)

// TunnelHandler 隧道HTTP API处理器，封装数据库操作、日志记录、配置校验和运行时隧道控制。
type TunnelHandler struct {
	database *sql.DB              // 数据库连接
	log      *logger.Logger       // 日志记录器
	cfg      *config.TunnelConfig // 隧道配置（端口范围等）
	runtime  TunnelRuntime        // 隧道运行时管理接口（启动/停止/断开）
}

// TunnelRuntime 隧道运行时管理接口，抽象出启动、停止和断开连接的运行时能力。
// 由control.Server实现，注入到TunnelHandler中以实现API驱动的隧道控制。
type TunnelRuntime interface {
	StartTunnel(ctx context.Context, id int64) (model.Tunnel, error)
	StopTunnel(ctx context.Context, id int64) (model.Tunnel, error)
	DisconnectTunnel(id int64)
}

// tunnelRequest 隧道创建/更新的请求参数结构体。
type tunnelRequest struct {
	Name       string `json:"name" binding:"required"`        // 隧道名称（必填）
	Protocol   string `json:"protocol"`                       // 协议类型（默认tcp）
	RemoteHost string `json:"remote_host"`                    // 公网监听地址
	RemotePort int    `json:"remote_port" binding:"required"` // 公网监听端口（必填）
	AutoStart  bool   `json:"auto_start"`                     // 是否自动启动
	Remark     string `json:"remark"`                         // 备注信息
}

// tunnelSecretResponse 隧道创建及密钥轮换时的响应体，同时返回隧道信息和明文密钥。
type tunnelSecretResponse struct {
	Tunnel model.Tunnel    `json:"tunnel"` // 隧道信息
	Key    model.TunnelKey `json:"key"`    // 密钥信息
	Secret string          `json:"secret"` // 密钥明文（仅此时返回一次）
}

// NewTunnelHandler 创建隧道HTTP API处理器。
// 参数database：数据库连接。
// 参数log：日志记录器。
// 参数cfg：隧道配置。
// 参数runtime：隧道运行时控制器。
// 返回值：初始化好的TunnelHandler。
func NewTunnelHandler(database *sql.DB, log *logger.Logger, cfg *config.TunnelConfig, runtime TunnelRuntime) *TunnelHandler {
	return &TunnelHandler{
		database: database,
		log:      log,
		cfg:      cfg,
		runtime:  runtime,
	}
}

// RegisterRoutes 在Gin路由组上注册隧道管理的所有REST端点。
// POST /tunnels - 创建隧道
// GET /tunnels - 分页查询隧道列表
// PUT /tunnels/:id - 更新隧道
// DELETE /tunnels/:id - 删除隧道
// POST /tunnels/:id/start - 启动隧道
// POST /tunnels/:id/stop - 停止隧道
// POST /tunnels/:id/rotate-secret - 轮换隧道密钥
// POST /tunnels/:id/enable-key - 启用隧道密钥
// POST /tunnels/:id/disable-key - 禁用隧道密钥
func (h *TunnelHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/tunnels", h.list)
	group.POST("/tunnels", h.create)
	group.PUT("/tunnels/:id", h.update)
	group.DELETE("/tunnels/:id", h.delete)
	group.POST("/tunnels/:id/start", h.start)
	group.POST("/tunnels/:id/stop", h.stop)
	group.POST("/tunnels/:id/rotate-secret", h.rotateSecret)
	group.POST("/tunnels/:id/enable-key", h.enableKey)
	group.POST("/tunnels/:id/disable-key", h.disableKey)
}

// list 处理获取隧道列表的请求，支持分页查询。
func (h *TunnelHandler) list(c *gin.Context) {
	var page PageRequest
	if err := c.ShouldBindQuery(&page); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "分页参数不正确")
		return
	}
	page.Normalize()
	tunnels, total, err := db.ListTunnels(c.Request.Context(), h.database, 0, page.Limit(), page.Offset())
	if err != nil {
		h.writeDBError(c, err, "list tunnels failed")
		return
	}
	OK(c, NewPageResponse(tunnels, total, page))
}

// create 处理创建隧道的请求，包括参数校验、数据库创建和密钥生成。
// 支持auto_start字段，为true时隧道创建后进入等待状态。
func (h *TunnelHandler) create(c *gin.Context) {
	var req tunnelRequest
	if !h.bindAndValidateTunnelRequest(c, &req) {
		return
	}
	tunnel, err := db.CreateTunnel(c.Request.Context(), h.database, db.CreateTunnelParams{
		Name:       req.Name,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: req.RemoteHost,
		RemotePort: req.RemotePort,
		AutoStart:  req.AutoStart,
		Remark:     req.Remark,
	})
	if err != nil {
		h.writeDBError(c, err, "create tunnel failed")
		return
	}
	secret, secretHash, secretHint, err := buildTunnelSecret()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成隧道秘钥失败")
		return
	}
	key, err := db.CreateTunnelKey(c.Request.Context(), h.database, db.CreateTunnelKeyParams{
		TunnelID: tunnel.ID, SecretHash: secretHash, SecretHint: secretHint, SecretPlain: secret,
	})
	if err != nil {
		h.writeDBError(c, err, "create tunnel key failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "tunnel_create", "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("created tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, tunnelSecretResponse{Tunnel: tunnel, Key: key, Secret: secret})
}

// update 处理更新隧道配置的请求，支持修改名称、公网地址/端口、auto_start等字段。
func (h *TunnelHandler) update(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req tunnelRequest
	if !h.bindAndValidateTunnelRequest(c, &req) {
		return
	}
	tunnel, err := db.UpdateTunnel(c.Request.Context(), h.database, id, db.UpdateTunnelParams{
		Name:       req.Name,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: req.RemoteHost,
		RemotePort: req.RemotePort,
		AutoStart:  req.AutoStart,
		Remark:     req.Remark,
	})
	if err != nil {
		h.writeDBError(c, err, "update tunnel failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "tunnel_update", "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("updated tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, tunnel)
}

func (h *TunnelHandler) rotateSecret(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	tunnel, err := db.GetTunnelByID(c.Request.Context(), h.database, id)
	if err != nil {
		h.writeDBError(c, err, "load tunnel failed")
		return
	}
	secret, secretHash, secretHint, err := buildTunnelSecret()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成隧道秘钥失败")
		return
	}
	key, err := db.RotateTunnelSecret(c.Request.Context(), h.database, id, secretHash, secretHint, secret)
	if err != nil {
		h.writeDBError(c, err, "rotate tunnel secret failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "tunnel_rotate_secret", "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("rotated secret for tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, tunnelSecretResponse{Tunnel: tunnel, Key: key, Secret: secret})
}

func (h *TunnelHandler) enableKey(c *gin.Context) {
	h.setKeyStatus(c, model.TunnelKeyStatusEnabled, "tunnel_key_enable", "enabled tunnel key")
}

func (h *TunnelHandler) disableKey(c *gin.Context) {
	h.setKeyStatus(c, model.TunnelKeyStatusDisabled, "tunnel_key_disable", "disabled tunnel key")
}

func (h *TunnelHandler) setKeyStatus(c *gin.Context, status model.TunnelKeyStatus, action string, contentPrefix string) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	key, err := db.SetTunnelKeyStatus(c.Request.Context(), h.database, id, status)
	if err != nil {
		h.writeDBError(c, err, "set tunnel key status failed")
		return
	}
	if status == model.TunnelKeyStatusDisabled && h.runtime != nil {
		_, _ = h.runtime.StopTunnel(c.Request.Context(), id)
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), action, "tunnel", strconv.FormatInt(id, 10), fmt.Sprintf("%s %d", contentPrefix, id), c.ClientIP())
	OK(c, key)
}

func (h *TunnelHandler) delete(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	tunnel, err := db.GetTunnelByID(c.Request.Context(), h.database, id)
	if err != nil {
		h.writeDBError(c, err, "load tunnel before delete failed")
		return
	}
	if _, err := db.SetTunnelKeyStatus(c.Request.Context(), h.database, id, model.TunnelKeyStatusDisabled); err != nil && !errors.Is(err, db.ErrNotFound) {
		h.writeDBError(c, err, "disable tunnel key before delete failed")
		return
	}
	if h.runtime != nil {
		if _, err := h.runtime.StopTunnel(c.Request.Context(), id); err != nil {
			h.writeRuntimeError(c, err, "stop tunnel before delete failed")
			return
		}
		h.runtime.DisconnectTunnel(id)
	}
	deleted, err := db.DeleteTunnel(c.Request.Context(), h.database, id)
	if err != nil {
		h.writeDBError(c, err, "delete tunnel failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "tunnel_delete", "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("deleted tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, deleted)
}

func (h *TunnelHandler) start(c *gin.Context) {
	if h.runtime != nil {
		h.runTunnelAction(c, h.runtime.StartTunnel, "tunnel_start", "started tunnel")
		return
	}
	h.setStatus(c, model.TunnelStatusRunning, "tunnel_start", "started tunnel")
}

func (h *TunnelHandler) stop(c *gin.Context) {
	if h.runtime != nil {
		h.runTunnelAction(c, h.runtime.StopTunnel, "tunnel_stop", "stopped tunnel")
		return
	}
	h.setStatus(c, model.TunnelStatusStopped, "tunnel_stop", "stopped tunnel")
}

func (h *TunnelHandler) runTunnelAction(c *gin.Context, actionFn func(context.Context, int64) (model.Tunnel, error), action string, contentPrefix string) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	tunnel, err := actionFn(c.Request.Context(), id)
	if err != nil {
		h.writeRuntimeError(c, err, "tunnel runtime action failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), action, "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("%s %s", contentPrefix, tunnel.Name), c.ClientIP())
	OK(c, tunnel)
}

func (h *TunnelHandler) setStatus(c *gin.Context, status model.TunnelStatus, action string, contentPrefix string) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var tunnel model.Tunnel
	var err error
	if status == model.TunnelStatusStopped {
		tunnel, err = db.SetTunnelStopped(c.Request.Context(), h.database, id, "")
	} else {
		tunnel, err = db.SetTunnelStatus(c.Request.Context(), h.database, id, status, "")
	}
	if err != nil {
		h.writeDBError(c, err, "set tunnel status failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), action, "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("%s %s", contentPrefix, tunnel.Name), c.ClientIP())
	OK(c, tunnel)
}

func (h *TunnelHandler) bindAndValidateTunnelRequest(c *gin.Context, req *tunnelRequest) bool {
	if !bindJSONOrFail(c, req, "隧道参数不正确") {
		return false
	}
	req.Name = strings.TrimSpace(req.Name)
	req.RemoteHost = strings.TrimSpace(req.RemoteHost)
	req.Protocol = strings.TrimSpace(strings.ToLower(req.Protocol))
	if req.Protocol == "" {
		req.Protocol = string(model.TunnelProtocolTCP)
	}
	if req.RemoteHost == "" {
		req.RemoteHost = "0.0.0.0"
	}

	switch {
	case req.Name == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "name 为必填项")
	case req.Protocol != string(model.TunnelProtocolTCP):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "仅支持 tcp 协议")
	case !validPort(req.RemotePort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "remote_port 必须在 1 到 65535 之间")
	case req.RemotePort < h.cfg.RemotePortMin || req.RemotePort > h.cfg.RemotePortMax:
		Fail(c, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("remote_port 必须在 %d 到 %d 之间", h.cfg.RemotePortMin, h.cfg.RemotePortMax))
	default:
		return true
	}
	return false
}

func (h *TunnelHandler) writeDBError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "隧道不存在")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		Fail(c, http.StatusConflict, CodeConflict, "公网端口已存在")
		return
	}
	if h.log != nil {
		h.log.Errorf("%s: %v", fallback, err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, fallback)
}

func (h *TunnelHandler) writeRuntimeError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "隧道不存在")
		return
	}
	message := err.Error()
	if strings.Contains(message, "not online") ||
		strings.Contains(message, "not expected") ||
		strings.Contains(message, "listen remote port") {
		Fail(c, http.StatusConflict, CodeConflict, message)
		return
	}
	if h.log != nil {
		h.log.Errorf("%s: %v", fallback, err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, fallback)
}

func parseOptionalInt64Query(c *gin.Context, key string) (int64, bool) {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		Fail(c, http.StatusBadRequest, CodeBadRequest, key+" 不正确")
		return 0, false
	}
	return parsed, true
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}

func buildTunnelSecret() (plain string, hash string, hint string, err error) {
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
