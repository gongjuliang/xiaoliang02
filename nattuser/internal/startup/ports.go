// Package startup 提供NATT客户端首次启动初始化相关的工具函数，
// 包括端口可用性检查和初始化向导功能。
package startup

import (
	// fmt 提供错误信息的格式化输出。
	"fmt"
	// net 提供网络连接和端口监听功能，用于检查端口是否被占用。
	"net"
	// strconv 提供整数与字符串的转换，用于端口号格式化。
	"strconv"
	// strings 提供字符串修剪功能。
	"strings"
)

// PortCheck 定义了一个需要检查的端口配置项。
// 用于在服务启动前验证关键监听端口是否可用，避免启动失败。
type PortCheck struct {
	Name string // 端口描述名称（如"HTTP管理端口"），用于错误提示
	Host string // 监听主机地址（如"127.0.0.1"）
	Port int    // 监听端口号
}

// CheckPorts 批量检查所有指定端口是否可用。
// 通过短暂监听每个端口来验证是否有其他进程占用。
// 参数checks：待检查的端口配置列表。
// 返回值：如果有端口不可用则返回错误，全部可用返回nil。
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
