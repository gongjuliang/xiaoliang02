// Package startup 提供NATT服务端首次启动初始化相关的工具函数，
// 包括端口可用性检查、初始化向导、配置模板生成等功能。
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
	// Name 端口描述名称（如"HTTP管理端口"），用于错误提示。
	Name string
	// Host 监听主机地址（如"0.0.0.0"或"127.0.0.1"）。
	Host string
	// Port 监听端口号。
	Port int
}

// CheckPorts 批量检查所有指定端口是否可用。
// 通过短暂监听每个端口来验证是否有其他进程占用。
// 参数checks：待检查的端口配置列表。
// 返回值：如果有端口不可用则返回错误，全部可用返回nil。
func CheckPorts(checks []PortCheck) error {
	// 使用map记录已检查的端口号，防止重复检查同一端口
	seen := make(map[string]struct{}, len(checks))
	// 遍历所有需要检查的端口
	for _, check := range checks {
		// 去除主机地址的首尾空白
		host := strings.TrimSpace(check.Host)
		// 如果主机地址为空，默认使用0.0.0.0（监听所有网络接口）
		if host == "" {
			host = "0.0.0.0"
		}
		// 使用端口号字符串作为去重键
		key := strconv.Itoa(check.Port)
		// 检查该端口是否已被检查过（同一端口只需检查一次）
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%d端口被占用", check.Port)
		}
		// 标记该端口已检查
		seen[key] = struct{}{}

		// 尝试在指定主机和端口上创建TCP监听器
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(check.Port)))
		if err != nil {
			// 监听失败说明端口已被占用
			return fmt.Errorf("%d端口被占用", check.Port)
		}
		// 监听成功后立即关闭监听器，释放端口使用权
		if err := listener.Close(); err != nil {
			return fmt.Errorf("关闭端口检查监听失败: %w", err)
		}
	}
	// 所有端口都可用
	return nil
}
