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

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"

	"github.com/gin-gonic/gin"
)

func TestTunnelKeyManagementFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/server/v1/tunnels", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list status=%d body=%s", rec.Code, rec.Body.String())
	}

	createResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":        "office-tunnel",
		"remote_port": 18080,
		"remark":      "office lab",
	})
	var created tunnelSecretResponse
	decodeResponseData(t, createResp, &created)
	if created.Tunnel.ID == 0 || created.Secret == "" {
		t.Fatalf("unexpected create response: %+v", created)
	}
	if !strings.HasPrefix(created.Secret, "natt_") {
		t.Fatalf("unexpected tunnel secret prefix: %s", created.Secret)
	}
	if strings.Contains(createResp.Body.String(), "secret_hash") {
		t.Fatal("response must not expose secret_hash")
	}

	listResp := authorizedJSON(t, router, http.MethodGet, "/api/server/v1/tunnels?page=1&page_size=10", tokens.AccessToken, nil)
	var page PageResponse
	decodeResponseData(t, listResp, &page)
	if page.Total != 1 {
		t.Fatalf("tunnel total=%d want=1", page.Total)
	}

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/server/v1/tunnels/1", tokens.AccessToken, map[string]any{
		"name":        "office-tunnel-renamed",
		"remote_port": 18081,
		"remark":      "updated",
	})
	var updated model.Tunnel
	decodeResponseData(t, updateResp, &updated)
	if updated.Name != "office-tunnel-renamed" || updated.Remark != "updated" || updated.RemotePort != 18081 {
		t.Fatalf("unexpected update response: %+v", updated)
	}

	disableResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels/1/disable-key", tokens.AccessToken, nil)
	var disabled model.TunnelKey
	decodeResponseData(t, disableResp, &disabled)
	if disabled.Status != model.TunnelKeyStatusDisabled {
		t.Fatalf("tunnel key status=%s want disabled", disabled.Status)
	}

	enableResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels/1/enable-key", tokens.AccessToken, nil)
	var enabled model.TunnelKey
	decodeResponseData(t, enableResp, &enabled)
	if enabled.Status != model.TunnelKeyStatusEnabled {
		t.Fatalf("tunnel key status=%s want enabled", enabled.Status)
	}

	rotateResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels/1/rotate-secret", tokens.AccessToken, nil)
	var rotated tunnelSecretResponse
	decodeResponseData(t, rotateResp, &rotated)
	if rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatal("expected rotated secret to be present and different")
	}
	if rotated.Key.SecretHint == created.Key.SecretHint {
		t.Fatal("expected secret hint to change after rotation")
	}

	assertAuditLogCount(t, database, 6)
}

func setupAuthenticatedServerRouter(t *testing.T) (*gin.Engine, *sql.DB, auth.TokenPair) {
	return setupAuthenticatedServerRouterWithRuntime(t, nil)
}

func setupAuthenticatedServerRouterWithRuntime(t *testing.T, runtime Runtime) (*gin.Engine, *sql.DB, auth.TokenPair) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "test.db")
	cfg.Log.Dir = filepath.Join(dir, "logs")
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Auth.SM2PrivateKeyFile = filepath.Join(dir, "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join(dir, "sm2_public.pem")
	cfg.MCP.Enabled = true
	cfg.MCP.AccessToken = "server-mcp-token"

	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.UpsertSetting(context.Background(), database, "mcp.enabled", "true"); err != nil {
		t.Fatalf("enable mcp setting: %v", err)
	}
	if err := db.UpsertSetting(context.Background(), database, "mcp.access_token", cfg.MCP.AccessToken); err != nil {
		t.Fatalf("set mcp token: %v", err)
	}
	router := NewRouterWithRuntime(cfg, database, nil, runtime)
	publicKey := fetchPublicKey(t, router, "/api/server/v1/auth/sm2-public-key")
	encryptedPassword := encryptForPublicKey(t, publicKey, "admin123456")
	tokens := login(t, router, "/api/server/v1/auth/login", encryptedPassword)
	return router, database, tokens
}

func authorizedJSON(t *testing.T, router http.Handler, method string, path string, accessToken string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s status=%d body=%s", method, path, rec.Code, rec.Body.String())
	}
	return rec
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
