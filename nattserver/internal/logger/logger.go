// Package logger 提供支持日志级别、按日滚动的日志记录器。
// 日志同时输出到标准输出（控制台）和按日期命名的日志文件，
// 支持Debug/Info/Error三个级别，线程安全，可动态修改日志级别。
package logger

import (
	// fmt 提供格式化字符串输出。
	"fmt"
	// io 提供MultiWriter等I/O组合功能，实现同时写入标准输出和文件。
	"io"
	// log 提供Go标准日志库的基础日志功能（时间戳前缀等）。
	"log"
	// os 提供文件和标准输出的打开与操作。
	"os"
	// path/filepath 提供文件路径的拼接。
	"path/filepath"
	// runtime 提供运行时信息获取，如调用栈中的文件名和行号。
	"runtime"
	// strings 提供字符串大小写转换和修剪。
	"strings"
	// sync 提供RWMutex读写锁，保证日志写入的线程安全。
	"sync"
	// time 提供时间获取和格式化，用于日志文件名和旋转判断。
	"time"
)

// Level 日志级别类型，使用iota枚举值。
type Level int

const (
	// LevelDebug 调试级别（最低），输出Debug及以上所有级别的日志。
	LevelDebug Level = iota // iota=0
	// LevelInfo 信息级别（默认），输出Info及以上级别的日志。
	LevelInfo // iota=1
	// LevelError 错误级别（最高），仅输出Error级别的日志。
	LevelError // iota=2
)

// Logger 日志记录器结构体，封装日志级别控制、文件旋转、线程安全和格式化输出。
type Logger struct {
	// mu 读写锁，保护Logger内部状态的并发访问。
	mu sync.RWMutex
	// level 当前日志级别，低于此级别的日志将被忽略。
	level Level
	// dir 日志文件存储目录。
	dir string
	// clock 时间获取函数（可注入用于测试）。
	clock func() time.Time
	// stdout 标准输出写入器（可注入用于测试）。
	stdout io.Writer
	// currentDate 当前日志文件对应的日期字符串（格式：2006-01-02）。
	currentDate string
	// file 当前日志文件的句柄。
	file *os.File
	// base Go标准库日志实例，负责实际格式化输出。
	base *log.Logger
}

// New 创建日志记录器实例，使用真实时钟和标准输出。
// 参数dir：日志文件存储目录（会自动创建）。
// 参数level：日志级别字符串（"debug"/"info"/"error"）。
// 返回值：日志记录器指针和可能的错误。
func New(dir string, level string) (*Logger, error) {
	// 委托给内部实现，传入真实时钟和标准输出
	return newWithClock(dir, level, time.Now, os.Stdout)
}

