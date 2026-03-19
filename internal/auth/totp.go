package auth

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/madalin/forgedesk/internal/crypto"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	qrcode "github.com/skip2/go-qrcode"
)

// GenerateTOTP creates a new TOTP key for a user, returning the secret and QR code PNG as base64.
func GenerateTOTP(email string) (secret string, qrBase64 string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "ForgeDesk",
		AccountName: email,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return "", "", fmt.Errorf("generating TOTP key: %w", err)
	}

	png, err := qrcode.Encode(key.URL(), qrcode.Medium, 256)
	if err != nil {
		return "", "", fmt.Errorf("generating QR code: %w", err)
	}

	return key.Secret(), base64.StdEncoding.EncodeToString(png), nil
}

// ValidateTOTP checks a TOTP code against a secret, allowing ±1 time step.
func ValidateTOTP(code, secret string) bool {
	valid, _ := totp.ValidateCustom(code, secret, time.Now(), totp.ValidateOpts{
		Period:    30,
		Skew:     1,
		Digits:   otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return valid
}

// ValidateTOTPOnce validates a TOTP code and prevents replay within the same time step.
// Returns true if the code is valid AND hasn't been used in the last 30 seconds.
func ValidateTOTPOnce(code, secret string, lastUsedAt *time.Time) bool {
	if !ValidateTOTP(code, secret) {
		return false
	}
	// Reject if the same code was used within the last 30s (one TOTP period)
	if lastUsedAt != nil && time.Since(*lastUsedAt) < 30*time.Second {
		return false
	}
	return true
}

// EncryptTOTPSecret encrypts a TOTP secret for storage.
func EncryptTOTPSecret(secret, aesKey string) ([]byte, error) {
	return crypto.Encrypt([]byte(secret), aesKey)
}

// DecryptTOTPSecret decrypts a stored TOTP secret.
func DecryptTOTPSecret(ciphertext []byte, aesKey string) (string, error) {
	plaintext, err := crypto.Decrypt(ciphertext, aesKey)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
