package auth

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/madalin/forgedesk/internal/crypto"
)

const (
	recoveryCodeCount  = 10
	recoveryCodeLength = 8
	recoveryCodeChars  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// GenerateRecoveryCodes creates 10 random recovery codes.
func GenerateRecoveryCodes() ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		code := make([]byte, recoveryCodeLength)
		for j := range code {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(recoveryCodeChars))))
			if err != nil {
				return nil, fmt.Errorf("generating random: %w", err)
			}
			code[j] = recoveryCodeChars[n.Int64()]
		}
		codes[i] = string(code)
	}
	return codes, nil
}

// HashRecoveryCodes hashes each recovery code with Argon2id.
func HashRecoveryCodes(codes []string) ([]string, error) {
	hashed := make([]string, len(codes))
	for i, code := range codes {
		h, err := HashPassword(code)
		if err != nil {
			return nil, fmt.Errorf("hashing recovery code: %w", err)
		}
		hashed[i] = h
	}
	return hashed, nil
}

// EncryptRecoveryCodes encrypts the hashed recovery codes for storage.
func EncryptRecoveryCodes(hashedCodes []string, aesKey string) ([]byte, error) {
	data, err := json.Marshal(hashedCodes)
	if err != nil {
		return nil, fmt.Errorf("marshaling codes: %w", err)
	}
	return crypto.Encrypt(data, aesKey)
}

// DecryptRecoveryCodes decrypts stored recovery codes.
func DecryptRecoveryCodes(ciphertext []byte, aesKey string) ([]string, error) {
	data, err := crypto.Decrypt(ciphertext, aesKey)
	if err != nil {
		return nil, err
	}
	var codes []string
	if err := json.Unmarshal(data, &codes); err != nil {
		return nil, fmt.Errorf("unmarshaling codes: %w", err)
	}
	return codes, nil
}

// VerifyRecoveryCode checks a recovery code against the stored hashes. Returns the index if found, -1 otherwise.
func VerifyRecoveryCode(code string, hashedCodes []string) int {
	for i, hashed := range hashedCodes {
		if hashed == "" {
			continue // already used
		}
		ok, _ := VerifyPassword(code, hashed)
		if ok {
			return i
		}
	}
	return -1
}
