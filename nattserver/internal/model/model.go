package model

type UserRole string

const (
	UserRoleAdmin UserRole = "admin"
)

type ClientStatus string

const (
	ClientStatusEnabled  ClientStatus = "enabled"
	ClientStatusDisabled ClientStatus = "disabled"
)

type OnlineStatus string

const (
	OnlineStatusOnline  OnlineStatus = "online"
	OnlineStatusOffline OnlineStatus = "offline"
)

type TunnelKeyStatus string

const (
	TunnelKeyStatusEnabled  TunnelKeyStatus = "enabled"
	TunnelKeyStatusDisabled TunnelKeyStatus = "disabled"
)

type TunnelProtocol string

const (
	TunnelProtocolTCP TunnelProtocol = "tcp"
)

type TunnelStatus string

const (
	TunnelStatusStopped  TunnelStatus = "stopped"
	TunnelStatusStarting TunnelStatus = "starting"
	TunnelStatusRunning  TunnelStatus = "running"
	TunnelStatusStopping TunnelStatus = "stopping"
	TunnelStatusError    TunnelStatus = "error"
)

type User struct {
	ID           int64    `json:"id"`
	Username     string   `json:"username"`
	PasswordHash string   `json:"-"`
	Role         UserRole `json:"role"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

type Client struct {
	ID           int64        `json:"id"`
	Name         string       `json:"name"`
	SecretHash   string       `json:"-"`
	SecretHint   string       `json:"secret_hint"`
	Status       ClientStatus `json:"status"`
	OnlineStatus OnlineStatus `json:"online_status"`
	LastIP       string       `json:"last_ip"`
	LastSeenAt   string       `json:"last_seen_at"`
	Remark       string       `json:"remark"`
	CreatedAt    string       `json:"created_at"`
	UpdatedAt    string       `json:"updated_at"`
}

type Tunnel struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	ClientID   int64          `json:"-"`
	Protocol   TunnelProtocol `json:"protocol"`
	LocalHost  string         `json:"-"`
	LocalPort  int            `json:"-"`
	RemoteHost string         `json:"remote_host"`
	RemotePort int            `json:"remote_port"`
	Status     TunnelStatus   `json:"status"`
	AutoStart  bool           `json:"auto_start"`
	LastError  string         `json:"last_error"`
	Remark     string         `json:"remark"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
}

type TunnelKey struct {
	ID           int64           `json:"id"`
	TunnelID     int64           `json:"tunnel_id"`
	SecretHash   string          `json:"-"`
	SecretHint   string          `json:"secret_hint"`
	Status       TunnelKeyStatus `json:"status"`
	OnlineStatus OnlineStatus    `json:"online_status"`
	LastIP       string          `json:"last_ip"`
	LastSeenAt   string          `json:"last_seen_at"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

type TunnelWithKey struct {
	Tunnel
	Key *TunnelKey `json:"key,omitempty"`
}

type AuditLog struct {
	ID         int64  `json:"id"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Content    string `json:"content"`
	IP         string `json:"ip"`
	CreatedAt  string `json:"created_at"`
}

type Setting struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at"`
}

type TrafficStat struct {
	ID                int64  `json:"id"`
	TunnelID          int64  `json:"tunnel_id"`
	ConnectionCount   int64  `json:"connection_count"`
	ActiveConnections int64  `json:"active_connections"`
	BytesIn           int64  `json:"bytes_in"`
	BytesOut          int64  `json:"bytes_out"`
	UpdatedAt         string `json:"updated_at"`
}
