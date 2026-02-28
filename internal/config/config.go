package config

import (
	"fmt"
	"os"
)

type Config struct {
	DatabaseURL   string
	SessionSecret string
	AESKey        string
	ListenAddr    string
	BaseURL       string

	// SMTP (optional — if SMTPHost is empty, email sending is disabled)
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string

	// WebAuthn
	RPDisplayName string
	RPID          string
	RPOrigins     []string

	// AI / Gemini (optional — if GeminiAPIKey is empty, assistant is hidden)
	GeminiAPIKey      string
	GeminiModel       string // default model (GEMINI_MODEL)
	GeminiModelChat   string // chat assistant model (GEMINI_MODEL_CHAT)
	GeminiModelPro    string // pro model (GEMINI_MODEL_PRO)
	GeminiModelImage  string // image model (GEMINI_MODEL_IMAGE)
	GeminiModelImagePro string // pro image model (GEMINI_MODEL_IMAGE_PRO)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func Load() (*Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	sessionSecret := os.Getenv("SESSION_SECRET")
	if sessionSecret == "" {
		return nil, fmt.Errorf("SESSION_SECRET is required")
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	baseURL := os.Getenv("APP_URL")
	if baseURL == "" {
		baseURL = "https://smart.madalin.me"
	}

	smtpPort := os.Getenv("SMTP_PORT")
	if smtpPort == "" {
		smtpPort = "587"
	}

	rpID := os.Getenv("WEBAUTHN_RPID")
	if rpID == "" {
		rpID = "smart.madalin.me"
	}

	return &Config{
		DatabaseURL:   dbURL,
		SessionSecret: sessionSecret,
		AESKey:        os.Getenv("AES_ENCRYPTION_KEY"),
		ListenAddr:    listenAddr,
		BaseURL:       baseURL,
		SMTPHost:      os.Getenv("SMTP_HOST"),
		SMTPPort:      smtpPort,
		SMTPUsername:   os.Getenv("SMTP_USERNAME"),
		SMTPPassword:   os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:      os.Getenv("SMTP_FROM"),
		RPDisplayName: "ForgeDesk",
		RPID:          rpID,
		RPOrigins:     []string{baseURL},
		GeminiAPIKey:      os.Getenv("GEMINI_API_KEY"),
		GeminiModel:       envOrDefault("GEMINI_MODEL", "gemini-2.5-flash"),
		GeminiModelChat:   envOrDefault("GEMINI_MODEL_CHAT", "gemini-2.5-flash"),
		GeminiModelPro:    envOrDefault("GEMINI_MODEL_PRO", "gemini-2.5-pro"),
		GeminiModelImage:  envOrDefault("GEMINI_MODEL_IMAGE", "gemini-2.5-flash"),
		GeminiModelImagePro: envOrDefault("GEMINI_MODEL_IMAGE_PRO", "gemini-2.5-pro"),
	}, nil
}
