package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/logger"
	"nattserver/internal/model"

	"github.com/gin-gonic/gin"
)

type TunnelHandler struct {
	database *sql.DB
	log      *logger.Logger
	cfg      *config.TunnelConfig
	runtime  TunnelRuntime
}

type TunnelRuntime interface {
	StartTunnel(ctx context.Context, id int64) (model.Tunnel, error)
	StopTunnel(ctx context.Context, id int64) (model.Tunnel, error)
}

type tunnelRequest struct {
	Name       string `json:"name" binding:"required"`
	ClientID   int64  `json:"client_id" binding:"required"`
	Protocol   string `json:"protocol"`
	LocalHost  string `json:"local_host" binding:"required"`
	LocalPort  int    `json:"local_port" binding:"required"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port" binding:"required"`
	AutoStart  bool   `json:"auto_start"`
	Remark     string `json:"remark"`
}

func NewTunnelHandler(database *sql.DB, log *logger.Logger, cfg *config.TunnelConfig, runtime TunnelRuntime) *TunnelHandler {
	return &TunnelHandler{
		database: database,
		log:      log,
		cfg:      cfg,
		runtime:  runtime,
	}
}

func (h *TunnelHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/tunnels", h.list)
	group.POST("/tunnels", h.create)
	group.PUT("/tunnels/:id", h.update)
	group.DELETE("/tunnels/:id", h.delete)
	group.POST("/tunnels/:id/start", h.start)
	group.POST("/tunnels/:id/stop", h.stop)
}

func (h *TunnelHandler) list(c *gin.Context) {
	var page PageRequest
	if err := c.ShouldBindQuery(&page); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid pagination parameters")
		return
	}
	page.Normalize()
	clientID, ok := parseOptionalInt64Query(c, "client_id")
	if !ok {
		return
	}
	tunnels, total, err := db.ListTunnels(c.Request.Context(), h.database, clientID, page.Limit(), page.Offset())
	if err != nil {
		h.writeDBError(c, err, "list tunnels failed")
		return
	}
	OK(c, NewPageResponse(tunnels, total, page))
}

func (h *TunnelHandler) create(c *gin.Context) {
	var req tunnelRequest
	if !h.bindAndValidateTunnelRequest(c, &req) {
		return
	}
	if _, err := db.GetClientByID(c.Request.Context(), h.database, req.ClientID); err != nil {
		h.writeClientLookupError(c, err)
		return
	}

	tunnel, err := db.CreateTunnel(c.Request.Context(), h.database, db.CreateTunnelParams{
		Name:       req.Name,
		ClientID:   req.ClientID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  req.LocalHost,
		LocalPort:  req.LocalPort,
		RemoteHost: req.RemoteHost,
		RemotePort: req.RemotePort,
		AutoStart:  req.AutoStart,
		Remark:     req.Remark,
	})
	if err != nil {
		h.writeDBError(c, err, "create tunnel failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "tunnel_create", "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("created tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, tunnel)
}

func (h *TunnelHandler) update(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req tunnelRequest
	if !h.bindAndValidateTunnelRequest(c, &req) {
		return
	}
	if _, err := db.GetClientByID(c.Request.Context(), h.database, req.ClientID); err != nil {
		h.writeClientLookupError(c, err)
		return
	}

	tunnel, err := db.UpdateTunnel(c.Request.Context(), h.database, id, db.UpdateTunnelParams{
		Name:       req.Name,
		ClientID:   req.ClientID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  req.LocalHost,
		LocalPort:  req.LocalPort,
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

func (h *TunnelHandler) delete(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	if h.runtime != nil {
		if tunnel, err := db.GetTunnelByID(c.Request.Context(), h.database, id); err == nil && tunnel.Status == model.TunnelStatusRunning {
			if _, err := h.runtime.StopTunnel(c.Request.Context(), id); err != nil {
				h.writeRuntimeError(c, err, "stop tunnel before delete failed")
				return
			}
		}
	}
	tunnel, err := db.DeleteTunnel(c.Request.Context(), h.database, id)
	if err != nil {
		h.writeDBError(c, err, "delete tunnel failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "tunnel_delete", "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("deleted tunnel %s", tunnel.Name), c.ClientIP())
	OK(c, tunnel)
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
	tunnel, err := db.SetTunnelStatus(c.Request.Context(), h.database, id, status, "")
	if err != nil {
		h.writeDBError(c, err, "set tunnel status failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), action, "tunnel", strconv.FormatInt(tunnel.ID, 10), fmt.Sprintf("%s %s", contentPrefix, tunnel.Name), c.ClientIP())
	OK(c, tunnel)
}

func (h *TunnelHandler) bindAndValidateTunnelRequest(c *gin.Context, req *tunnelRequest) bool {
	if err := c.ShouldBindJSON(req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid tunnel parameters")
		return false
	}
	req.Name = strings.TrimSpace(req.Name)
	req.LocalHost = strings.TrimSpace(req.LocalHost)
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
		Fail(c, http.StatusBadRequest, CodeBadRequest, "name is required")
	case req.ClientID <= 0:
		Fail(c, http.StatusBadRequest, CodeBadRequest, "client_id is required")
	case req.Protocol != string(model.TunnelProtocolTCP):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "only tcp protocol is supported")
	case req.LocalHost == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_host is required")
	case !validPort(req.LocalPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_port must be between 1 and 65535")
	case !validPort(req.RemotePort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "remote_port must be between 1 and 65535")
	case req.RemotePort < h.cfg.RemotePortMin || req.RemotePort > h.cfg.RemotePortMax:
		Fail(c, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("remote_port must be between %d and %d", h.cfg.RemotePortMin, h.cfg.RemotePortMax))
	default:
		return true
	}
	return false
}

func (h *TunnelHandler) writeClientLookupError(c *gin.Context, err error) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "client_id does not exist")
		return
	}
	if h.log != nil {
		h.log.Errorf("load client for tunnel failed: %v", err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, "load client failed")
}

func (h *TunnelHandler) writeDBError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "tunnel not found")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		Fail(c, http.StatusConflict, CodeConflict, "remote_port already exists")
		return
	}
	if h.log != nil {
		h.log.Errorf("%s: %v", fallback, err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, fallback)
}

func (h *TunnelHandler) writeRuntimeError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "tunnel not found")
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
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid "+key)
		return 0, false
	}
	return parsed, true
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}
