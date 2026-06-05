package model

type UserRole string

const (
	UserRoleAdmin UserRole = "admin"
)

type ServerConnectionStatus string

const (
	ServerConnectionStatusStopped    ServerConnectionStatus = "stopped"
	ServerConnectionStatusConnecting ServerConnectionStatus = "connecting"
	ServerConnectionStatusConnected  ServerConnectionStatus = "connected"
	ServerConnectionStatusError      ServerConnectionStatus = "error"
)

type User struct {
	ID           int64    `json:"id"`
	Username     string   `json:"username"`
	PasswordHash string   `json:"-"`
	Role         UserRole `json:"role"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

type TunnelConnection struct {
	ID           int64                  `json:"id"`
	Name         string                 `json:"name"`
	ServerHost   string                 `json:"server_host"`
	ServerPort   int                    `json:"server_port"`
	DataPort     int                    `json:"data_port"`
	RemotePort   int                    `json:"remote_port"`
	ClientSecret string                 `json:"-"`
	LocalHost    string                 `json:"local_host"`
	LocalPort    int                    `json:"local_port"`
	Status       ServerConnectionStatus `json:"status"`
	AutoStart    bool                   `json:"auto_start"`
	LastError    string                 `json:"last_error"`
	Remark       string                 `json:"remark"`
	CreatedAt    string                 `json:"created_at"`
	UpdatedAt    string                 `json:"updated_at"`
}

type ServerConnection = TunnelConnection

type LocalTunnel struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	ServerConnectionID int64  `json:"server_connection_id"`
	ServerTunnelID     int64  `json:"server_tunnel_id"`
	LocalHost          string `json:"local_host"`
	LocalPort          int    `json:"local_port"`
	Enabled            bool   `json:"enabled"`
	LastError          string `json:"last_error"`
	Remark             string `json:"remark"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
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
