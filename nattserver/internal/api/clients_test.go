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

func TestClientManagementFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/server/v1/clients", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list status=%d body=%s", rec.Code, rec.Body.String())
	}

	createResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients", tokens.AccessToken, map[string]string{
		"name":   "office-client",
		"remark": "office lab",
	})
	var created clientSecretResponse
	decodeResponseData(t, createResp, &created)
	if created.Client.ID == 0 || created.ClientSecret == "" {
		t.Fatalf("unexpected create response: %+v", created)
	}
	if !strings.HasPrefix(created.ClientSecret, "natt_") {
		t.Fatalf("unexpected client secret prefix: %s", created.ClientSecret)
	}
	if strings.Contains(createResp.Body.String(), "secret_hash") {
		t.Fatal("response must not expose secret_hash")
	}

	listResp := authorizedJSON(t, router, http.MethodGet, "/api/server/v1/clients?page=1&page_size=10", tokens.AccessToken, nil)
	var page PageResponse
	decodeResponseData(t, listResp, &page)
	if page.Total != 1 {
		t.Fatalf("client total=%d want=1", page.Total)
	}

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/server/v1/clients/1", tokens.AccessToken, map[string]string{
		"name":   "office-client-renamed",
		"remark": "updated",
	})
	var updated model.Client
	decodeResponseData(t, updateResp, &updated)
	if updated.Name != "office-client-renamed" || updated.Remark != "updated" {
		t.Fatalf("unexpected update response: %+v", updated)
	}

	disableResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients/1/disable", tokens.AccessToken, nil)
	var disabled model.Client
	decodeResponseData(t, disableResp, &disabled)
	if disabled.Status != model.ClientStatusDisabled {
		t.Fatalf("client status=%s want disabled", disabled.Status)
	}

	enableResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients/1/enable", tokens.AccessToken, nil)
	var enabled model.Client
	decodeResponseData(t, enableResp, &enabled)
	if enabled.Status != model.ClientStatusEnabled {
		t.Fatalf("client status=%s want enabled", enabled.Status)
	}

	rotateResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients/1/rotate-secret", tokens.AccessToken, nil)
	var rotated clientSecretResponse
	decodeResponseData(t, rotateResp, &rotated)
	if rotated.ClientSecret == "" || rotated.ClientSecret == created.ClientSecret {
		t.Fatal("expected rotated secret to be present and different")
	}
	if rotated.Client.SecretHint == created.Client.SecretHint {
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

	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
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
