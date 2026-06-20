// Package protocol 定义NATT内网穿透协议的消息类型、状态码和辅助函数。
// 协议采用"信令通道+数据通道"的分离架构：
// 控制通道传输JSON消息完成认证、心跳、隧道起停等管理操作；
// 数据通道在完成绑定握手后转成透明TCP代理模式。
package protocol

import (
	// crypto/rand 提供密码学安全随机数生成，用于产生唯一的请求ID。
	"crypto/rand"
	// encoding/hex 提供十六进制编解码，用于将随机字节转为可读的请求ID字符串。
	"encoding/hex"
	// encoding/json 提供JSON序列化与反序列化，用于Payload编解码。
	"encoding/json"
	// fmt 提供错误信息的格式化输出。
	"fmt"
	// time 提供时间戳获取，用于消息的时间标记。
	"time"
)

// Version 定义当前协议版本号，用于客户端与服务端的版本兼容性校验。
const Version = "1"

// 消息类型常量：定义了NATT协议支持的所有消息类型。
// 控制通道消息用于管理隧道的生命周期，数据通道消息用于建立数据代理连接。
const (
	// TypeAuthRequest 认证请求：客户端携带密钥向服务端发起身份认证。
	TypeAuthRequest = "auth_request"
	// TypeAuthResponse 认证响应：服务端对客户端认证请求的回复。
	TypeAuthResponse = "auth_response"
	// TypeHeartbeat 心跳请求：客户端定期发送以维持连接活性。
	TypeHeartbeat = "heartbeat"
	// TypeHeartbeatAck 心跳确认：服务端对心跳请求的回复。
	TypeHeartbeatAck = "heartbeat_ack"
	// TypeTunnelStart 启动隧道：通知客户端启动指定隧道的数据转发。
	TypeTunnelStart = "tunnel_start"
	// TypeTunnelStop 停止隧道：通知客户端停止指定隧道的数据转发。
	TypeTunnelStop = "tunnel_stop"
	// TypeTunnelStatus 隧道状态：查询或上报隧道的当前运行状态。
	TypeTunnelStatus = "tunnel_status"
	// TypeDataOpen 打开数据通道：通知客户端为指定隧道建立新的数据连接。
	TypeDataOpen = "data_open"
	// TypeDataBind 数据绑定：外部用户连入时携带密钥进行身份绑定。
	TypeDataBind = "data_bind"
	// TypeDataClose 关闭数据连接：通知对端关闭指定的数据通道。
	TypeDataClose = "data_close"
	// TypeError 错误消息：用于向对端报告协议层错误。
	TypeError = "error"
)

// 协议状态码常量：定义消息处理结果的标准化状态码。
const (
	// CodeOK 操作成功。
	CodeOK = "ok"
	// CodeBadRequest 请求格式错误或参数无效。
	CodeBadRequest = "bad_request"
	// CodeUnauthorized 身份认证失败，密钥无效或过期。
	CodeUnauthorized = "unauthorized"
	// CodeConflict 资源冲突（如端口已被占用）。
	CodeConflict = "conflict"
	// CodeUnsupportedType 不支持的消息类型。
	CodeUnsupportedType = "unsupported_type"
	// CodeInternalError 服务器内部错误。
	CodeInternalError = "internal_error"
	// CodeLocalServiceUnavailable 内网目标服务不可达（客户端无法连接到本地服务）。
	CodeLocalServiceUnavailable = "local_service_unavailable"
)

