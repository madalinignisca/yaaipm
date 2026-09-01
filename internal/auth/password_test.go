package auth

import (
	"context"
	"strings"
	"testing"
)

func TestHashPasswordFormat(t *testing.T) {
	hash, err := HashPassword(context.Background(), "my-secure-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash should start with $argon2id$, got: %s", hash[:20])
	}

	parts := strings.Split(hash, "$")
	if len(parts) != 6 {
		t.Fatalf("expected 6 parts in hash, got %d", len(parts))
	}
}

func TestHashPasswordUnique(t *testing.T) {
	h1, _ := HashPassword(context.Background(), "same-password")
	h2, _ := HashPassword(context.Background(), "same-password")

	if h1 == h2 {
		t.Fatal("hashing same password twice should produce different hashes (random salt)")
	}
}

func TestVerifyPasswordCorrect(t *testing.T) {
	password := "correct-horse-battery-staple"
	hash, err := HashPassword(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}

	ok, err := VerifyPassword(context.Background(), password, hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("correct password should verify")
	}
}

func TestVerifyPasswordIncorrect(t *testing.T) {
	hash, _ := HashPassword(context.Background(), "correct-password")

	ok, err := VerifyPassword(context.Background(), "wrong-password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("wrong password should not verify")
	}
}

func TestVerifyPasswordInvalidFormat(t *testing.T) {
	_, err := VerifyPassword(context.Background(), "anything", "not-a-valid-hash")
	if err == nil {
		t.Fatal("expected error for invalid hash format")
	}
}

func TestVerifyPasswordEmptyInput(t *testing.T) {
	hash, _ := HashPassword(context.Background(), "something")
	ok, err := VerifyPassword(context.Background(), "", hash)
	if err != nil {
		t.Fatalf("VerifyPassword empty: %v", err)
	}
	if ok {
		t.Fatal("empty password should not match")
	}
}
