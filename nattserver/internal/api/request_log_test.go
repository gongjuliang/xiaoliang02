package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nattserver/internal/logger"

	"github.com/gin-gonic/gin"
)

func TestRequestLogMiddlewareWritesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	log, err := logger.New(logDir, "info")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.Use(RequestLogMiddleware(log))
	router.GET("/health", func(c *gin.Context) {
		OK(c, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-ID", "req-test-001")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(logDir, time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	logText := string(content)
	for _, want := range []string{
		"request_id=req-test-001",
		"method=GET",
		"path=/health",
		"status=200",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("log does not contain %q: %s", want, logText)
		}
	}
}
