// Package protocol 定义NATT内网穿透协议的消息类型、状态码和辅助函数。
// 协议采用"信令通道+数据通道"的分离架构：控制通道传输JSON消息完成认证、心跳、
// 隧道起停等管理操作；数据通道在完成绑定握手后转成透明TCP代理模式。
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const Version = "1"

const (
	TypeAuthRequest  = "auth_request"
	TypeAuthResponse = "auth_response"
	TypeHeartbeat    = "heartbeat"
	TypeHeartbeatAck = "heartbeat_ack"
	TypeTunnelStart  = "tunnel_start"
	TypeTunnelStop   = "tunnel_stop"
	TypeTunnelStatus = "tunnel_status"
	TypeDataOpen     = "data_open"
	TypeDataBind     = "data_bind"
	TypeDataClose    = "data_close"
	TypeError        = "error"
)

const (
	CodeOK                      = "ok"
	CodeBadRequest              = "bad_request"
	CodeUnauthorized            = "unauthorized"
	CodeConflict                = "conflict"
	CodeUnsupportedType         = "unsupported_type"
	CodeInternalError           = "internal_error"
	CodeLocalServiceUnavailable = "local_service_unavailable"
)

// Message is the small envelope shared by control commands and data-bind
// handshakes; Payload is decoded after Type tells the receiver what to expect.
type Message struct {
	Type         string          `json:"type"`
	RequestID    string          `json:"request_id"`
	ClientID     int64           `json:"client_id,omitempty"`
	TunnelID     int64           `json:"tunnel_id,omitempty"`
	ConnectionID string          `json:"connection_id,omitempty"`
	Timestamp    int64           `json:"timestamp"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type AuthRequest struct {
	ClientSecret    string         `json:"client_secret"`
	ClientName      string         `json:"client_name"`
	ClientVersion   string         `json:"client_version"`
	ProtocolVersion string         `json:"protocol_version"`
	SystemInfo      map[string]any `json:"system_info,omitempty"`
}

type AuthResponse struct {
	Success                  bool   `json:"success"`
	ClientID                 int64  `json:"client_id,omitempty"`
	TunnelID                 int64  `json:"tunnel_id,omitempty"`
	RemotePort               int    `json:"remote_port,omitempty"`
	ProtocolVersion          string `json:"protocol_version"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
	Message                  string `json:"message,omitempty"`
}

type Heartbeat struct {
	ClientTime int64 `json:"client_time"`
}

type HeartbeatAck struct {
	ServerTime int64 `json:"server_time"`
}

type DataOpen struct {
	DataHost  string `json:"data_host,omitempty"`
	DataPort  int    `json:"data_port,omitempty"`
	LocalHost string `json:"local_host,omitempty"`
	LocalPort int    `json:"local_port,omitempty"`
}

type DataBind struct {
	ClientSecret string `json:"client_secret"`
}

type DataClose struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewMessage(messageType string, clientID int64, tunnelID int64, connectionID string, payload any) (Message, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Type:         messageType,
		RequestID:    NewRequestID(),
		ClientID:     clientID,
		TunnelID:     tunnelID,
		ConnectionID: connectionID,
		Timestamp:    time.Now().Unix(),
		Payload:      raw,
	}, nil
}

func NewErrorMessage(requestID string, code string, message string) Message {
	raw, _ := json.Marshal(ProtocolError{Code: code, Message: message})
	if requestID == "" {
		requestID = NewRequestID()
	}
	return Message{
		Type:      TypeError,
		RequestID: requestID,
		Timestamp: time.Now().Unix(),
		Payload:   raw,
	}
}

func DecodePayload[T any](message Message) (T, error) {
	var out T
	if len(message.Payload) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(message.Payload, &out); err != nil {
		return out, fmt.Errorf("decode %s payload: %w", message.Type, err)
	}
	return out, nil
}

func NewRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func marshalPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return raw, nil
}
