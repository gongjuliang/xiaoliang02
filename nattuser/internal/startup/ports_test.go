package startup

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestCheckPortsReportsOccupiedPortInChinese(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen occupied port: %v", err)
	}
	defer listener.Close()

	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	err = CheckPorts([]PortCheck{{Name: "HTTP管理端口", Host: "127.0.0.1", Port: port}})
	if err == nil {
		t.Fatal("expected occupied port error")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(port)+"端口被占用") {
		t.Fatalf("error=%q want Chinese occupied port message", err.Error())
	}
}

func TestCheckPortsAllowsFreePort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port probe: %v", err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	if err := CheckPorts([]PortCheck{{Name: "HTTP管理端口", Host: "127.0.0.1", Port: port}}); err != nil {
		t.Fatalf("check free port: %v", err)
	}
}

func TestCheckPortsRejectsDuplicateConfiguredPort(t *testing.T) {
	err := CheckPorts([]PortCheck{
		{Name: "HTTP管理端口", Host: "0.0.0.0", Port: 25520},
		{Name: "另一个监听端口", Host: "127.0.0.1", Port: 25520},
	})
	if err == nil {
		t.Fatal("expected duplicate configured port error")
	}
	if !strings.Contains(err.Error(), "25520端口被占用") {
		t.Fatalf("error=%q want duplicate port occupied message", err.Error())
	}
}
