package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"

	"github.com/gin-gonic/gin"
)

func TestServerConnectionManagementFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/client/v1/servers", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list status=%d body=%s", rec.Code, rec.Body.String())
	}

	createResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/servers", tokens.AccessToken, map[string]any{
		"name":          "public-server",
		"server_host":   "example.com",
		"server_port":   7000,
		"data_port":     7001,
		"use_tls":       true,
		"client_secret": "natt_test_secret",
		"auto_start":    true,
		"remark":        "prod",
	})
	var created model.ServerConnection
	decodeResponseData(t, createResp, &created)
	if created.ID == 0 || created.Status != model.ServerConnectionStatusStopped || !created.UseTLS || !created.AutoStart {
		t.Fatalf("unexpected created server connection: %+v", created)
	}
	if strings.Contains(createResp.Body.String(), "natt_test_secret") || strings.Contains(createResp.Body.String(), "client_secret") {
		t.Fatal("response must not expose client_secret")
	}

	listResp := authorizedJSON(t, router, http.MethodGet, "/api/client/v1/servers?page=1&page_size=10", tokens.AccessToken, nil)
	var page PageResponse
	decodeResponseData(t, listResp, &page)
	if page.Total != 1 {
		t.Fatalf("server connection total=%d want=1", page.Total)
	}

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/client/v1/servers/1", tokens.AccessToken, map[string]any{
		"name":          "public-server-renamed",
		"server_host":   "192.0.2.10",
		"server_port":   7100,
		"data_port":     7101,
		"use_tls":       false,
		"client_secret": "natt_test_secret_2",
		"auto_start":    false,
		"remark":        "updated",
	})
	var updated model.ServerConnection
	decodeResponseData(t, updateResp, &updated)
	if updated.Name != "public-server-renamed" || updated.ServerPort != 7100 || updated.DataPort != 7101 || updated.AutoStart {
		t.Fatalf("unexpected updated server connection: %+v", updated)
	}

	startResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/servers/1/start", tokens.AccessToken, nil)
	var connected model.ServerConnection
	decodeResponseData(t, startResp, &connected)
	if connected.Status != model.ServerConnectionStatusConnected {
		t.Fatalf("server connection status=%s want connected", connected.Status)
	}

	stopResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/servers/1/stop", tokens.AccessToken, nil)
	var stopped model.ServerConnection
	decodeResponseData(t, stopResp, &stopped)
	if stopped.Status != model.ServerConnectionStatusStopped {
		t.Fatalf("server connection status=%s want stopped", stopped.Status)
	}

	deleteResp := authorizedJSON(t, router, http.MethodDelete, "/api/client/v1/servers/1", tokens.AccessToken, nil)
	var deleted model.ServerConnection
	decodeResponseData(t, deleteResp, &deleted)
	if deleted.ID != created.ID {
		t.Fatalf("deleted server connection id=%d want=%d", deleted.ID, created.ID)
	}
	assertAuditLogCount(t, database, 6)
}

func TestServerConnectionCreateUsesDefaultsAndRejectsBadPorts(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	createResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/servers", tokens.AccessToken, map[string]any{
		"name":          "defaulted",
		"client_secret": "natt_default_secret",
	})
	var created model.ServerConnection
	decodeResponseData(t, createResp, &created)
	if created.ServerHost != "127.0.0.1" || created.ServerPort != 7000 || created.DataPort != 7001 {
		t.Fatalf("defaults were not applied: %+v", created)
	}

	resp := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/client/v1/servers", tokens.AccessToken, map[string]any{
		"name":          "bad-port",
		"server_port":   70000,
		"client_secret": "natt_default_secret",
	}, http.StatusBadRequest)
	assertResponseCode(t, resp, CodeBadRequest)
}

func setupAuthenticatedClientRouter(t *testing.T) (*gin.Engine, *sql.DB, auth.TokenPair) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "test.db")
	cfg.Log.Dir = filepath.Join(dir, "logs")
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Auth.SM2PrivateKeyFile = filepath.Join(dir, "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join(dir, "sm2_public.pem")

	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	router := NewRouter(cfg, database, nil)
	publicKey := fetchPublicKey(t, router, "/api/client/v1/auth/sm2-public-key")
	encryptedPassword := encryptForPublicKey(t, publicKey, "admin123456")
	tokens := login(t, router, "/api/client/v1/auth/login", encryptedPassword)
	return router, database, tokens
}

func authorizedJSON(t *testing.T, router http.Handler, method string, path string, accessToken string, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := authorizedJSONRaw(t, router, method, path, accessToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s status=%d body=%s", method, path, rec.Code, rec.Body.String())
	}
	return rec
}

func authorizedJSONAllowStatus(t *testing.T, router http.Handler, method string, path string, accessToken string, body any, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	rec := authorizedJSONRaw(t, router, method, path, accessToken, body)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	return rec
}

func authorizedJSONRaw(t *testing.T, router http.Handler, method string, path string, accessToken string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := makeJSONRequest(t, method, path, body)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func makeJSONRequest(t *testing.T, method string, path string, body any) *http.Request {
	t.Helper()
	if body == nil {
		return httptest.NewRequest(method, path, nil)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeResponseData(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != CodeOK {
		t.Fatalf("response code=%d body=%s", resp.Code, rec.Body.String())
	}
	if err := json.Unmarshal(resp.Data, target); err != nil {
		t.Fatalf("decode data: %v body=%s", err, rec.Body.String())
	}
}

func assertResponseCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	var resp struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response code: %v", err)
	}
	if resp.Code != want {
		t.Fatalf("response code=%d want=%d body=%s", resp.Code, want, rec.Body.String())
	}
}
