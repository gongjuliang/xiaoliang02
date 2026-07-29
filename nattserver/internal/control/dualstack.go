package control

import (
	"net"
	"strconv"
	"strings"
	"sync"
)

type dualTCPListener struct {
	listeners []net.Listener
	accepted  chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

func newDualTCPListener(listeners []net.Listener) net.Listener {
	listener := &dualTCPListener{
		listeners: listeners,
		accepted:  make(chan net.Conn),
		done:      make(chan struct{}),
	}
	for _, item := range listeners {
		go listener.acceptLoop(item)
	}
	return listener
}

func (l *dualTCPListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accepted:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *dualTCPListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
		closeListeners(l.listeners)
	})
	return nil
}

func (l *dualTCPListener) Addr() net.Addr {
	if len(l.listeners) == 0 {
		return &net.TCPAddr{}
	}
	return l.listeners[0].Addr()
}

func (l *dualTCPListener) acceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case l.accepted <- conn:
		case <-l.done:
			_ = conn.Close()
			return
		}
	}
}

func listenTCPDual(ipv4Host string, ipv6Host string, port int) ([]net.Listener, error, error) {
	listener, err := net.Listen("tcp4", net.JoinHostPort(normalizeIPv4Host(ipv4Host), strconv.Itoa(port)))
	if err != nil {
		return nil, nil, err
	}
	listeners := []net.Listener{listener}
	listener6, ipv6Err := net.Listen("tcp6", net.JoinHostPort(normalizeIPv6Host(ipv6Host), strconv.Itoa(port)))
	if ipv6Err != nil {
		return listeners, ipv6Err, nil
	}
	return append(listeners, listener6), nil, nil
}

func listenUDPDual(ipv4Host string, ipv6Host string, port int) ([]*net.UDPConn, error, error) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(normalizeIPv4Host(ipv4Host)), Port: port})
	if err != nil {
		return nil, nil, err
	}
	listeners := []*net.UDPConn{listener}
	listener6, ipv6Err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.ParseIP(normalizeIPv6Host(ipv6Host)), Port: port})
	if ipv6Err != nil {
		return listeners, ipv6Err, nil
	}
	return append(listeners, listener6), nil, nil
}

func closeListeners(listeners []net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}

func closeUDPListeners(listeners []*net.UDPConn) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}

func normalizeIPv4Host(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || host == "::" || host == "[::]" {
		return "0.0.0.0"
	}
	return host
}

func normalizeIPv6Host(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" {
		return "::"
	}
	return strings.Trim(host, "[]")
}
