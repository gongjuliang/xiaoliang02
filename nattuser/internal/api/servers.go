package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/logger"
	"nattuser/internal/model"

	"github.com/gin-gonic/gin"
)

type ServerHandler struct {
	database *sql.DB
	log      *logger.Logger
	defaults *config.ServerDefaultsConfig
}

type serverConnectionRequest struct {
	Name         string `json:"name" binding:"required"`
	ServerHost   string `json:"server_host"`
	ServerPort   int    `json:"server_port"`
	DataPort     int    `json:"data_port"`
	UseTLS       *bool  `json:"use_tls"`
	ClientSecret string `json:"client_secret" binding:"required"`
	LocalHost    string `json:"local_host" binding:"required"`
	LocalPort    int    `json:"local_port" binding:"required"`
	AutoStart    bool   `json:"auto_start"`
	Remark       string `json:"remark"`
}

func NewServerHandler(database *sql.DB, log *logger.Logger, defaults *config.ServerDefaultsConfig) *ServerHandler {
	return &ServerHandler{
		database: database,
		log:      log,
		defaults: defaults,
	}
}

func (h *ServerHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/tunnel-connections", h.list)
	group.POST("/tunnel-connections", h.create)
	group.PUT("/tunnel-connections/:id", h.update)
	group.DELETE("/tunnel-connections/:id", h.delete)
	group.POST("/tunnel-connections/:id/start", h.start)
	group.POST("/tunnel-connections/:id/stop", h.stop)
}

func (h *ServerHandler) list(c *gin.Context) {
	var page PageRequest
	if err := c.ShouldBindQuery(&page); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid pagination parameters")
		return
	}
	page.Normalize()
	connections, total, err := db.ListServerConnections(c.Request.Context(), h.database, page.Limit(), page.Offset())
	if err != nil {
		h.writeDBError(c, err, "list server connections failed")
		return
	}
	OK(c, NewPageResponse(connections, total, page))
}

func (h *ServerHandler) create(c *gin.Context) {
	var req serverConnectionRequest
	if !h.bindAndValidate(c, &req) {
		return
	}
	connection, err := db.CreateServerConnection(c.Request.Context(), h.database, db.CreateServerConnectionParams{
		Name:         req.Name,
		ServerHost:   req.ServerHost,
		ServerPort:   req.ServerPort,
		DataPort:     req.DataPort,
		UseTLS:       h.resolveUseTLS(req.UseTLS),
		ClientSecret: req.ClientSecret,
		LocalHost:    req.LocalHost,
		LocalPort:    req.LocalPort,
		AutoStart:    req.AutoStart,
		Remark:       req.Remark,
	})
	if err != nil {
		h.writeDBError(c, err, "create server connection failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "server_create", "server_connection", strconv.FormatInt(connection.ID, 10), fmt.Sprintf("created server connection %s", connection.Name), c.ClientIP())
	OK(c, connection)
}

func (h *ServerHandler) update(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req serverConnectionRequest
	if !h.bindAndValidate(c, &req) {
		return
	}
	connection, err := db.UpdateServerConnection(c.Request.Context(), h.database, id, db.UpdateServerConnectionParams{
		Name:         req.Name,
		ServerHost:   req.ServerHost,
		ServerPort:   req.ServerPort,
		DataPort:     req.DataPort,
		UseTLS:       h.resolveUseTLS(req.UseTLS),
		ClientSecret: req.ClientSecret,
		LocalHost:    req.LocalHost,
		LocalPort:    req.LocalPort,
		AutoStart:    req.AutoStart,
		Remark:       req.Remark,
	})
	if err != nil {
		h.writeDBError(c, err, "update server connection failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "server_update", "server_connection", strconv.FormatInt(connection.ID, 10), fmt.Sprintf("updated server connection %s", connection.Name), c.ClientIP())
	OK(c, connection)
}

func (h *ServerHandler) delete(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	connection, err := db.DeleteServerConnection(c.Request.Context(), h.database, id)
	if err != nil {
		h.writeDBError(c, err, "delete server connection failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "server_delete", "server_connection", strconv.FormatInt(connection.ID, 10), fmt.Sprintf("deleted server connection %s", connection.Name), c.ClientIP())
	OK(c, connection)
}

func (h *ServerHandler) start(c *gin.Context) {
	h.setStatus(c, model.ServerConnectionStatusConnected, "server_start", "connected server")
}

func (h *ServerHandler) stop(c *gin.Context) {
	h.setStatus(c, model.ServerConnectionStatusStopped, "server_stop", "stopped server")
}

func (h *ServerHandler) setStatus(c *gin.Context, status model.ServerConnectionStatus, action string, contentPrefix string) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	connection, err := db.SetServerConnectionStatus(c.Request.Context(), h.database, id, status, "")
	if err != nil {
		h.writeDBError(c, err, "set server connection status failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), action, "server_connection", strconv.FormatInt(connection.ID, 10), fmt.Sprintf("%s %s", contentPrefix, connection.Name), c.ClientIP())
	OK(c, connection)
}

func (h *ServerHandler) bindAndValidate(c *gin.Context, req *serverConnectionRequest) bool {
	if err := c.ShouldBindJSON(req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid server connection parameters")
		return false
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ServerHost = strings.TrimSpace(req.ServerHost)
	req.ClientSecret = strings.TrimSpace(req.ClientSecret)
	req.LocalHost = strings.TrimSpace(req.LocalHost)
	if req.ServerHost == "" {
		req.ServerHost = h.defaults.ServerHost
	}
	if req.ServerPort == 0 {
		req.ServerPort = h.defaults.ControlPort
	}
	if req.DataPort == 0 {
		req.DataPort = h.defaults.DataPort
	}

	switch {
	case req.Name == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "name is required")
	case req.ServerHost == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "server_host is required")
	case !validPort(req.ServerPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "server_port must be between 1 and 65535")
	case !validPort(req.DataPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "data_port must be between 1 and 65535")
	case req.ClientSecret == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "client_secret is required")
	case req.LocalHost == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_host is required")
	case !validPort(req.LocalPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_port must be between 1 and 65535")
	default:
		return true
	}
	return false
}

func (h *ServerHandler) resolveUseTLS(value *bool) bool {
	if value == nil {
		return h.defaults.UseTLS
	}
	return *value
}

func (h *ServerHandler) writeDBError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "server connection not found")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		Fail(c, http.StatusConflict, CodeConflict, "server connection conflict")
		return
	}
	if h.log != nil {
		h.log.Errorf("%s: %v", fallback, err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, fallback)
}

func parseIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func currentActor(c *gin.Context) string {
	value, ok := c.Get(authClaimsKey)
	if !ok {
		return "unknown"
	}
	claims, ok := value.(*auth.Claims)
	if !ok || claims.Username == "" {
		return "unknown"
	}
	return claims.Username
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}
