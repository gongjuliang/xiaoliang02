package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"
)

func TestBuildServiceRunnersUsesHTTPAndControlOnly(t *testing.T) {
	runner := func(context.Context) error { return nil }

	if got := len(buildServiceRunners(runner, runner)); got != 2 {
		t.Fatalf("runners=%d want=2", got)
	}
}

func TestConfigFlagDefaultUsesAutoDiscovery(t *testing.T) {
	if got := configFlagDefault(); got != "" {
		t.Fatalf("config flag default=%q want empty auto-discovery path", got)
	}
}

func TestDefaultStartupWorkingDirectoryUsesExecutableDir(t *testing.T) {
	exeDir := filepath.Join(t.TempDir(), "standalone")
	exePath := filepath.Join(exeDir, "nattuser.exe")

	dir, shouldChange, err := defaultStartupWorkingDirectory("", exePath)
	if err != nil {
		t.Fatalf("default startup working directory: %v", err)
	}
	if !shouldChange {
		t.Fatal("expected default startup to use executable directory")
	}
	if dir != exeDir {
		t.Fatalf("startup dir=%q want %q", dir, exeDir)
	}
}

func TestExplicitConfigDoesNotChangeWorkingDirectory(t *testing.T) {
	exePath := filepath.Join(t.TempDir(), "nattuser.exe")

	dir, shouldChange, err := defaultStartupWorkingDirectory("config/config.json", exePath)
	if err != nil {
		t.Fatalf("default startup working directory: %v", err)
	}
	if shouldChange {
		t.Fatalf("explicit config should keep caller working directory, got dir=%q", dir)
	}
}

func TestStartupPortChecksOnlyLocalHTTP(t *testing.T) {
	cfg := config.Default()
	cfg.HTTP.Port = 25520
	cfg.ServerDefaults.ControlPort = 25511
	cfg.ServerDefaults.DataPort = 25512

	checks := startupPortChecks(cfg)
	if len(checks) != 1 {
		t.Fatalf("port checks=%d want 1: %+v", len(checks), checks)
	}
	if checks[0].Port != 25520 {
		t.Fatalf("checked port=%d want 25520", checks[0].Port)
	}
}

func TestNeedsConsoleInitializationWhenUsersAreMissing(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "nattuser.db")

	needsInit, err := needsConsoleInitialization(ctx, cfg)
	if err != nil {
		t.Fatalf("check initialization status: %v", err)
	}
	if !needsInit {
		t.Fatal("expected empty users table to require initialization")
	}
}

func TestNeedsConsoleInitializationFalseAfterAdminCreated(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "nattuser.db")
	createMainTestAdmin(t, cfg.Database.Path)

	needsInit, err := needsConsoleInitialization(ctx, cfg)
	if err != nil {
		t.Fatalf("check initialization status: %v", err)
	}
	if needsInit {
		t.Fatal("expected existing console admin to skip initialization")
	}
}

func TestExplicitConfigWithoutAdminReturnsChineseStartupError(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "nattuser.db")

	_, err := ensureConsoleInitialized(ctx, "config/config.json", cfg)
	if err == nil {
		t.Fatal("expected explicit config without admin to fail")
	}
	if !strings.Contains(err.Error(), "控制台账号尚未初始化") {
		t.Fatalf("error=%q want Chinese initialization error", err.Error())
	}
}

func createMainTestAdmin(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	hash, err := auth.HashPassword("MainTest1234")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := db.CreateUser(ctx, database, db.CreateUserParams{
		Username:     "main_test_admin",
		PasswordHash: hash,
		Role:         model.UserRoleAdmin,
	}); err != nil {
		t.Fatalf("create main test admin: %v", err)
	}
}
