package config

import (
	"testing"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/testdb")
	t.Setenv("SESSION_SECRET", "test-secret-key")
}

func TestEnvOrDefault(t *testing.T) {
	t.Run("returns fallback when env not set", func(t *testing.T) {
		// Ensure the key is unset (t.Setenv restores original on cleanup)
		t.Setenv("TEST_ENV_OR_DEFAULT_KEY", "")
		got := envOrDefault("TEST_ENV_OR_DEFAULT_KEY", "fallback-value")
		if got != "fallback-value" {
			t.Errorf("envOrDefault() = %q, want %q", got, "fallback-value")
		}
	})

	t.Run("returns env value when set", func(t *testing.T) {
		t.Setenv("TEST_ENV_OR_DEFAULT_KEY", "custom-value")
		got := envOrDefault("TEST_ENV_OR_DEFAULT_KEY", "fallback-value")
		if got != "custom-value" {
			t.Errorf("envOrDefault() = %q, want %q", got, "custom-value")
		}
	})
}

func TestLoad_MissingRequired(t *testing.T) {
	t.Run("fails without DATABASE_URL", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		t.Setenv("SESSION_SECRET", "some-secret")

		cfg, err := Load()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if cfg != nil {
			t.Fatal("expected nil config on error")
		}
		if err.Error() != "DATABASE_URL is required" {
			t.Errorf("unexpected error message: %s", err)
		}
	})

	t.Run("fails without SESSION_SECRET", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/db")
		t.Setenv("SESSION_SECRET", "")

		cfg, err := Load()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if cfg != nil {
			t.Fatal("expected nil config on error")
		}
		if err.Error() != "SESSION_SECRET is required" {
			t.Errorf("unexpected error message: %s", err)
		}
	})
}

func TestLoad_Defaults(t *testing.T) {
	setRequired(t)
	// Clear optional vars to ensure defaults apply
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("APP_URL", "")
	t.Setenv("SMTP_PORT", "")
	t.Setenv("WEBAUTHN_RPID", "")
	t.Setenv("GEMINI_MODEL", "")
	t.Setenv("GEMINI_MODEL_CHAT", "")
	t.Setenv("GEMINI_MODEL_PRO", "")
	t.Setenv("GEMINI_MODEL_IMAGE", "")
	t.Setenv("GEMINI_MODEL_IMAGE_PRO", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"ListenAddr", cfg.ListenAddr, ":8080"},
		{"BaseURL", cfg.BaseURL, "https://smart.madalin.me"},
		{"SMTPPort", cfg.SMTPPort, "587"},
		{"RPID", cfg.RPID, "smart.madalin.me"},
		{"RPDisplayName", cfg.RPDisplayName, "ForgeDesk"},
		{"GeminiModel", cfg.GeminiModel, "gemini-2.5-flash"},
		{"GeminiModelChat", cfg.GeminiModelChat, "gemini-2.5-flash"},
		{"GeminiModelPro", cfg.GeminiModelPro, "gemini-2.5-pro"},
		{"GeminiModelImage", cfg.GeminiModelImage, "gemini-2.5-flash"},
		{"GeminiModelImagePro", cfg.GeminiModelImagePro, "gemini-2.5-pro"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}

	// RPOrigins should contain the base URL
	if len(cfg.RPOrigins) != 1 || cfg.RPOrigins[0] != "https://smart.madalin.me" {
		t.Errorf("RPOrigins = %v, want [https://smart.madalin.me]", cfg.RPOrigins)
	}
}

func TestLoad_CustomOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("LISTEN_ADDR", ":9090")
	t.Setenv("APP_URL", "https://custom.example.com")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("WEBAUTHN_RPID", "custom.example.com")
	t.Setenv("AES_ENCRYPTION_KEY", "my-aes-key")
	t.Setenv("GEMINI_API_KEY", "gemini-key-123")
	t.Setenv("GEMINI_MODEL", "gemini-custom")
	t.Setenv("GEMINI_MODEL_CHAT", "gemini-chat-custom")
	t.Setenv("GEMINI_MODEL_PRO", "gemini-pro-custom")
	t.Setenv("GEMINI_MODEL_IMAGE", "gemini-image-custom")
	t.Setenv("GEMINI_MODEL_IMAGE_PRO", "gemini-image-pro-custom")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"ListenAddr", cfg.ListenAddr, ":9090"},
		{"BaseURL", cfg.BaseURL, "https://custom.example.com"},
		{"SMTPPort", cfg.SMTPPort, "465"},
		{"RPID", cfg.RPID, "custom.example.com"},
		{"AESKey", cfg.AESKey, "my-aes-key"},
		{"GeminiAPIKey", cfg.GeminiAPIKey, "gemini-key-123"},
		{"GeminiModel", cfg.GeminiModel, "gemini-custom"},
		{"GeminiModelChat", cfg.GeminiModelChat, "gemini-chat-custom"},
		{"GeminiModelPro", cfg.GeminiModelPro, "gemini-pro-custom"},
		{"GeminiModelImage", cfg.GeminiModelImage, "gemini-image-custom"},
		{"GeminiModelImagePro", cfg.GeminiModelImagePro, "gemini-image-pro-custom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}

	// RPOrigins should reflect the custom APP_URL
	if len(cfg.RPOrigins) != 1 || cfg.RPOrigins[0] != "https://custom.example.com" {
		t.Errorf("RPOrigins = %v, want [https://custom.example.com]", cfg.RPOrigins)
	}
}

