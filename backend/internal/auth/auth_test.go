package auth

import (
	"testing"
	"time"
)

func TestHashAndCheckPassword(t *testing.T) {
	pw := "correct horse battery staple"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" || hash == pw {
		t.Fatalf("hash empty or equals plaintext")
	}
	if !CheckPassword(hash, pw) {
		t.Errorf("CheckPassword correct = false, want true")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Errorf("CheckPassword wrong = true, want false")
	}
}

func TestSignParseRoundTrip(t *testing.T) {
	secret := "s"
	claims := NewClaims("user-1", "alice", true, time.Hour)
	token, err := Sign(secret, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	got, err := Parse(secret, token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.UserID != claims.UserID {
		t.Errorf("UserID = %q, want %q", got.UserID, claims.UserID)
	}
	if got.Username != claims.Username {
		t.Errorf("Username = %q, want %q", got.Username, claims.Username)
	}
	if got.IsAdmin != claims.IsAdmin {
		t.Errorf("IsAdmin = %v, want %v", got.IsAdmin, claims.IsAdmin)
	}
	if got.Subject != claims.Username {
		t.Errorf("Subject = %q, want %q", got.Subject, claims.Username)
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt nil")
	}
}

// TestSignParse_IsAdminNonAdmin verifies the adm claim round-trips for a
// non-admin user too (false), so the middleware's is_admin context value is
// authoritative for both populations.
func TestSignParse_IsAdminNonAdmin(t *testing.T) {
	claims := NewClaims("user-2", "bob", false, time.Hour)
	token, err := Sign("s", claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := Parse("s", token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.IsAdmin {
		t.Fatalf("IsAdmin = true, want false for non-admin token")
	}
}

func TestParseWrongSecret(t *testing.T) {
	claims := NewClaims("user-1", "alice", true, time.Hour)
	token, err := Sign("correct-secret", claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := Parse("wrong-secret", token); err == nil {
		t.Fatal("Parse with wrong secret: want error, got nil")
	}
}

func TestParseExpired(t *testing.T) {
	claims := NewClaims("user-1", "alice", true, -time.Hour) // already expired
	token, err := Sign("s", claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := Parse("s", token); err == nil {
		t.Fatal("Parse expired token: want error, got nil")
	}
}