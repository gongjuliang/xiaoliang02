// Package protocol 定义NATT内网穿透协议的消息格式和帧编码/解码逻辑。
// 协议采用固定4字节大端长度前缀+JSON消息体的帧格式，
// 用于控制通道和数据通道的握手通信。绑定成功后数据通道切换为原始TCP代理模式。
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const MaxFrameSize = 4 * 1024 * 1024

func ReadMessage(reader io.Reader) (Message, error) {
	var lengthBuf [4]byte
	if _, err := io.ReadFull(reader, lengthBuf[:]); err != nil {
		return Message{}, err
	}
	// Frames use a fixed 4-byte big-endian length prefix followed by one JSON
	// message, which keeps parsing simple without mixing raw data into control.
	length := binary.BigEndian.Uint32(lengthBuf[:])
	if length == 0 || length > MaxFrameSize {
		return Message{}, fmt.Errorf("invalid frame length: %d", length)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return Message{}, err
	}

	var message Message
	if err := json.Unmarshal(body, &message); err != nil {
		return Message{}, fmt.Errorf("decode frame json: %w", err)
	}
	if message.Type == "" {
		return Message{}, fmt.Errorf("message type is required")
	}
	if message.RequestID == "" {
		message.RequestID = NewRequestID()
	}
	return message, nil
}

func WriteMessage(writer io.Writer, message Message) error {
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode frame json: %w", err)
	}
	if len(body) == 0 || len(body) > MaxFrameSize {
		return fmt.Errorf("invalid encoded frame length: %d", len(body))
	}

	// The same length prefix is used by control and data-bind handshakes; after
	// a successful bind, data sockets switch to raw TCP proxy bytes.
	var lengthBuf [4]byte
	binary.BigEndian.PutUint32(lengthBuf[:], uint32(len(body)))
	if _, err := writer.Write(lengthBuf[:]); err != nil {
		return err
	}
	_, err = writer.Write(body)
	return err
}
