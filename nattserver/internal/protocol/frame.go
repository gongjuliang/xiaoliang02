// Package protocol 定义NATT内网穿透协议的消息格式和帧编码/解码逻辑。
// 协议采用固定4字节大端长度前缀+JSON消息体的帧格式，
// 用于控制通道和数据通道的握手通信。
package protocol

import (
	// encoding/binary 提供大端序整数的编解码，用于帧长度前缀。
	"encoding/binary"
	// encoding/json 提供JSON消息体的序列化与反序列化。
	"encoding/json"
	// fmt 提供错误信息格式化。
	"fmt"
	// io 提供数据流读取接口(io.Reader/io.Writer)。
	"io"
)

// MaxFrameSize 定义了单个协议帧的最大有效载荷大小（4MB）。
// 超过此大小的帧将被拒绝，用于防御内存耗尽攻击。
const MaxFrameSize = 4 * 1024 * 1024

// ReadMessage 从io.Reader中读取并解析一个完整的协议帧，返回解码后的Message。
// 协议帧格式：[4字节大端长度前缀][JSON消息体]
// 参数reader：数据流读取器，通常为net.Conn。
// 返回值：解码后的Message和可能的错误。
func ReadMessage(reader io.Reader) (Message, error) {
	// 创建4字节缓冲区用于读取长度前缀
	var lengthBuf [4]byte
	// 完整读取4字节长度前缀，不完整则返回错误
	if _, err := io.ReadFull(reader, lengthBuf[:]); err != nil {
		return Message{}, err
	}
	// 帧格式采用固定4字节大端序长度前缀，后跟一个JSON消息体，
	// 这种设计保持了解析的简洁性，避免将原始数据混入控制消息。
	// 将4字节大端序数据解析为uint32类型的长度值
	length := binary.BigEndian.Uint32(lengthBuf[:])
	// 校验长度：不能为0且不能超过最大帧大小限制
	if length == 0 || length > MaxFrameSize {
		return Message{}, fmt.Errorf("invalid frame length: %d", length)
	}

	// 根据长度值创建足够大的缓冲区用于存储JSON消息体
	body := make([]byte, length)
	// 完整读取与长度匹配的JSON消息体数据
	if _, err := io.ReadFull(reader, body); err != nil {
		return Message{}, err
	}

	// 声明Message变量用于承载解析结果
	var message Message
	// 将JSON字节数据反序列化为Message结构体
	if err := json.Unmarshal(body, &message); err != nil {
		return Message{}, fmt.Errorf("decode frame json: %w", err)
	}
	// 校验消息类型不能为空
	if message.Type == "" {
		return Message{}, fmt.Errorf("message type is required")
	}
	// 如果请求ID为空，自动生成一个新的请求ID以保证可追溯性
	if message.RequestID == "" {
		message.RequestID = NewRequestID()
	}
	// 返回解析完成的Message
	return message, nil
}

// WriteMessage 将一个Message编码为协议帧并写入io.Writer。
// 编码格式：[4字节大端长度前缀][JSON消息体]
// 参数writer：数据流写入器，通常为net.Conn。
// 参数message：待序列化并发送的Message。
// 返回值：写入过程中的错误。
func WriteMessage(writer io.Writer, message Message) error {
	// 将Message结构体序列化为JSON字节数据
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode frame json: %w", err)
	}
	// 校验编码后的消息体长度是否合法
	if len(body) == 0 || len(body) > MaxFrameSize {
		return fmt.Errorf("invalid encoded frame length: %d", len(body))
	}

	// 相同的长度前缀格式在控制通道和数据绑定握手中使用；
	// 绑定成功后，数据套接字切换为原始TCP代理字节传输。
	// 创建4字节缓冲区存储长度前缀
	var lengthBuf [4]byte
	// 将消息体长度以大端序编码写入长度前缀缓冲区
	binary.BigEndian.PutUint32(lengthBuf[:], uint32(len(body)))
	// 先写入4字节长度前缀
	if _, err := writer.Write(lengthBuf[:]); err != nil {
		return err
	}
	// 再写入JSON消息体
	_, err = writer.Write(body)
	return err
}
