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
	LevelDebug Level = iota
	// LevelInfo 信息级别（默认），输出Info及以上级别的日志。
	LevelInfo
	// LevelError 错误级别（最高），仅输出Error级别的日志。
	LevelError
)

// Logger 日志记录器结构体，封装日志级别控制、文件旋转、线程安全和格式化输出。
type Logger struct {
	mu          sync.RWMutex
	level       Level
	dir         string
	clock       func() time.Time
	stdout      io.Writer
	currentDate string
	file        *os.File
	base        *log.Logger
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	if clock == nil {
		clock = time.Now
	}
	if stdout == nil {
		stdout = os.Stdout
	}

	currentDate := clock().Format("2006-01-02")
	file, err := os.OpenFile(filepath.Join(dir, currentDate+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	writer := io.MultiWriter(stdout, file)
	return &Logger{
		level:       parseLevel(level),
		dir:         dir,
		clock:       clock,
		stdout:      stdout,
		currentDate: currentDate,
		file:        file,
		base:        log.New(writer, "", log.LstdFlags|log.Lmicroseconds),
	}, nil
}

// Close 关闭日志文件句柄，释放资源。
// 对nil Logger调用是安全的（返回nil）。
// 返回值：关闭文件时的错误。
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	l.base = nil
	return err
}

// Debugf 输出Debug级别的格式化日志。
// 参数format：printf风格的格式化字符串。
// 参数args：格式化参数。
func (l *Logger) Debugf(format string, args ...any) {
	l.logf(LevelDebug, "DEBUG", format, args...)
}

// Infof 输出Info级别的格式化日志。
// 参数format：printf风格的格式化字符串。
// 参数args：格式化参数。
func (l *Logger) Infof(format string, args ...any) {
	l.logf(LevelInfo, "INFO", format, args...)
}

// Errorf 输出Error级别的格式化日志。
// 参数format：printf风格的格式化字符串。
// 参数args：格式化参数。
func (l *Logger) Errorf(format string, args ...any) {
	l.logf(LevelError, "ERROR", format, args...)
}

// SetLevel 动态修改日志级别。
// 在运行时调整日志输出详细程度（如排查问题时临时开启Debug级别）。
// 参数level：新的日志级别字符串。
func (l *Logger) SetLevel(level string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = parseLevel(level)
}

func (l *Logger) Level() string {
	if l == nil {
		return "info"
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return levelString(l.level)
}

func (l *Logger) enabled(level Level) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.level <= level
}

func (l *Logger) logf(level Level, label string, format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.level > level {
		return
	}
	if err := l.rotateIfNeededLocked(); err != nil {
		return
	}
	if l.base == nil {
		return
	}
	l.base.Printf("[%s] %s %s", label, callerLocation(), fmt.Sprintf(format, args...))
}

func callerLocation() string {
	_, file, line, ok := runtime.Caller(3)
	if !ok {
		return "unknown:0"
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

func (l *Logger) rotateIfNeededLocked() error {
	if l == nil {
		return nil
	}
	currentDate := l.clock().Format("2006-01-02")
	if currentDate == l.currentDate && l.base != nil {
		return nil
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	file, err := os.OpenFile(filepath.Join(l.dir, currentDate+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	l.currentDate = currentDate
	l.file = file
	l.base = log.New(io.MultiWriter(l.stdout, file), "", log.LstdFlags|log.Lmicroseconds)
	return nil
}

func parseLevel(level string) Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return LevelDebug
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

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