func TestLoad_SMTPSSL(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		wantSSL bool
	}{
		{"true string", "true", true},
		{"1 string", "1", true},
		{"empty string", "", false},
		{"false string", "false", false},
		{"0 string", "0", false},
		{"random string", "yes", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("SMTP_SSL", tc.envVal)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.SMTPSSL != tc.wantSSL {
				t.Errorf("SMTPSSL = %v, want %v (SMTP_SSL=%q)", cfg.SMTPSSL, tc.wantSSL, tc.envVal)
			}
		})
	}
}

func TestLoad_SMTPUsernameDefaultsToFrom(t *testing.T) {
	t.Run("defaults to SMTP_FROM when SMTP_USERNAME not set", func(t *testing.T) {
		setRequired(t)
		t.Setenv("SMTP_FROM", "noreply@example.com")
		t.Setenv("SMTP_USERNAME", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.SMTPUsername != "noreply@example.com" {
			t.Errorf("SMTPUsername = %q, want %q", cfg.SMTPUsername, "noreply@example.com")
		}
	})

	t.Run("uses SMTP_USERNAME when set", func(t *testing.T) {
		setRequired(t)
		t.Setenv("SMTP_FROM", "noreply@example.com")
		t.Setenv("SMTP_USERNAME", "custom-user")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.SMTPUsername != "custom-user" {
			t.Errorf("SMTPUsername = %q, want %q", cfg.SMTPUsername, "custom-user")
		}
	})
}

func TestLoad_ProtectedSuperadmins(t *testing.T) {
	t.Run("empty string results in nil slice", func(t *testing.T) {
		setRequired(t)
		t.Setenv("PROTECTED_SUPERADMINS", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.ProtectedSuperadmins != nil {
			t.Errorf("ProtectedSuperadmins = %v, want nil", cfg.ProtectedSuperadmins)
		}
	})

	t.Run("single email", func(t *testing.T) {
		setRequired(t)
		t.Setenv("PROTECTED_SUPERADMINS", "Admin@Example.COM")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if len(cfg.ProtectedSuperadmins) != 1 || cfg.ProtectedSuperadmins[0] != "admin@example.com" {
			t.Errorf("ProtectedSuperadmins = %v, want [admin@example.com]", cfg.ProtectedSuperadmins)
		}
	})

	t.Run("comma-separated with spaces and mixed case", func(t *testing.T) {
		setRequired(t)
		t.Setenv("PROTECTED_SUPERADMINS", " Alice@Example.com , BOB@test.io , charlie@foo.bar ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		want := []string{"alice@example.com", "bob@test.io", "charlie@foo.bar"}
		if len(cfg.ProtectedSuperadmins) != len(want) {
			t.Fatalf("ProtectedSuperadmins len = %d, want %d", len(cfg.ProtectedSuperadmins), len(want))
		}
		for i, got := range cfg.ProtectedSuperadmins {
			if got != want[i] {
				t.Errorf("ProtectedSuperadmins[%d] = %q, want %q", i, got, want[i])
			}
		}
	})

	t.Run("trailing comma produces no empty entries", func(t *testing.T) {
		setRequired(t)
		t.Setenv("PROTECTED_SUPERADMINS", "admin@test.com,,")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if len(cfg.ProtectedSuperadmins) != 1 || cfg.ProtectedSuperadmins[0] != "admin@test.com" {
			t.Errorf("ProtectedSuperadmins = %v, want [admin@test.com]", cfg.ProtectedSuperadmins)
		}
	})
}

func TestLoad_DirectEnvPassthrough(t *testing.T) {
	setRequired(t)
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PASSWORD", "smtp-pass")
	t.Setenv("SMTP_FROM", "hello@example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.DatabaseURL != "postgres://localhost/testdb" {
		t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, "postgres://localhost/testdb")
	}
	if cfg.SessionSecret != "test-secret-key" {
		t.Errorf("SessionSecret = %q, want %q", cfg.SessionSecret, "test-secret-key")
	}
	if cfg.SMTPHost != "smtp.example.com" {
		t.Errorf("SMTPHost = %q, want %q", cfg.SMTPHost, "smtp.example.com")
	}
	if cfg.SMTPPassword != "smtp-pass" {
		t.Errorf("SMTPPassword = %q, want %q", cfg.SMTPPassword, "smtp-pass")
	}
	if cfg.SMTPFrom != "hello@example.com" {
		t.Errorf("SMTPFrom = %q, want %q", cfg.SMTPFrom, "hello@example.com")
	}
}
