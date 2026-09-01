package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory     = 64 * 1024 // 64 MB
	argonIterations = 3
	argonParallel   = 4
	argonKeyLength  = 32
	argonSaltLength = 16
)

// HashPassword hashes a password using Argon2id.
//
// Takes a context because the work is gated: each call holds 64 MB, so
// concurrency is capped to keep the process inside its memory limit (#142).
// Returns ErrHashingBusy if the gate is saturated and ctx expires first.
func HashPassword(ctx context.Context, password string) (string, error) {
	release, err := acquireHashSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallel, argonKeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonIterations, argonParallel,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword checks a password against an Argon2id hash.
//
// Gated like HashPassword: verification runs the same 64 MB Argon2id
// computation, and the login path reaches it on every attempt (#142).
func VerifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	release, err := acquireHashSlot(ctx)
	if err != nil {
		return false, err
	}
	defer release()

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("invalid hash format")
	}

	var memory uint32
	var iterations uint32
	var parallel uint8
	_, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallel)
	if err != nil {
		return false, fmt.Errorf("parsing params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decoding salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decoding hash: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallel, uint32(len(expectedHash)))

	return subtle.ConstantTimeCompare(hash, expectedHash) == 1, nil
}
