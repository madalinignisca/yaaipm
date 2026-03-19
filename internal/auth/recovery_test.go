package auth

import (
	"testing"
)

func TestGenerateRecoveryCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}

	if len(codes) != 10 {
		t.Fatalf("expected 10 codes, got %d", len(codes))
	}

	// Each code should be 8 chars, uppercase alphanumeric
	for i, code := range codes {
		if len(code) != 8 {
			t.Errorf("code[%d] length = %d, want 8", i, len(code))
		}
		for _, c := range code {
			if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				t.Errorf("code[%d] contains invalid char %c", i, c)
			}
		}
	}

	// Codes should be unique
	seen := make(map[string]bool)
	for _, code := range codes {
		if seen[code] {
			t.Errorf("duplicate code: %s", code)
		}
		seen[code] = true
	}
}

func TestGenerateRecoveryCodesRandomness(t *testing.T) {
	codes1, _ := GenerateRecoveryCodes()
	codes2, _ := GenerateRecoveryCodes()

	allSame := true
	for i := range codes1 {
		if codes1[i] != codes2[i] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Fatal("two generations should not produce identical code sets")
	}
}

func TestHashAndVerifyRecoveryCodes(t *testing.T) {
	codes, _ := GenerateRecoveryCodes()

	hashed, err := HashRecoveryCodes(codes)
	if err != nil {
		t.Fatalf("HashRecoveryCodes: %v", err)
	}

	if len(hashed) != len(codes) {
		t.Fatalf("hashed count = %d, want %d", len(hashed), len(codes))
	}

	// Verify each code against its hash
	for i, code := range codes {
		ok, err := VerifyPassword(code, hashed[i])
		if err != nil {
			t.Fatalf("VerifyPassword code[%d]: %v", i, err)
		}
		if !ok {
			t.Errorf("code[%d] should verify against its hash", i)
		}
	}
}

func TestVerifyRecoveryCodeFound(t *testing.T) {
	codes, _ := GenerateRecoveryCodes()
	hashed, _ := HashRecoveryCodes(codes)

	// Verify the 5th code
	idx := VerifyRecoveryCode(codes[4], hashed)
	if idx != 4 {
		t.Fatalf("expected index 4, got %d", idx)
	}
}

func TestVerifyRecoveryCodeNotFound(t *testing.T) {
	codes, _ := GenerateRecoveryCodes()
	hashed, _ := HashRecoveryCodes(codes)

	idx := VerifyRecoveryCode("INVALIDX", hashed)
	if idx != -1 {
		t.Fatalf("expected -1 for invalid code, got %d", idx)
	}
}

func TestVerifyRecoveryCodeConsumed(t *testing.T) {
	codes, _ := GenerateRecoveryCodes()
	hashed, _ := HashRecoveryCodes(codes)

	// "Consume" a code by clearing its hash
	hashed[3] = ""

	idx := VerifyRecoveryCode(codes[3], hashed)
	if idx != -1 {
		t.Fatalf("consumed code should return -1, got %d", idx)
	}
}

func TestEncryptDecryptRecoveryCodes(t *testing.T) {
	codes, _ := GenerateRecoveryCodes()
	hashed, _ := HashRecoveryCodes(codes)

	encrypted, err := EncryptRecoveryCodes(hashed, testAESKey)
	if err != nil {
		t.Fatalf("EncryptRecoveryCodes: %v", err)
	}

	decrypted, err := DecryptRecoveryCodes(encrypted, testAESKey)
	if err != nil {
		t.Fatalf("DecryptRecoveryCodes: %v", err)
	}

	if len(decrypted) != len(hashed) {
		t.Fatalf("decrypted count = %d, want %d", len(decrypted), len(hashed))
	}

	for i := range hashed {
		if decrypted[i] != hashed[i] {
			t.Errorf("decrypted[%d] mismatch", i)
		}
	}
}
