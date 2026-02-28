package auth

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

const testAESKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestGenerateTOTP(t *testing.T) {
	secret, qrBase64, err := GenerateTOTP("test@example.com")
	if err != nil {
		t.Fatalf("GenerateTOTP: %v", err)
	}

	if secret == "" {
		t.Fatal("secret should not be empty")
	}

	if qrBase64 == "" {
		t.Fatal("QR code should not be empty")
	}

	// Secret should be base32-encoded, typical length 32 chars
	if len(secret) < 16 {
		t.Fatalf("secret too short: %d", len(secret))
	}
}

func TestValidateTOTPCorrectCode(t *testing.T) {
	secret, _, err := GenerateTOTP("test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Generate a valid code for now
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generating test code: %v", err)
	}

	if !ValidateTOTP(code, secret) {
		t.Fatal("valid TOTP code should pass validation")
	}
}

func TestValidateTOTPWrongCode(t *testing.T) {
	secret, _, err := GenerateTOTP("test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	if ValidateTOTP("000000", secret) {
		// This could technically pass if 000000 happens to be the current code,
		// but it's extremely unlikely
		t.Log("warning: 000000 validated - extremely unlikely coincidence")
	}

	if ValidateTOTP("", secret) {
		t.Fatal("empty code should not validate")
	}

	if ValidateTOTP("not-a-code", secret) {
		t.Fatal("non-numeric code should not validate")
	}
}

func TestEncryptDecryptTOTPSecret(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"

	encrypted, err := EncryptTOTPSecret(secret, testAESKey)
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}

	if string(encrypted) == secret {
		t.Fatal("encrypted should differ from plaintext")
	}

	decrypted, err := DecryptTOTPSecret(encrypted, testAESKey)
	if err != nil {
		t.Fatalf("DecryptTOTPSecret: %v", err)
	}

	if decrypted != secret {
		t.Fatalf("got %q, want %q", decrypted, secret)
	}
}

func TestEncryptTOTPSecretWrongKey(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"
	encrypted, err := EncryptTOTPSecret(secret, testAESKey)
	if err != nil {
		t.Fatal(err)
	}

	wrongKey := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	_, err = DecryptTOTPSecret(encrypted, wrongKey)
	if err == nil {
		t.Fatal("should fail with wrong key")
	}
}
