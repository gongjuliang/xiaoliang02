// Package model 定义NATT内网穿透管理平台的数据模型。
// 包含用户(User)、客户端(Client)、隧道(Tunnel)、隧道密钥(TunnelKey)、
// 审计日志(AuditLog)、系统设置(Setting)、流量统计(TrafficStat)等核心业务实体，
// 以及相关的状态枚举类型(角色、协议、在线状态、隧道状态等)。
package model

// UserRole 用户角色类型，用于区分不同权限级别的用户。
type UserRole string

const (
	// UserRoleAdmin 管理员角色，拥有系统所有管理权限。
	UserRoleAdmin UserRole = "admin"
)

// ClientStatus 客户端状态类型，表示客户端是否被启用。
type ClientStatus string

const (
	// ClientStatusEnabled 客户端已启用，可以正常建立隧道连接。
	ClientStatusEnabled ClientStatus = "enabled"
	// ClientStatusDisabled 客户端已禁用，不允许建立任何隧道连接。
	ClientStatusDisabled ClientStatus = "disabled"
)

// OnlineStatus 在线状态类型，表示客户端或隧道密钥的在线/离线状态。
type OnlineStatus string

const (
	// OnlineStatusOnline 在线状态，表示客户端/密钥当前已连接到服务器。
	OnlineStatusOnline OnlineStatus = "online"
	// OnlineStatusOffline 离线状态，表示客户端/密钥当前未连接到服务器。
	OnlineStatusOffline OnlineStatus = "offline"
)

// TunnelKeyStatus 隧道密钥状态类型，控制密钥是否可用。
type TunnelKeyStatus string

const (
	// TunnelKeyStatusEnabled 密钥已启用，持有该密钥的客户端可以连接对应隧道。
	TunnelKeyStatusEnabled TunnelKeyStatus = "enabled"
	// TunnelKeyStatusDisabled 密钥已禁用，持有该密钥的客户端无法连接对应隧道。
	TunnelKeyStatusDisabled TunnelKeyStatus = "disabled"
)

// TunnelProtocol 隧道协议类型，定义隧道使用的传输协议。
type TunnelProtocol string

const (
	// TunnelProtocolTCP TCP协议隧道，基于TCP进行数据传输。
	TunnelProtocolTCP TunnelProtocol = "tcp"
)

// TunnelStatus 隧道运行状态类型，描述隧道当前所处的生命周期阶段。
type TunnelStatus string

const (
	// TunnelStatusStopped 隧道已停止，未运行。
	TunnelStatusStopped TunnelStatus = "stopped"
	// TunnelStatusWaiting 隧道等待中，等待客户端上线后自动启动。
	TunnelStatusWaiting TunnelStatus = "waiting"
	// TunnelStatusStarting 隧道正在启动中，正在建立连接。
	TunnelStatusStarting TunnelStatus = "starting"
	// TunnelStatusRunning 隧道正在运行中，数据可以正常传输。
	TunnelStatusRunning TunnelStatus = "running"
	// TunnelStatusStopping 隧道正在停止中，正在断开连接。
	TunnelStatusStopping TunnelStatus = "stopping"
	// TunnelStatusError 隧道异常状态，运行过程中出现错误。
	TunnelStatusError TunnelStatus = "error"
)

// User 系统用户模型，存储可登录管理平台的用户信息。
type User struct {
	// ID 用户唯一标识主键。
	ID int64 `json:"id"`
	// Username 用户登录名。
	Username string `json:"username"`
	// PasswordHash 密码哈希值，JSON序列化时隐藏（"-"不导出）。
	PasswordHash string `json:"-"`
	// Role 用户角色，决定其管理权限范围。
	Role UserRole `json:"role"`
	// CreatedAt 用户创建时间。
	CreatedAt string `json:"created_at"`
	// UpdatedAt 用户信息最后更新时间。
	UpdatedAt string `json:"updated_at"`
}

// Client 客户端模型，代表一个内网穿透客户端实例（如内网中的一台机器）。
type Client struct {
	// ID 客户端唯一标识主键。
	ID int64 `json:"id"`
	// Name 客户端名称，用于展示和识别。
	Name string `json:"name"`
	// SecretHash 客户端密钥哈希值，用于验证客户端身份；JSON序列化时隐藏。
	SecretHash string `json:"-"`
	// SecretHint 密钥提示信息，帮助管理员识别密钥用途。
	SecretHint string `json:"secret_hint"`
	// Status 客户端启用/禁用状态。
	Status ClientStatus `json:"status"`
	// OnlineStatus 客户端当前在线/离线状态。
	OnlineStatus OnlineStatus `json:"online_status"`
	// LastIP 客户端最后一次连接时的IP地址。
	LastIP string `json:"last_ip"`
	// LastSeenAt 客户端最后一次在线的时间。
	LastSeenAt string `json:"last_seen_at"`
	// Remark 客户端备注信息。
	Remark string `json:"remark"`
	// CreatedAt 客户端创建时间。
	CreatedAt string `json:"created_at"`
	// UpdatedAt 客户端信息最后更新时间。
	UpdatedAt string `json:"updated_at"`
}

