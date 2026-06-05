package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nattserver/internal/api"
	"nattserver/internal/config"
	"nattserver/internal/db"
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

func TestHTTPServerServesHealthEndpointWithHTTPS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	certFile, keyFile := writeTestCertificate(t, dir)
	cfg := config.Default()
	cfg.HTTP.Host = "127.0.0.1"
	cfg.HTTP.Port = freeHTTPPort(t)
	cfg.HTTP.ShutdownTimeoutSeconds = 1
	cfg.HTTP.HTTPSEnabled = true
	cfg.HTTP.CertFile = certFile
	cfg.HTTP.KeyFile = keyFile
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

	client := &http.Client{
		Timeout: 200 * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	body := getWithRetryClient(t, client, "https://127.0.0.1:"+portText(cfg.HTTP.Port)+"/health")
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

func TestHTTPServerReportsMissingHTTPSCertificateInChinese(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.HTTP.Host = "127.0.0.1"
	cfg.HTTP.Port = freeHTTPPort(t)
	cfg.HTTP.HTTPSEnabled = true
	cfg.HTTP.CertFile = filepath.Join(dir, "missing.crt")
	cfg.HTTP.KeyFile = filepath.Join(dir, "missing.key")

	err := New(cfg.HTTP, http.NewServeMux(), nil).Run(context.Background())
	if err == nil {
		t.Fatal("expected missing certificate error")
	}
	if !strings.Contains(err.Error(), "HTTPS证书文件不存在或不可读取") {
		t.Fatalf("error=%q want Chinese missing certificate message", err.Error())
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
	return getWithRetryClient(t, client, url)
}

func getWithRetryClient(t *testing.T, client *http.Client, url string) string {
	t.Helper()
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

func writeTestCertificate(t *testing.T, dir string) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(time.Hour),
		KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certFile := filepath.Join(dir, "web.crt")
	keyFile := filepath.Join(dir, "web.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		t.Fatalf("write cert file: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return certFile, keyFile
}
