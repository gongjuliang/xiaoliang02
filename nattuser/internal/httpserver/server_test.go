package httpserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nattuser/internal/api"
	"nattuser/internal/config"
	"nattuser/internal/db"
)

func TestHTTPServerServesHealthEndpoint(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.HTTP.Host = "127.0.0.1"
	cfg.HTTP.Port = freeHTTPPort(t)
	cfg.HTTP.ShutdownTimeoutSeconds = 1
	cfg.Database.Path = filepath.Join(dir, "test.db")
	cfg.Log.Dir = filepath.Join(dir, "logs")
	cfg.Auth.SM2PrivateKeyFile = filepath.Join(dir, "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join(dir, "sm2_public.pem")

	database, err := db.Open(ctx, cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	server := New(cfg.HTTP, api.NewRouter(cfg, database, nil), nil)
	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(runCtx)
	}()

	body := getWithRetry(t, "http://127.0.0.1:"+portText(cfg.HTTP.Port)+"/health")
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("unexpected health body: %s", body)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("server run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}
}

func freeHTTPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	var parsed int
	if _, err := fmt.Sscanf(port, "%d", &parsed); err != nil {
		t.Fatalf("parse port %s: %v", port, err)
	}
	return parsed
}

func getWithRetry(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			raw, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				t.Fatalf("read response body: %v", readErr)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status=%d body=%s", url, resp.StatusCode, string(raw))
			}
			return string(raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s timed out", url)
	return ""
}

func portText(port int) string {
	return strconv.Itoa(port)
}
