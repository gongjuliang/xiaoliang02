package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nattuser/internal/config"
	"nattuser/internal/db"

	"github.com/gin-gonic/gin"
)

func TestRouterServesEmbeddedFrontend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "test.db")
	cfg.Log.Dir = filepath.Join(dir, "logs")
	cfg.Auth.SM2PrivateKeyFile = filepath.Join(dir, "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join(dir, "sm2_public.pem")
	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	router := NewRouter(cfg, database, nil)

	index := httptest.NewRecorder()
	router.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("index status=%d body=%s", index.Code, index.Body.String())
	}
	if !strings.Contains(index.Body.String(), "NATT Client") {
		t.Fatalf("index does not contain client title: %s", index.Body.String())
	}
	for _, path := range []string{"/login.html", "/dashboard.html", "/servers.html", "/tunnels.html", "/config.html", "/audit.html"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "data-page=") {
			t.Fatalf("%s does not look like a module page: %s", path, rec.Body.String())
		}
	}

	css := httptest.NewRecorder()
	router.ServeHTTP(css, httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil))
	if css.Code != http.StatusOK {
		t.Fatalf("css status=%d body=%s", css.Code, css.Body.String())
	}
	if !strings.Contains(css.Body.String(), ".app-shell") {
		t.Fatalf("css body missing app styles: %s", css.Body.String())
	}

	js := httptest.NewRecorder()
	router.ServeHTTP(js, httptest.NewRequest(http.MethodGet, "/static/js/app.js", nil))
	if js.Code != http.StatusOK {
		t.Fatalf("js status=%d body=%s", js.Code, js.Body.String())
	}
	for _, want := range []string{"renderPager", "data-page-action", "page_size"} {
		if !strings.Contains(js.Body.String(), want) {
			t.Fatalf("app js missing %q", want)
		}
	}
}
