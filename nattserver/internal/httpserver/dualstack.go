package httpserver

import (
	"net"
	"strconv"
	"strings"
)

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

func closeListeners(listeners []net.Listener) {
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
