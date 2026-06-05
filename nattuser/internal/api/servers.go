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
	ClientSecret string `json:"client_secret" binding:"required"`
	LocalHost    string `json:"local_host" binding:"required"`
	LocalPort    int    `json:"local_port"`
	AutoStart    bool   `json:"auto_start"`
	Remark       string `json:"remark"`
}

type serverConnectionResponse struct {
	ID           int64                        `json:"id"`
	Name         string                       `json:"name"`
	ServerHost   string                       `json:"server_host"`
	ServerPort   int                          `json:"server_port"`
	DataPort     int                          `json:"data_port"`
	RemotePort   int                          `json:"remote_port"`
	ClientSecret string                       `json:"client_secret"`
	LocalHost    string                       `json:"local_host"`
	LocalPort    int                          `json:"local_port"`
	Status       model.ServerConnectionStatus `json:"status"`
	AutoStart    bool                         `json:"auto_start"`
	LastError    string                       `json:"last_error"`
	Remark       string                       `json:"remark"`
	CreatedAt    string                       `json:"created_at"`
	UpdatedAt    string                       `json:"updated_at"`
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
		Fail(c, http.StatusBadRequest, CodeBadRequest, "分页参数不正确")
		return
	}
	page.Normalize()
	connections, total, err := db.ListServerConnections(c.Request.Context(), h.database, page.Limit(), page.Offset())
	if err != nil {
		h.writeDBError(c, err, "list server connections failed")
		return
	}
	OK(c, NewPageResponse(serverConnectionResponses(connections), total, page))
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
	OK(c, serverConnectionResponseFrom(connection))
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
	OK(c, serverConnectionResponseFrom(connection))
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
	OK(c, serverConnectionResponseFrom(connection))
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
	OK(c, serverConnectionResponseFrom(connection))
}

func (h *ServerHandler) bindAndValidate(c *gin.Context, req *serverConnectionRequest) bool {
	if !bindJSONOrFail(c, req, "服务端连接参数不正确") {
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
		Fail(c, http.StatusBadRequest, CodeBadRequest, "name 为必填项")
	case req.ServerHost == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "server_host 为必填项")
	case !validPort(req.ServerPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "server_port 必须在 1 到 65535 之间")
	case !validPort(req.DataPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "data_port 必须在 1 到 65535 之间")
	case req.ClientSecret == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "client_secret 为必填项")
	case req.LocalHost == "":
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_host 为必填项")
	case !validPort(req.LocalPort):
		Fail(c, http.StatusBadRequest, CodeBadRequest, "local_port 必须在 1 到 65535 之间")
	default:
		return true
	}
	return false
}

func (h *ServerHandler) writeDBError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "服务端连接不存在")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		Fail(c, http.StatusConflict, CodeConflict, "服务端连接配置冲突")
		return
	}
	if h.log != nil {
		h.log.Errorf("%s: %v", fallback, err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, fallback)
}

func serverConnectionResponses(connections []model.ServerConnection) []serverConnectionResponse {
	result := make([]serverConnectionResponse, 0, len(connections))
	for _, connection := range connections {
		result = append(result, serverConnectionResponseFrom(connection))
	}
	return result
}

func serverConnectionResponseFrom(connection model.ServerConnection) serverConnectionResponse {
	return serverConnectionResponse{
		ID:           connection.ID,
		Name:         connection.Name,
		ServerHost:   connection.ServerHost,
		ServerPort:   connection.ServerPort,
		DataPort:     connection.DataPort,
		RemotePort:   connection.RemotePort,
		ClientSecret: connection.ClientSecret,
		LocalHost:    connection.LocalHost,
		LocalPort:    connection.LocalPort,
		Status:       connection.Status,
		AutoStart:    connection.AutoStart,
		LastError:    connection.LastError,
		Remark:       connection.Remark,
		CreatedAt:    connection.CreatedAt,
		UpdatedAt:    connection.UpdatedAt,
	}
}

func parseIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "id 不正确")
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
