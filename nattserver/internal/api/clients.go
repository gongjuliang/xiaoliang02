// Package api 提供客户端授权管理的Web API处理器。
// 包含客户端CRUD（创建/查询/更新）、状态管理（启用/禁用）、
// 密钥轮换等完整的客户端生命周期管理端点。
package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"nattserver/internal/auth"
	"nattserver/internal/db"
	"nattserver/internal/logger"
	"nattserver/internal/model"

	"github.com/gin-gonic/gin"
)

// ClientHandler 客户端管理HTTP API处理器，提供客户端的完整生命周期管理。
// 包含客户端的增删改查以及启用/禁用/密钥轮换等状态管理功能。
type ClientHandler struct {
	database *sql.DB                // 数据库连接
	log      *logger.Logger         // 日志记录器
	closer   ClientConnectionCloser // 客户端连接关闭器（禁用客户端时断开在线连接）
}

// ClientConnectionCloser 客户端连接关闭器接口，用于在禁用客户端时断开其控制连接。
// 由control.Server实现，注入到ClientHandler中。
type ClientConnectionCloser interface {
	DisconnectClient(clientID int64)
}

// createClientRequest 创建客户端时的请求参数。
type createClientRequest struct {
	Name   string `json:"name" binding:"required"` // 客户端名称（必填）
	Remark string `json:"remark"`                  // 备注信息
}

// updateClientRequest 更新客户端时的请求参数。
type updateClientRequest struct {
	Name   string `json:"name" binding:"required"` // 客户端名称（必填）
	Remark string `json:"remark"`                  // 备注信息
}

// clientSecretResponse 客户端创建及密钥轮换时的响应体，同时返回客户端信息和明文密钥。
// 密钥明文仅在创建或轮换时返回一次，之后不可再获取。
type clientSecretResponse struct {
	Client       model.Client `json:"client"`        // 客户端信息
	ClientSecret string       `json:"client_secret"` // 客户端密钥明文（仅此时可见）
}

// NewClientHandler 创建客户端管理HTTP API处理器。
// 参数database：数据库连接。
// 参数log：日志记录器。
// 参数closer：客户端连接关闭器（禁用客户端时使用）。
// 返回值：初始化好的ClientHandler。
func NewClientHandler(database *sql.DB, log *logger.Logger, closer ClientConnectionCloser) *ClientHandler {
	return &ClientHandler{
		database: database,
		log:      log,
		closer:   closer,
	}
}

// RegisterRoutes 注册客户端管理的REST端点：列表/创建/更新/启用/禁用/密钥轮换。
func (h *ClientHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/clients", h.list)
	group.POST("/clients", h.create)
	group.PUT("/clients/:id", h.update)
	group.POST("/clients/:id/enable", h.enable)
	group.POST("/clients/:id/disable", h.disable)
	group.POST("/clients/:id/rotate-secret", h.rotateSecret)
}

// list 处理分页查询客户端列表的请求。
func (h *ClientHandler) list(c *gin.Context) {
	var page PageRequest
	if err := c.ShouldBindQuery(&page); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "分页参数不正确")
		return
	}
	page.Normalize()
	clients, total, err := db.ListClients(c.Request.Context(), h.database, page.Limit(), page.Offset())
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "查询客户端失败")
		return
	}
	OK(c, NewPageResponse(clients, total, page))
}

// create 创建新客户端，生成"xiaoliang_"前缀的密钥并返回明文。
func (h *ClientHandler) create(c *gin.Context) {
	var req createClientRequest
	if !bindJSONOrFail(c, &req, "客户端参数不正确") {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "name 为必填项")
		return
	}

	secret, secretHash, secretHint, err := buildClientSecret()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成客户端秘钥失败")
		return
	}
	client, err := db.CreateClient(c.Request.Context(), h.database, db.CreateClientParams{
		Name:       req.Name,
		SecretHash: secretHash,
		SecretHint: secretHint,
		Remark:     req.Remark,
	})
	if err != nil {
		h.writeDBError(c, err, "create client failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "client_create", "client", strconv.FormatInt(client.ID, 10), fmt.Sprintf("created client %s", client.Name), c.ClientIP())
	OK(c, clientSecretResponse{Client: client, ClientSecret: secret})
}

// update 更新客户端名称和备注信息。
func (h *ClientHandler) update(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req updateClientRequest
	if !bindJSONOrFail(c, &req, "客户端参数不正确") {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "name 为必填项")
		return
	}
	client, err := db.UpdateClient(c.Request.Context(), h.database, id, db.UpdateClientParams{
		Name:   req.Name,
		Remark: req.Remark,
	})
	if err != nil {
		h.writeDBError(c, err, "update client failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "client_update", "client", strconv.FormatInt(client.ID, 10), fmt.Sprintf("updated client %s", client.Name), c.ClientIP())
	OK(c, client)
}

// enable 启用指定客户端，使其可以建立隧道连接。
func (h *ClientHandler) enable(c *gin.Context) {
	h.setStatus(c, model.ClientStatusEnabled, "client_enable", "enabled client")
}

// disable 禁用指定客户端，断开其在线连接并阻止新的连接尝试。
func (h *ClientHandler) disable(c *gin.Context) {
	h.setStatus(c, model.ClientStatusDisabled, "client_disable", "disabled client")
}

// rotateSecret 轮换客户端密钥，生成新密钥并返回明文（仅此时可见）。
func (h *ClientHandler) rotateSecret(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	secret, secretHash, secretHint, err := buildClientSecret()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成客户端秘钥失败")
		return
	}
	client, err := db.RotateClientSecret(c.Request.Context(), h.database, id, secretHash, secretHint)
	if err != nil {
		h.writeDBError(c, err, "rotate client secret failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), "client_rotate_secret", "client", strconv.FormatInt(client.ID, 10), fmt.Sprintf("rotated secret for client %s", client.Name), c.ClientIP())
	OK(c, clientSecretResponse{Client: client, ClientSecret: secret})
}

// setStatus 通用的客户端状态变更方法，处理启用/禁用并记录审计日志。
// 参数status：目标状态（enabled/disabled）。
// 参数action：审计日志操作类型。
// 参数contentPrefix：审计日志内容前缀。
func (h *ClientHandler) setStatus(c *gin.Context, status model.ClientStatus, action string, contentPrefix string) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	client, err := db.SetClientStatus(c.Request.Context(), h.database, id, status)
	if err != nil {
		h.writeDBError(c, err, "set client status failed")
		return
	}
	if status == model.ClientStatusDisabled && h.closer != nil {
		h.closer.DisconnectClient(id)
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, currentActor(c), action, "client", strconv.FormatInt(client.ID, 10), fmt.Sprintf("%s %s", contentPrefix, client.Name), c.ClientIP())
	OK(c, client)
}

func (h *ClientHandler) writeDBError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, db.ErrNotFound) {
		Fail(c, http.StatusNotFound, CodeNotFound, "客户端不存在")
		return
	}
	if errors.Is(err, db.ErrConflict) {
		Fail(c, http.StatusConflict, CodeConflict, "客户端名称已存在")
		return
	}
	if h.log != nil {
		h.log.Errorf("%s: %v", fallback, err)
	}
	Fail(c, http.StatusInternalServerError, CodeInternalError, fallback)
}

func buildClientSecret() (plain string, hash string, hint string, err error) {
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
