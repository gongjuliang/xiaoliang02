package control

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"
	"nattserver/internal/protocol"
)

func TestServerControlAndDataConnectionsSupportTLS(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	secret := "natt_client_secret"
	secretHash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash client secret: %v", err)
	}
	client, err := db.CreateClient(ctx, database, db.CreateClientParams{
		Name:       "client-a",
		SecretHash: secretHash,
		SecretHint: auth.SecretHint(secret),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	remotePort := freeTCPPort(t)
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "tls-echo",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  "127.0.0.1",
		LocalPort:  9080,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	certFile, keyFile := writeSelfSignedCertFiles(t)

	controlPort := freeTCPPort(t)
	dataPort := freeTCPPort(t)
	server := NewServer(config.ProtocolConfig{
		ControlHost: "127.0.0.1",
		ControlPort: controlPort,
		DataHost:    "127.0.0.1",
		DataPort:    dataPort,
		TLS: config.TLSConfig{
			Enabled:  true,
			CertFile: certFile,
			KeyFile:  keyFile,
		},
	}, database, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(runCtx)
	}()

	controlConn := authenticateTLSFakeClient(t, controlPort, secret)
	defer controlConn.Close()
	if _, err := server.StartTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if startCommand, err := protocol.ReadMessage(controlConn); err != nil {
		t.Fatalf("read tunnel_start: %v", err)
	} else if startCommand.Type != protocol.TypeTunnelStart {
		t.Fatalf("start command type=%s want=%s", startCommand.Type, protocol.TypeTunnelStart)
	}

	publicConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", remotePort))
	defer publicConn.Close()
	dataOpen, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read data_open: %v", err)
	}

	dataConn := dialTLSWithRetry(t, fmt.Sprintf("127.0.0.1:%d", dataPort))
	defer dataConn.Close()
	bind, err := protocol.NewMessage(protocol.TypeDataBind, client.ID, tunnel.ID, dataOpen.ConnectionID, protocol.DataBind{
		ClientSecret: secret,
	})
	if err != nil {
		t.Fatalf("build data bind: %v", err)
	}
	if err := protocol.WriteMessage(dataConn, bind); err != nil {
		t.Fatalf("write data bind: %v", err)
	}

	echoDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(dataConn)
		line, err := reader.ReadString('\n')
		if err != nil {
			echoDone <- fmt.Errorf("read forwarded data: %w", err)
			return
		}
		_, err = dataConn.Write([]byte(line))
		echoDone <- err
	}()

	if _, err := publicConn.Write([]byte("tls tunnel\n")); err != nil {
		t.Fatalf("write public connection: %v", err)
	}
	_ = publicConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := bufio.NewReader(publicConn).ReadString('\n')
	if err != nil {
		t.Fatalf("read public response: %v", err)
	}
	if got != "tls tunnel\n" {
		t.Fatalf("public response=%q want tls tunnel", got)
	}
	if err := <-echoDone; err != nil {
		t.Fatalf("fake TLS data peer failed: %v", err)
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

func authenticateTLSFakeClient(t *testing.T, controlPort int, secret string) net.Conn {
	t.Helper()
	conn := dialTLSWithRetry(t, fmt.Sprintf("127.0.0.1:%d", controlPort))
	authReq, err := protocol.NewMessage(protocol.TypeAuthRequest, 0, 0, "", protocol.AuthRequest{
		ClientSecret:    secret,
		ClientName:      "client-a",
		ClientVersion:   "test-version",
		ProtocolVersion: protocol.Version,
	})
	if err != nil {
		t.Fatalf("build auth request: %v", err)
	}
	if err := protocol.WriteMessage(conn, authReq); err != nil {
		t.Fatalf("write auth request: %v", err)
	}
	authRespMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read auth response: %v", err)
	}
	if authRespMsg.Type != protocol.TypeAuthResponse {
		t.Fatalf("auth response type=%s want=%s", authRespMsg.Type, protocol.TypeAuthResponse)
	}
	return conn
}

func dialTLSWithRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 100 * time.Millisecond}, "tcp", addr, &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		})
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("dial TLS server %s timed out", addr)
	return nil
}

func writeSelfSignedCertFiles(t *testing.T) (string, string) {
	t.Helper()
	certPEM, keyPEM := selfSignedCertificatePEM(t)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		t.Fatalf("write TLS cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write TLS key: %v", err)
	}
	return certFile, keyFile
}

func selfSignedCertificatePEM(t *testing.T) ([]byte, []byte) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM
}
