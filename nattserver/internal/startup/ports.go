package startup

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type PortCheck struct {
	Name string
	Host string
	Port int
}

func CheckPorts(checks []PortCheck) error {
	seen := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		host := strings.TrimSpace(check.Host)
		if host == "" {
			host = "0.0.0.0"
		}
		key := strconv.Itoa(check.Port)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%d端口被占用", check.Port)
		}
		seen[key] = struct{}{}

		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(check.Port)))
		if err != nil {
			return fmt.Errorf("%d端口被占用", check.Port)
		}
		if err := listener.Close(); err != nil {
			return fmt.Errorf("关闭端口检查监听失败: %w", err)
		}
	}
	return nil
}
