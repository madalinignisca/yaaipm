package crypto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plaintext := []byte("hello world, this is a secret!")

	ciphertext, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext should differ from plaintext")
	}

	result, err := Decrypt(ciphertext, testKey)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(result, plaintext) {
		t.Fatalf("got %q, want %q", result, plaintext)
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	plaintext := []byte("same input")

	ct1, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatal(err)
	}
	ct2, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encryptions of same plaintext should produce different ciphertexts (random nonce)")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	plaintext := []byte("secret data")
	ciphertext, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatal(err)
	}

	wrongKey := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	_, err = Decrypt(ciphertext, wrongKey)
	if err == nil {
		t.Fatal("expected error decrypting with wrong key")
	}
}

func TestEncryptInvalidKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"not hex", "zzzz"},
		{"too short", hex.EncodeToString([]byte("short"))},
		{"empty", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Encrypt([]byte("data"), tc.key)
			if err == nil {
				t.Fatal("expected error for invalid key")
			}
		})
	}
}

func TestDecryptTruncatedCiphertext(t *testing.T) {
	_, err := Decrypt([]byte("short"), testKey)
	if err == nil {
		t.Fatal("expected error for truncated ciphertext")
	}
}

func TestDecryptEmpty(t *testing.T) {
	_, err := Decrypt(nil, testKey)
	if err == nil {
		t.Fatal("expected error for nil ciphertext")
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	ct, err := Encrypt([]byte{}, testKey)
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}

	pt, err := Decrypt(ct, testKey)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if len(pt) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(pt))
	}
}
