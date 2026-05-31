package auth

import (
	"testing"
	"time"

	"nattserver/internal/model"
)

func TestJWTServiceGenerateAndParseTokens(t *testing.T) {
	service := NewJWTService("test-secret", time.Minute, time.Hour)
	user := model.User{
		ID:       7,
		Username: "admin",
		Role:     model.UserRoleAdmin,
	}

	tokens, err := service.GeneratePair(user)
	if err != nil {
		t.Fatalf("generate token pair: %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatal("expected access and refresh tokens")
	}

	accessClaims, err := service.ParseAccessToken(tokens.AccessToken)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	if accessClaims.UserID != user.ID || accessClaims.Username != user.Username {
		t.Fatalf("unexpected access claims: %+v", accessClaims)
	}

	refreshClaims, err := service.ParseRefreshToken(tokens.RefreshToken)
	if err != nil {
		t.Fatalf("parse refresh token: %v", err)
	}
	if refreshClaims.TokenType != TokenTypeRefresh {
		t.Fatalf("unexpected refresh token type: %s", refreshClaims.TokenType)
	}
}

func TestJWTServiceRejectsWrongTokenType(t *testing.T) {
	service := NewJWTService("test-secret", time.Minute, time.Hour)
	tokens, err := service.GeneratePair(model.User{ID: 1, Username: "admin", Role: model.UserRoleAdmin})
	if err != nil {
		t.Fatalf("generate token pair: %v", err)
	}
	if _, err := service.ParseAccessToken(tokens.RefreshToken); err == nil {
		t.Fatal("expected refresh token to be rejected as access token")
	}
}

func TestJWTServiceRejectsExpiredTokens(t *testing.T) {
	user := model.User{ID: 1, Username: "admin", Role: model.UserRoleAdmin}

	expiredAccess := NewJWTService("test-secret", -time.Minute, time.Hour)
	tokens, err := expiredAccess.GeneratePair(user)
	if err != nil {
		t.Fatalf("generate expired access token pair: %v", err)
	}
	if _, err := expiredAccess.ParseAccessToken(tokens.AccessToken); err == nil {
		t.Fatal("expected expired access token to be rejected")
	}

	expiredRefresh := NewJWTService("test-secret", time.Minute, -time.Minute)
	tokens, err = expiredRefresh.GeneratePair(user)
	if err != nil {
		t.Fatalf("generate expired refresh token pair: %v", err)
	}
	if _, err := expiredRefresh.ParseRefreshToken(tokens.RefreshToken); err == nil {
		t.Fatal("expected expired refresh token to be rejected")
	}
}
