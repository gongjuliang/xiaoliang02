package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelError
)

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

func New(dir string, level string) (*Logger, error) {
	return newWithClock(dir, level, time.Now, os.Stdout)
}

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

func (l *Logger) Debugf(format string, args ...any) {
	l.logf(LevelDebug, "DEBUG", format, args...)
}

func (l *Logger) Infof(format string, args ...any) {
	l.logf(LevelInfo, "INFO", format, args...)
}

func (l *Logger) Errorf(format string, args ...any) {
	l.logf(LevelError, "ERROR", format, args...)
}

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