// Message 是控制命令和数据绑定握手共用的信令信封结构体。
// Payload字段的具体格式由Type字段指示，接收方根据Type解码Payload。
type Message struct {
	// Type 消息类型，见上方常量定义。
	Type string `json:"type"`
	// RequestID 请求唯一标识，用于关联请求与响应。
	RequestID string `json:"request_id"`
	// ClientID 客户端ID，标识消息来源或目标客户端。
	ClientID int64 `json:"client_id,omitempty"`
	// TunnelID 隧道ID，标识消息关联的隧道。
	TunnelID int64 `json:"tunnel_id,omitempty"`
	// ConnectionID 数据连接ID，用于标识单个数据通道连接。
	ConnectionID string `json:"connection_id,omitempty"`
	// Timestamp 消息产生时的Unix时间戳（秒）。
	Timestamp int64 `json:"timestamp"`
	// Payload 消息载荷，根据Type动态解析为对应的结构体。
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ProtocolError 协议错误结构体，作为错误消息的Payload内容。
type ProtocolError struct {
	// Code 错误状态码，见上方Code常量定义。
	Code string `json:"code"`
	// Message 人类可读的错误描述信息。
	Message string `json:"message"`
}

// AuthRequest 认证请求的Payload：客户端携带密钥和基本信息向服务端认证。
type AuthRequest struct {
	// ClientSecret 客户端密钥（明文），服务端与数据库中的哈希值比对验证。
	ClientSecret string `json:"client_secret"`
	// ClientName 客户端名称，用于展示和管理。
	ClientName string `json:"client_name"`
	// ClientVersion 客户端软件版本号。
	ClientVersion string `json:"client_version"`
	// ProtocolVersion 客户端支持的协议版本号。
	ProtocolVersion string `json:"protocol_version"`
	// SystemInfo 客户端系统信息（如操作系统、CPU架构等），可选字段。
	SystemInfo map[string]any `json:"system_info,omitempty"`
}

// AuthResponse 认证响应的Payload：服务端告知客户端认证结果和隧道配置。
type AuthResponse struct {
	// Success 认证是否成功。
	Success bool `json:"success"`
	// ClientID 认证成功后分配的客户端ID。
	ClientID int64 `json:"client_id,omitempty"`
	// TunnelID 关联的隧道ID（如有）。
	TunnelID int64 `json:"tunnel_id,omitempty"`
	// RemotePort 分配的远程公网端口。
	RemotePort int `json:"remote_port,omitempty"`
	// ProtocolVersion 服务端使用的协议版本。
	ProtocolVersion string `json:"protocol_version"`
	// HeartbeatIntervalSeconds 心跳间隔（秒），客户端按此频率发送心跳。
	HeartbeatIntervalSeconds int `json:"heartbeat_interval_seconds"`
	// Message 附加说明信息（如失败原因）。
	Message string `json:"message,omitempty"`
}

// Heartbeat 心跳请求Payload：客户端携带当前时间发送心跳。
type Heartbeat struct {
	// ClientTime 客户端当前的Unix时间戳（秒）。
	ClientTime int64 `json:"client_time"`
}

// HeartbeatAck 心跳确认Payload：服务端回复当前时间，并可携带当前隧道状态。
type HeartbeatAck struct {
	// ServerTime 服务端当前的Unix时间戳（秒）。
	ServerTime int64 `json:"server_time"`
	// TunnelStatus 服务端隧道状态，老版本可能不携带。
	TunnelStatus string `json:"tunnel_status,omitempty"`
	// LastError 服务端隧道状态详情。
	LastError string `json:"last_error,omitempty"`
	// RemotePort 服务端公网监听端口。
	RemotePort int `json:"remote_port,omitempty"`
}

// DataOpen 数据通道打开Payload：服务端通知客户端/外部用户建立数据连接。
type DataOpen struct {
	// DataHost 数据通道的监听地址。
	DataHost string `json:"data_host,omitempty"`
	// DataPort 数据通道的监听端口。
	DataPort int `json:"data_port,omitempty"`
	// LocalHost 内网目标服务主机地址。
	LocalHost string `json:"local_host,omitempty"`
	// LocalPort 内网目标服务端口。
	LocalPort int `json:"local_port,omitempty"`
}

// DataBind 数据绑定Payload：外部用户携带隧道密钥进行身份绑定。
type DataBind struct {
	// ClientSecret 隧道访问密钥，用于验证外部用户的访问权限。
	ClientSecret string `json:"client_secret"`
}

// DataClose 数据连接关闭Payload：通知对端关闭指定数据连接。
type DataClose struct {
	// Code 关闭原因的状态码。
	Code string `json:"code"`
	// Message 关闭的详细说明。
	Message string `json:"message"`
}

// NewMessage 创建一条新的协议消息，自动填充请求ID和时间戳。
// 参数messageType：消息类型（如"auth_request"）。
// 参数clientID：关联的客户端ID（可选，0表示不关联）。
// 参数tunnelID：关联的隧道ID（可选，0表示不关联）。
// 参数connectionID：关联的数据连接ID（可选，空字符串表示无关联）。
// 参数payload：消息载荷结构体，将被JSON序列化后存入Payload字段。
// 返回值：构建好的Message和可能的序列化错误。
func NewMessage(messageType string, clientID int64, tunnelID int64, connectionID string, payload any) (Message, error) {
	// 将payload序列化为json.RawMessage
	raw, err := marshalPayload(payload)
	if err != nil {
		return Message{}, err
	}
	// 构建并返回完整的Message结构体
	return Message{
		Type:         messageType,       // 设置消息类型
		RequestID:    NewRequestID(),    // 自动生成唯一请求ID
		ClientID:     clientID,          // 设置客户端ID
		TunnelID:     tunnelID,          // 设置隧道ID
		ConnectionID: connectionID,      // 设置数据连接ID
		Timestamp:    time.Now().Unix(), // 标记当前Unix时间戳
		Payload:      raw,               // 设置序列化后的载荷
	}, nil
}

// NewErrorMessage 创建一条协议错误消息，用于向对端报告错误。
// 参数requestID：关联的原始请求ID（为空时自动生成）。
// 参数code：错误状态码（如"unauthorized"）。
// 参数message：人类可读的错误描述。
// 返回值：构建好的错误Message。
func NewErrorMessage(requestID string, code string, message string) Message {
	// 将ProtocolError结构体序列化为JSON
	raw, _ := json.Marshal(ProtocolError{Code: code, Message: message})
	// 如果未提供请求ID，自动生成一个
	if requestID == "" {
		requestID = NewRequestID()
	}
	// 构建并返回错误消息
	return Message{
		Type:      TypeError,         // 消息类型固定为"error"
		RequestID: requestID,         // 设置请求ID
		Timestamp: time.Now().Unix(), // 标记当前时间戳
		Payload:   raw,               // 设置错误详情载荷
	}
}

// DecodePayload 泛型函数，将Message的Payload字段反序列化为指定类型T。
// 使用方式：authReq, err := DecodePayload[AuthRequest](msg)
// 参数message：包含Payload的Message。
// 返回值：反序列化后的类型T实例和可能的错误。
func DecodePayload[T any](message Message) (T, error) {
	// 声明泛型类型的零值变量
	var out T
	// 如果Payload为空，直接返回零值
	if len(message.Payload) == 0 {
		return out, nil
	}
	// 将JSON Payload反序列化为泛型类型T
	if err := json.Unmarshal(message.Payload, &out); err != nil {
		return out, fmt.Errorf("decode %s payload: %w", message.Type, err)
	}
	return out, nil
}

// NewRequestID 生成一个全局唯一的请求ID。
// 使用16字节密码学安全随机数（crypto/rand），转为32字符的十六进制字符串。
// 如果随机数生成失败则降级使用纳秒时间戳字符串。
// 返回值：唯一请求ID字符串。
func NewRequestID() string {
	// 创建16字节随机数缓冲区
	var buf [16]byte
	// 使用密码学安全随机数填充缓冲区
	if _, err := rand.Read(buf[:]); err != nil {
		// 随机数生成失败时降级为纳秒时间戳字符串
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	// 将随机字节转为十六进制字符串作为请求ID
	return hex.EncodeToString(buf[:])
}

// marshalPayload 将任意类型的payload序列化为json.RawMessage。
// 参数payload：待序列化的载荷（为nil时返回nil）。
// 返回值：序列化后的json.RawMessage和可能的错误。
func marshalPayload(payload any) (json.RawMessage, error) {
	// 如果载荷为nil，返回nil（序列化为JSON的null会导致问题）
	if payload == nil {
		return nil, nil
	}
	// 将载荷序列化为JSON字节
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return raw, nil
}
