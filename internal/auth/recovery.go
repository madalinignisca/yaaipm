package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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
func HashRecoveryCodes(ctx context.Context, codes []string) ([]string, error) {
	hashed := make([]string, len(codes))
	for i, code := range codes {
		h, err := HashPassword(ctx, code)
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
//
// This is the heaviest Argon2id path in the codebase: it verifies against every
// unused stored hash, so a single call can run up to ten 64 MB computations
// (#142). They are sequential, and each one passes through the hashing gate.
// It returns (-1, ErrHashingBusy) when the gate is saturated rather than
// reporting "no match". Reporting no-match would tell a user their genuine
// recovery code is wrong — on the very path they reach after losing their
// authenticator, and the codes are single-use, so they might burn several
// believing they had failed. A busy server must say it is busy.
//
// Other errors (a malformed stored hash) stay non-fatal: one corrupt entry
// must not stop the remaining codes from being checked.
func VerifyRecoveryCode(ctx context.Context, code string, hashedCodes []string) (int, error) {
	for i, hashed := range hashedCodes {
		if hashed == "" {
			continue // already used
		}
		ok, err := VerifyPassword(ctx, code, hashed)
		if errors.Is(err, ErrHashingBusy) {
			return -1, err
		}
		if ok {
			return i, nil
		}
	}
	return -1, nil
}
