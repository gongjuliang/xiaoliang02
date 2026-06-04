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

type ClientHandler struct {
	database *sql.DB
	log      *logger.Logger
	closer   ClientConnectionCloser
}

type ClientConnectionCloser interface {
	DisconnectClient(clientID int64)
}

type createClientRequest struct {
	Name   string `json:"name" binding:"required"`
	Remark string `json:"remark"`
}

type updateClientRequest struct {
	Name   string `json:"name" binding:"required"`
	Remark string `json:"remark"`
}

type clientSecretResponse struct {
	Client       model.Client `json:"client"`
	ClientSecret string       `json:"client_secret"`
}

func NewClientHandler(database *sql.DB, log *logger.Logger, closer ClientConnectionCloser) *ClientHandler {
	return &ClientHandler{
		database: database,
		log:      log,
		closer:   closer,
	}
}

func (h *ClientHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/clients", h.list)
	group.POST("/clients", h.create)
	group.PUT("/clients/:id", h.update)
	group.POST("/clients/:id/enable", h.enable)
	group.POST("/clients/:id/disable", h.disable)
	group.POST("/clients/:id/rotate-secret", h.rotateSecret)
}

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

func (h *ClientHandler) enable(c *gin.Context) {
	h.setStatus(c, model.ClientStatusEnabled, "client_enable", "enabled client")
}

func (h *ClientHandler) disable(c *gin.Context) {
	h.setStatus(c, model.ClientStatusDisabled, "client_disable", "disabled client")
}

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
