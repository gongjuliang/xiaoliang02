package logger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoggerRotatesFileWhenDateChanges(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 28, 23, 59, 0, 0, time.Local)
	log, err := newWithClock(dir, "debug", func() time.Time { return now }, io.Discard)
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	log.Infof("first-day-message")
	now = now.Add(2 * time.Minute)
	log.Errorf("second-day-message")
	if err := log.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	first := readLogFile(t, filepath.Join(dir, "2026-05-28.log"))
	second := readLogFile(t, filepath.Join(dir, "2026-05-29.log"))
	if !strings.Contains(first, "first-day-message") || strings.Contains(first, "second-day-message") {
		t.Fatalf("unexpected first log file content: %q", first)
	}
	if !strings.Contains(second, "second-day-message") {
		t.Fatalf("unexpected second log file content: %q", second)
	}
}

func readLogFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file %s: %v", path, err)
	}
	return string(content)
}
