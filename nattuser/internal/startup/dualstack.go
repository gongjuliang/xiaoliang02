package startup

import (
	"net"
	"strconv"
	"strings"
)

func listenTCPDual(ipv4Host, ipv6Host string, port int) ([]net.Listener, error, error) {
	ipv4Host = strings.TrimSpace(ipv4Host)
	if ipv4Host == "" || ipv4Host == "::" || ipv4Host == "[::]" {
		ipv4Host = "0.0.0.0"
	}
	listener4, err := net.Listen("tcp4", net.JoinHostPort(ipv4Host, strconv.Itoa(port)))
	if err != nil {
		return nil, nil, err
	}
	listeners := []net.Listener{listener4}
	ipv6Host = strings.Trim(strings.TrimSpace(ipv6Host), "[]")
	if ipv6Host == "" || ipv6Host == "0.0.0.0" {
		ipv6Host = "::"
	}
	listener6, err := net.Listen("tcp6", net.JoinHostPort(ipv6Host, strconv.Itoa(port)))
	if err != nil {
		return listeners, err, nil
	}
	return append(listeners, listener6), nil, nil
}