// Tunnel 隧道模型，定义一条从公网入口到内网服务的数据转发通道。
type Tunnel struct {
	// ID 隧道唯一标识主键。
	ID int64 `json:"id"`
	// Name 隧道名称，用于展示和识别。
	Name string `json:"name"`
	// ClientID 所属客户端ID，JSON序列化时隐藏（内部关联使用）。
	ClientID int64 `json:"-"`
	// Protocol 隧道使用的传输协议（如TCP）。
	Protocol TunnelProtocol `json:"protocol"`
	// LocalHost 内网目标主机地址，JSON序列化时隐藏（敏感信息）。
	LocalHost string `json:"-"`
	// LocalPort 内网目标端口号，JSON序列化时隐藏（敏感信息）。
	LocalPort int `json:"-"`
	// RemoteHost 公网监听主机地址，外部用户通过此地址访问隧道。
	RemoteHost string `json:"remote_host"`
	// RemotePort 公网监听端口号，外部用户通过此端口访问隧道。
	RemotePort int `json:"remote_port"`
	// Status 隧道当前运行状态（停止/等待/启动中/运行中/停止中/异常）。
	Status TunnelStatus `json:"status"`
	// AutoStart 是否在客户端上线后自动启动隧道。
	AutoStart bool `json:"auto_start"`
	// LastError 最近一次错误信息，用于故障排查。
	LastError string `json:"last_error"`
	// Secret 隧道访问密钥（明文），空值时JSON省略该字段。
	Secret string `json:"secret,omitempty"`
	// SecretHint 隧道密钥提示信息，空值时JSON省略该字段。
	SecretHint string `json:"secret_hint,omitempty"`
	// BytesIn 隧道累计入站字节数（从外部流入内网的数据量）。
	BytesIn int64 `json:"bytes_in"`
	// BytesOut 隧道累计出站字节数（从内网流出到外部的数据量）。
	BytesOut int64 `json:"bytes_out"`
	// Remark 隧道备注信息。
	Remark string `json:"remark"`
	// CreatedAt 隧道创建时间。
	CreatedAt string `json:"created_at"`
	// UpdatedAt 隧道信息最后更新时间。
	UpdatedAt string `json:"updated_at"`
}

// TunnelKey 隧道密钥模型，用于分发隧道访问凭证给不同的外部用户。
// 一个隧道可以有多个密钥，每个密钥可独立控制启用/禁用和追踪在线状态。
type TunnelKey struct {
	// ID 密钥唯一标识主键。
	ID int64 `json:"id"`
	// TunnelID 关联的隧道ID。
	TunnelID int64 `json:"tunnel_id"`
	// SecretHash 密钥哈希值，用于验证连接；JSON序列化时隐藏。
	SecretHash string `json:"-"`
	// SecretHint 密钥提示信息，帮助识别密钥用途。
	SecretHint string `json:"secret_hint"`
	// SecretPlain 密钥明文（仅在创建时返回一次），JSON序列化时隐藏。
	SecretPlain string `json:"-"`
	// Status 密钥启用/禁用状态。
	Status TunnelKeyStatus `json:"status"`
	// OnlineStatus 使用该密钥的连接当前在线状态。
	OnlineStatus OnlineStatus `json:"online_status"`
	// LastIP 最后使用该密钥连接的IP地址。
	LastIP string `json:"last_ip"`
	// LastSeenAt 最后使用该密钥连接的时间。
	LastSeenAt string `json:"last_seen_at"`
	// CreatedAt 密钥创建时间。
	CreatedAt string `json:"created_at"`
	// UpdatedAt 密钥信息最后更新时间。
	UpdatedAt string `json:"updated_at"`
}

// TunnelWithKey 隧道及其关联密钥的组合模型，用于API响应中同时返回隧道和密钥信息。
// 通过嵌入Tunnel结构体继承所有隧道字段，并附加一个可选的密钥字段。
type TunnelWithKey struct {
	// Tunnel 嵌入的隧道信息，包含所有隧道基本字段。
	Tunnel
	// Key 关联的隧道密钥指针；无密钥时JSON省略该字段。
	Key *TunnelKey `json:"key,omitempty"`
}

// AuditLog 审计日志模型，记录管理员在系统中的重要操作，用于安全审计和追溯。
type AuditLog struct {
	// ID 日志唯一标识主键。
	ID int64 `json:"id"`
	// Actor 操作执行者的用户名。
	Actor string `json:"actor"`
	// Action 操作类型（如create/update/delete等）。
	Action string `json:"action"`
	// TargetType 操作目标类型（如client/tunnel/user等）。
	TargetType string `json:"target_type"`
	// TargetID 操作目标的唯一标识。
	TargetID string `json:"target_id"`
	// Content 操作的详细内容或变更描述。
	Content string `json:"content"`
	// IP 操作执行者的IP地址。
	IP string `json:"ip"`
	// CreatedAt 操作时间。
	CreatedAt string `json:"created_at"`
}

// Setting 系统设置模型，以键值对形式存储系统级别的配置项。
type Setting struct {
	// Key 配置项的键名。
	Key string `json:"key"`
	// Value 配置项的值。
	Value string `json:"value"`
	// UpdatedAt 配置项最后更新时间。
	UpdatedAt string `json:"updated_at"`
}

// TrafficStat 流量统计模型，记录每条隧道的实时和历史流量数据。
type TrafficStat struct {
	// ID 流量统计记录唯一标识主键。
	ID int64 `json:"id"`
	// TunnelID 关联的隧道ID。
	TunnelID int64 `json:"tunnel_id"`
	// ConnectionCount 历史累计连接数。
	ConnectionCount int64 `json:"connection_count"`
	// ActiveConnections 当前活跃连接数。
	ActiveConnections int64 `json:"active_connections"`
	// BytesIn 累计入站字节数（从外部流入内网）。
	BytesIn int64 `json:"bytes_in"`
	// BytesOut 累计出站字节数（从内网流出到外部）。
	BytesOut int64 `json:"bytes_out"`
	// UpdatedAt 统计数据最后更新时间。
	UpdatedAt string `json:"updated_at"`
}
