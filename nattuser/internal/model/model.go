// Package model 定义NATT客户端管理平台的数据模型。
// 包含用户(User)、隧道连接(TunnelConnection/ServerConnection)、
// 本地隧道绑定(LocalTunnel)、审计日志(AuditLog)、系统设置(Setting)等核心业务实体，
// 以及相关的状态枚举类型(角色、连接状态等)。
package model

// UserRole 用户角色类型，用于区分不同权限级别的用户。
type UserRole string

const (
	// UserRoleAdmin 管理员角色，拥有系统所有管理权限。
	UserRoleAdmin UserRole = "admin"
)

// ServerConnectionStatus 服务端连接状态类型，表示客户端与nattserver之间的连接状态。
type ServerConnectionStatus string

const (
	// ServerConnectionStatusStopped 连接已停止。
	ServerConnectionStatusStopped ServerConnectionStatus = "stopped"
	// ServerConnectionStatusConnecting 正在连接中。
	ServerConnectionStatusConnecting ServerConnectionStatus = "connecting"
	// ServerConnectionStatusConnected 已成功连接。
	ServerConnectionStatusConnected ServerConnectionStatus = "connected"
	// ServerConnectionStatusError 连接异常。
	ServerConnectionStatusError ServerConnectionStatus = "error"
)

// User 系统用户模型，存储可登录管理平台的用户信息。
type User struct {
	ID           int64    `json:"id"`
	Username     string   `json:"username"`
	PasswordHash string   `json:"-"`
	Role         UserRole `json:"role"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// TunnelConnection 隧道连接模型，定义客户端到nattserver的一条隧道连接配置。
// 包含服务端地址、端口、密钥、内网目标等信息。
type TunnelConnection struct {
	ID           int64                  `json:"id"`          // 连接唯一标识
	Name         string                 `json:"name"`        // 连接名称
	ServerHost   string                 `json:"server_host"` // 服务端地址
	ServerPort   int                    `json:"server_port"` // 服务端控制端口
	DataPort     int                    `json:"data_port"`   // 服务端数据端口
	RemotePort   int                    `json:"remote_port"` // 服务端公网监听端口（连接成功后由服务端回传）
	ClientSecret string                 `json:"-"`           // 服务端隧道密钥（JSON序列化时隐藏）
	LocalHost    string                 `json:"local_host"`  // 内网目标主机地址
	LocalPort    int                    `json:"local_port"`  // 内网目标端口
	Status       ServerConnectionStatus `json:"status"`      // 连接状态
	AutoStart    bool                   `json:"auto_start"`  // 是否自动启动连接
	LastError    string                 `json:"last_error"`  // 最近一次错误信息
	Remark       string                 `json:"remark"`      // 备注信息
	CreatedAt    string                 `json:"created_at"`  // 创建时间
	UpdatedAt    string                 `json:"updated_at"`  // 最后更新时间
}

// ServerConnection 是TunnelConnection的别名，语义上表示一条到服务端的隧道连接。
type ServerConnection = TunnelConnection

// LocalTunnel 本地隧道绑定模型，将服务端隧道ID映射到内网本地服务地址。
type LocalTunnel struct {
	ID                 int64  `json:"id"`                   // 绑定记录唯一标识
	Name               string `json:"name"`                 // 绑定名称
	ServerConnectionID int64  `json:"server_connection_id"` // 关联的服务端连接ID
	ServerTunnelID     int64  `json:"server_tunnel_id"`     // 关联的服务端隧道ID
	LocalHost          string `json:"local_host"`           // 内网目标主机地址
	LocalPort          int    `json:"local_port"`           // 内网目标端口
	Enabled            bool   `json:"enabled"`              // 是否启用该绑定
	LastError          string `json:"last_error"`           // 最近错误信息
	Remark             string `json:"remark"`               // 备注
	CreatedAt          string `json:"created_at"`           // 创建时间
	UpdatedAt          string `json:"updated_at"`           // 最后更新时间
}

// AuditLog 审计日志模型，记录用户在系统中的重要操作，用于安全审计和追溯。
type AuditLog struct {
	ID         int64  `json:"id"`          // 日志唯一标识
	Actor      string `json:"actor"`       // 操作执行者用户名
	Action     string `json:"action"`      // 操作类型（如create/update/delete）
	TargetType string `json:"target_type"` // 操作目标类型（如tunnel/connection）
	TargetID   string `json:"target_id"`   // 操作目标唯一标识
	Content    string `json:"content"`     // 操作详细内容
	IP         string `json:"ip"`          // 操作执行者IP地址
	CreatedAt  string `json:"created_at"`  // 操作时间
}

// Setting 系统设置模型，以键值对形式存储系统级别的配置项。
type Setting struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at"`
}
