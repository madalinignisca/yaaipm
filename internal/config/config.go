package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	SMTPSSL      bool

	// WebAuthn
	RPDisplayName string
	RPID          string
	RPOrigins     []string

	// Protected superadmins (comma-separated emails) — cannot be removed or demoted by anyone
	ProtectedSuperadmins []string

	// AI / Gemini (optional — if GeminiAPIKey is empty, assistant is hidden)
	GeminiAPIKey      string
	GeminiModel       string // default model (GEMINI_MODEL)
	GeminiModelChat   string // chat assistant model (GEMINI_MODEL_CHAT)
	GeminiModelPro    string // pro model (GEMINI_MODEL_PRO)
	GeminiModelImage  string // image model (GEMINI_MODEL_IMAGE)
	GeminiModelImagePro string // pro image model (GEMINI_MODEL_IMAGE_PRO)

	// Gemini pricing (cents per million tokens)
	GeminiGoogleSearchCents     int64 // GEMINI_MODEL_GOOGLE_SEARCH
	GeminiInputPrice            int64 // GEMINI_MODEL_INPUT_PRICE
	GeminiOutputPrice           int64 // GEMINI_MODEL_OUTPUT_PRICE
	GeminiProInputPrice         int64 // GEMINI_MODEL_PRO_INPUT_PRICE
	GeminiProOutputPrice        int64 // GEMINI_MODEL_PRO_OUTPUT_PRICE
	GeminiImageInputPrice       int64 // GEMINI_MODEL_IMAGE_INPUT_PRICE
	GeminiImageTextOutPrice     int64 // GEMINI_MODEL_IMAGE_TEXT_OUTPUT_PRICE
	GeminiImageImageOutPrice    int64 // GEMINI_MODEL_IMAGE_IMAGE_OUTPUT_PRICE
	GeminiImageProInputPrice    int64 // GEMINI_MODEL_IMAGE_PRO_INPUT_PRICE
	GeminiImageProTextOutPrice  int64 // GEMINI_MODEL_IMAGE_PRO_TEXT_OUTPUT_PRICE
	GeminiImageProImageOutPrice int64 // GEMINI_MODEL_IMAGE_PRO_IMAGE_OUTPUT_PRICE

	// S3 storage (optional — if S3Endpoint is empty, file uploads are disabled)
	S3Endpoint       string // S3_ENDPOINT
	S3AccessKeyID    string // S3_ACCESS_KEY_ID
	S3SecretAccessKey string // S3_SECRET_ACCESS_KEY
	S3Region         string // S3_REGION
	S3Bucket         string // S3_PUBLIC_BUCKET
	S3ForcePathStyle bool   // S3_FORCE_PATH_STYLE
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
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

	smtpSSL := os.Getenv("SMTP_SSL")
	isSSL := smtpSSL == "true" || smtpSSL == "1"

	smtpFrom := os.Getenv("SMTP_FROM")
	smtpUsername := os.Getenv("SMTP_USERNAME")
	if smtpUsername == "" {
		smtpUsername = smtpFrom
	}

	var protectedSuperadmins []string
	if raw := os.Getenv("PROTECTED_SUPERADMINS"); raw != "" {
		for _, email := range strings.Split(raw, ",") {
			if e := strings.TrimSpace(email); e != "" {
				protectedSuperadmins = append(protectedSuperadmins, strings.ToLower(e))
			}
		}
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
		SMTPUsername:  smtpUsername,
		SMTPPassword:  os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:      smtpFrom,
		SMTPSSL:              isSSL,
		ProtectedSuperadmins: protectedSuperadmins,
		RPDisplayName:        "ForgeDesk",
		RPID:          rpID,
		RPOrigins:     []string{baseURL},
		GeminiAPIKey:      os.Getenv("GEMINI_API_KEY"),
		GeminiModel:       envOrDefault("GEMINI_MODEL", "gemini-2.5-flash"),
		GeminiModelChat:   envOrDefault("GEMINI_MODEL_CHAT", "gemini-2.5-flash"),
		GeminiModelPro:    envOrDefault("GEMINI_MODEL_PRO", "gemini-2.5-pro"),
		GeminiModelImage:  envOrDefault("GEMINI_MODEL_IMAGE", "gemini-2.5-flash"),
		GeminiModelImagePro: envOrDefault("GEMINI_MODEL_IMAGE_PRO", "gemini-2.5-pro"),

		GeminiGoogleSearchCents:     envInt64("GEMINI_MODEL_GOOGLE_SEARCH", 1400),
		GeminiInputPrice:            envInt64("GEMINI_MODEL_INPUT_PRICE", 50),
		GeminiOutputPrice:           envInt64("GEMINI_MODEL_OUTPUT_PRICE", 300),
		GeminiProInputPrice:         envInt64("GEMINI_MODEL_PRO_INPUT_PRICE", 200),
		GeminiProOutputPrice:        envInt64("GEMINI_MODEL_PRO_OUTPUT_PRICE", 1200),
		GeminiImageInputPrice:       envInt64("GEMINI_MODEL_IMAGE_INPUT_PRICE", 25),
		GeminiImageTextOutPrice:     envInt64("GEMINI_MODEL_IMAGE_TEXT_OUTPUT_PRICE", 150),
		GeminiImageImageOutPrice:    envInt64("GEMINI_MODEL_IMAGE_IMAGE_OUTPUT_PRICE", 6000),
		GeminiImageProInputPrice:    envInt64("GEMINI_MODEL_IMAGE_PRO_INPUT_PRICE", 200),
		GeminiImageProTextOutPrice:  envInt64("GEMINI_MODEL_IMAGE_PRO_TEXT_OUTPUT_PRICE", 1200),
		GeminiImageProImageOutPrice: envInt64("GEMINI_MODEL_IMAGE_PRO_IMAGE_OUTPUT_PRICE", 12000),

		S3Endpoint:       os.Getenv("S3_ENDPOINT"),
		S3AccessKeyID:    os.Getenv("S3_ACCESS_KEY_ID"),
		S3SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		S3Region:         envOrDefault("S3_REGION", "us-east-1"),
		S3Bucket:         os.Getenv("S3_PUBLIC_BUCKET"),
		S3ForcePathStyle: os.Getenv("S3_FORCE_PATH_STYLE") == "true",
	}, nil
}

// CalculateAICost returns cost in cents for a given model and token counts.
// hasImageOutput should be true when the response contains generated images.
func (c *Config) CalculateAICost(model string, inputTokens, outputTokens int32, hasImageOutput bool) int64 {
	var inPrice, outPrice int64
	switch model {
	case c.GeminiModelPro:
		inPrice = c.GeminiProInputPrice
		outPrice = c.GeminiProOutputPrice
	case c.GeminiModelImagePro:
		inPrice = c.GeminiImageProInputPrice
		if hasImageOutput {
			outPrice = c.GeminiImageProImageOutPrice
		} else {
			outPrice = c.GeminiImageProTextOutPrice
		}
	case c.GeminiModelImage:
		inPrice = c.GeminiImageInputPrice
		if hasImageOutput {
			outPrice = c.GeminiImageImageOutPrice
		} else {
			outPrice = c.GeminiImageTextOutPrice
		}
	default: // GeminiModel, GeminiModelChat, and any unknown
		inPrice = c.GeminiInputPrice
		outPrice = c.GeminiOutputPrice
	}
	return int64(inputTokens)*inPrice/1_000_000 + int64(outputTokens)*outPrice/1_000_000
}