// newWithClock 内部创建函数，支持注入时钟和输出流（用于测试）。
// 参数dir：日志文件存储目录。
// 参数level：日志级别字符串。
// 参数clock：时间获取函数。
// 参数stdout：标准输出写入器。
// 返回值：日志记录器指针和可能的错误。
func newWithClock(dir string, level string, clock func() time.Time, stdout io.Writer) (*Logger, error) {
	// 确保日志目录存在（权限755）
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	// 如果时钟函数为空，使用真实的time.Now
	if clock == nil {
		clock = time.Now
	}
	// 如果标准输出为空，使用os.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	// 获取当前日期字符串作为日志文件名
	currentDate := clock().Format("2006-01-02")
	// 打开或创建以日期命名的日志文件（追加模式，权限644）
	file, err := os.OpenFile(filepath.Join(dir, currentDate+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	// 创建MultiWriter：同时写入标准输出和日志文件
	writer := io.MultiWriter(stdout, file)
	// 构建并返回Logger实例
	return &Logger{
		level:       parseLevel(level),                                    // 解析日志级别字符串
		dir:         dir,                                                  // 保存日志目录
		clock:       clock,                                                // 保存时钟函数
		stdout:      stdout,                                               // 保存标准输出流
		currentDate: currentDate,                                          // 记录当前日期
		file:        file,                                                 // 保存文件句柄
		base:        log.New(writer, "", log.LstdFlags|log.Lmicroseconds), // 创建标准日志实例（含时间戳和微秒）
	}, nil
}

// Close 关闭日志文件句柄，释放资源。
// 对nil Logger调用是安全的（返回nil）。
// 返回值：关闭文件时的错误。
func (l *Logger) Close() error {
	// nil Logger安全检查
	if l == nil {
		return nil
	}
	// 加写锁保护文件操作
	l.mu.Lock()
	defer l.mu.Unlock()
	// 文件已关闭或为nil
	if l.file == nil {
		return nil
	}
	// 关闭文件句柄并置空相关字段
	err := l.file.Close()
	l.file = nil // 置空文件句柄防止重复关闭
	l.base = nil // 置空base logger
	return err
}

// Debugf 输出Debug级别的格式化日志。
// 参数format：printf风格的格式化字符串。
// 参数args：格式化参数。
func (l *Logger) Debugf(format string, args ...any) {
	// 调用内部日志方法，级别为Debug，标签为"DEBUG"
	l.logf(LevelDebug, "DEBUG", format, args...)
}

// Infof 输出Info级别的格式化日志。
// 参数format：printf风格的格式化字符串。
// 参数args：格式化参数。
func (l *Logger) Infof(format string, args ...any) {
	// 调用内部日志方法，级别为Info，标签为"INFO"
	l.logf(LevelInfo, "INFO", format, args...)
}

// Errorf 输出Error级别的格式化日志。
// 参数format：printf风格的格式化字符串。
// 参数args：格式化参数。
func (l *Logger) Errorf(format string, args ...any) {
	// 调用内部日志方法，级别为Error，标签为"ERROR"
	l.logf(LevelError, "ERROR", format, args...)
}

// SetLevel 动态修改日志级别。
// 在运行时调整日志输出详细程度（如排查问题时临时开启Debug级别）。
// 参数level：新的日志级别字符串。
func (l *Logger) SetLevel(level string) {
	// nil Logger安全检查
	if l == nil {
		return
	}
	// 加写锁保护级别变更
	l.mu.Lock()
	defer l.mu.Unlock()
	// 解析并设置新的日志级别
	l.level = parseLevel(level)
}

// Level 获取当前日志级别的字符串表示。
// 返回值：当前日志级别字符串（"debug"/"info"/"error"）。
func (l *Logger) Level() string {
	// nil Logger返回默认值"info"
	if l == nil {
		return "info"
	}
	// 加读锁获取级别
	l.mu.RLock()
	defer l.mu.RUnlock()
	return levelString(l.level)
}

// enabled 判断指定级别是否应该被输出（即当前配置级别是否足够低）。
// 参数level：待判断的日志级别。
// 返回值：如果应该输出返回true。
func (l *Logger) enabled(level Level) bool {
	// nil Logger不输出任何日志
	if l == nil {
		return false
	}
	// 加读锁读取当前级别
	l.mu.RLock()
	defer l.mu.RUnlock()
	// Level枚举值越小优先级越高：Debug(0) < Info(1) < Error(2)
	// 当前级别 <= 待输出级别 时允许输出
	return l.level <= level
}

// logf 内部日志输出方法，包含级别过滤、文件旋转和格式化输出逻辑。
// 参数level：日志级别。
// 参数label：日志标签字符串（"DEBUG"/"INFO"/"ERROR"）。
// 参数format：格式化字符串。
// 参数args：格式化参数。
func (l *Logger) logf(level Level, label string, format string, args ...any) {
	// nil Logger安全检查
	if l == nil {
		return
	}
	// 加写锁保护并发日志写入
	l.mu.Lock()
	defer l.mu.Unlock()
	// 级别过滤：当前级别高于请求级别时忽略
	if l.level > level {
		return
	}
	// 尝试按日期旋转日志文件（跨天自动切换新文件）
	if err := l.rotateIfNeededLocked(); err != nil {
		return // 旋转失败则丢弃本条日志
	}
	// base logger可能已置为nil（如Close后）
	if l.base == nil {
		return
	}
	// 格式化输出：[标签] 文件名:行号 格式化消息
	l.base.Printf("[%s] %s %s", label, callerLocation(), fmt.Sprintf(format, args...))
}

// callerLocation 获取调用者的文件名和行号。
// 跳过3层调用栈（logf -> Debugf/Infof/Errorf -> 实际调用者）。
// 返回值："文件名:行号"格式的字符串。
func callerLocation() string {
	// Caller(3)跳过三层：callerLocation -> logf -> Debugf/Infof/Errorf -> 实际调用者
	_, file, line, ok := runtime.Caller(3)
	if !ok {
		// 获取调用栈信息失败
		return "unknown:0"
	}
	// 只保留文件名（去掉完整路径），拼接行号
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

// rotateIfNeededLocked 检查是否需要按日期旋转日志文件（调用者必须持有写锁）。
// 如果日期变更（跨天），关闭旧文件并创建新的日期日志文件。
// 返回值：旋转过程中的错误。
func (l *Logger) rotateIfNeededLocked() error {
	// nil Logger安全检查
	if l == nil {
		return nil
	}
	// 获取当前日期字符串
	currentDate := l.clock().Format("2006-01-02")
	// 日期未变且base logger仍然有效，无需旋转
	if currentDate == l.currentDate && l.base != nil {
		return nil
	}
	// 关闭旧日志文件（如果存在）
	if l.file != nil {
		_ = l.file.Close() // 忽略关闭错误
		l.file = nil       // 置空文件句柄
	}
	// 创建新的日期日志文件
	file, err := os.OpenFile(filepath.Join(l.dir, currentDate+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	// 更新Logger状态
	l.currentDate = currentDate                                                           // 更新当前日期
	l.file = file                                                                         // 保存新文件句柄
	l.base = log.New(io.MultiWriter(l.stdout, file), "", log.LstdFlags|log.Lmicroseconds) // 重建base logger
	return nil
}

// parseLevel 将日志级别字符串解析为Level枚举值。
// 参数level：日志级别字符串（大小写不敏感）。
// 返回值：解析后的Level枚举值，未知级别默认返回LevelInfo。
func parseLevel(level string) Level {
	switch strings.ToLower(strings.TrimSpace(level)) { // 转为小写并去除空白
	case "debug":
		return LevelDebug // 调试级别
	case "error":
		return LevelError // 错误级别
	default:
		return LevelInfo // 默认信息级别
	}
}

// levelString 将Level枚举值转为字符串表示。
// 参数level：Level枚举值。
// 返回值：对应的字符串表示。
func levelString(level Level) string {
	switch level {
	case LevelDebug:
		return "debug"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}
