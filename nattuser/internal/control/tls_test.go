package control

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"path/filepath"
	"testing"
	"time"

	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"
)

func TestManagerConnectsAndForwardsDataWhenTLSEnabled(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	localAddr, stopEcho := startLocalEchoServer(t)
	defer stopEcho()
	localHost, localPort := splitHostPort(t, localAddr)

	tlsConfig := testTLSServerConfig(t)
	rawDataListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake TLS data server: %v", err)
	}
	defer rawDataListener.Close()
	dataListener := tls.NewListener(rawDataListener, tlsConfig)
	dataHost, dataPort := splitHostPort(t, rawDataListener.Addr().String())

	rawControlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake TLS control server: %v", err)
	}
	defer rawControlListener.Close()
	controlListener := tls.NewListener(rawControlListener, tlsConfig)
	controlHost, controlPort := splitHostPort(t, rawControlListener.Addr().String())

	connection, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name:         "tls-server",
		ServerHost:   controlHost,
		ServerPort:   controlPort,
		DataPort:     dataPort,
		UseTLS:       true,
		ClientSecret: "natt_client_secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	createLocalTunnelBinding(t, ctx, database, connection.ID, 7, localHost, localPort, true)

	dataDone := make(chan error, 1)
	go runFakeDataServer(t, dataListener, dataDone)
	controlDone := make(chan error, 1)
	go runDataOpenControlServer(t, controlListener, dataHost, dataPort, controlDone)

	manager := NewManagerWithOptions(config.Default(), database, nil, Options{
		ScanInterval:      10 * time.Millisecond,
		ReconnectInterval: 20 * time.Millisecond,
		DialTimeout:       200 * time.Millisecond,
		HeartbeatInterval: 30 * time.Millisecond,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	managerDone := make(chan error, 1)
	go func() {
		managerDone <- manager.Run(runCtx)
	}()

	waitForDataDone(t, dataDone, controlDone)

	stored, err := db.GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get server connection: %v", err)
	}
	if stored.Status != model.ServerConnectionStatusConnected {
		t.Fatalf("server connection status=%s want=%s", stored.Status, model.ServerConnectionStatusConnected)
	}

	cancel()
	select {
	case err := <-managerDone:
		if err != nil {
			t.Fatalf("manager run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for manager shutdown")
	}
}

func testTLSServerConfig(t *testing.T) *tls.Config {
	t.Helper()
	certPEM, keyPEM := selfSignedCertificatePEM(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse TLS key pair: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
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
