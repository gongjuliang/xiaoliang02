package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRecoveryMiddlewareReturnsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.Use(RecoveryMiddleware(nil))
	router.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != CodeInternalError || resp.Message != "internal server error" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.RequestID == "" {
		t.Fatal("expected request id in panic response")
	}
	if got := rec.Header().Get("X-Request-ID"); got != resp.RequestID {
		t.Fatalf("request id header=%q body=%q", got, resp.RequestID)
	}
}
