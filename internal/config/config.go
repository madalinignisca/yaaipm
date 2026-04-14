package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AnthropicModel        string
	GeminiAPIKey          string
	AESKey                string
	ListenAddr            string
	BaseURL               string
	SMTPHost              string
	SMTPPort              string
	SMTPUsername          string
	SMTPPassword          string
	SMTPFrom              string
	S3Bucket              string
	RPDisplayName         string
	RPID                  string
	S3Region              string
	S3SecretAccessKey     string
	S3AccessKeyID         string
	GeminiModel           string
	GeminiModelChat       string
	GeminiModelPro        string
	GeminiModelImage      string
	GeminiModelImagePro   string
	S3Endpoint            string
	SessionSecret         string
	WorkspacesDir         string
	AnthropicModelContent string
	DatabaseURL           string
	AnthropicAPIKey       string
	// OpenAI (used by Feature Debate Mode's ChatGPT refiner).
	OpenAIAPIKey                string
	OpenAIModel                 string
	ProtectedSuperadmins        []string
	RPOrigins                   []string
	GeminiImageTextOutPrice     int64
	AnthropicOutputPrice        int64
	GeminiImageProImageOutPrice int64
	GeminiImageInputPrice       int64
	GeminiProOutputPrice        int64
	AnthropicInputPrice         int64
	AnthropicContentOutputPrice int64
	GeminiProInputPrice         int64
	AnthropicContentInputPrice  int64
	GeminiImageProTextOutPrice  int64
	GeminiInputPrice            int64
	GeminiGoogleSearchCents     int64
	GeminiOutputPrice           int64
	GeminiImageProInputPrice    int64
	GeminiImageImageOutPrice    int64
	SMTPSSL                     bool
	S3ForcePathStyle            bool
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
	if len(sessionSecret) < 32 {
		return nil, fmt.Errorf("SESSION_SECRET must be at least 32 bytes, got %d", len(sessionSecret))
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
		for email := range strings.SplitSeq(raw, ",") {
			if e := strings.TrimSpace(email); e != "" {
				protectedSuperadmins = append(protectedSuperadmins, strings.ToLower(e))
			}
		}
	}

	rpID := os.Getenv("WEBAUTHN_RPID")
	if rpID == "" {
		rpID = "smart.madalin.me"
	}

	workspacesDir := os.Getenv("WORKSPACES_DIR")
	if workspacesDir == "" {
		home, _ := os.UserHomeDir()
		workspacesDir = home + "/forgedesk-workspaces"
	}

	aesKey := os.Getenv("AES_ENCRYPTION_KEY")
	if aesKey == "" {
		return nil, fmt.Errorf("AES_ENCRYPTION_KEY is required (hex-encoded 32-byte key)")
	}
	if len(aesKey) != 64 {
		log.Printf("WARNING: AES_ENCRYPTION_KEY length is %d, expected 64 (hex-encoded 32-byte key)", len(aesKey))
	}

	return &Config{
		DatabaseURL:          dbURL,
		SessionSecret:        sessionSecret,
		AESKey:               aesKey,
		ListenAddr:           listenAddr,
		BaseURL:              baseURL,
		SMTPHost:             os.Getenv("SMTP_HOST"),
		SMTPPort:             smtpPort,
		SMTPUsername:         smtpUsername,
		SMTPPassword:         os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:             smtpFrom,
		SMTPSSL:              isSSL,
		ProtectedSuperadmins: protectedSuperadmins,
		RPDisplayName:        "ForgeDesk",
		RPID:                 rpID,
		RPOrigins:            []string{baseURL},
		GeminiAPIKey:         os.Getenv("GEMINI_API_KEY"),
		GeminiModel:          envOrDefault("GEMINI_MODEL", "gemini-2.5-flash"),
		GeminiModelChat:      envOrDefault("GEMINI_MODEL_CHAT", "gemini-2.5-flash"),
		GeminiModelPro:       envOrDefault("GEMINI_MODEL_PRO", "gemini-2.5-pro"),
		GeminiModelImage:     envOrDefault("GEMINI_MODEL_IMAGE", "gemini-2.5-flash"),
		GeminiModelImagePro:  envOrDefault("GEMINI_MODEL_IMAGE_PRO", "gemini-2.5-pro"),

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

		AnthropicAPIKey:             os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:              envOrDefault("ANTHROPIC_MODEL", "claude-sonnet-4-6"),
		AnthropicModelContent:       envOrDefault("ANTHROPIC_MODEL_CONTENT", "claude-opus-4-6"),
		OpenAIAPIKey:                os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:                 envOrDefault("OPENAI_MODEL", "gpt-5-mini"),
		AnthropicInputPrice:         envInt64("ANTHROPIC_INPUT_PRICE", 300),
		AnthropicOutputPrice:        envInt64("ANTHROPIC_OUTPUT_PRICE", 1500),
		AnthropicContentInputPrice:  envInt64("ANTHROPIC_CONTENT_INPUT_PRICE", 1500),
		AnthropicContentOutputPrice: envInt64("ANTHROPIC_CONTENT_OUTPUT_PRICE", 7500),

		S3Endpoint:        os.Getenv("S3_ENDPOINT"),
		S3AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
		S3SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		S3Region:          envOrDefault("S3_REGION", "us-east-1"),
		S3Bucket:          os.Getenv("S3_PUBLIC_BUCKET"),
		S3ForcePathStyle:  os.Getenv("S3_FORCE_PATH_STYLE") == "true",

		WorkspacesDir: workspacesDir,
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
	case c.AnthropicModelContent:
		inPrice = c.AnthropicContentInputPrice
		outPrice = c.AnthropicContentOutputPrice
	case c.AnthropicModel:
		inPrice = c.AnthropicInputPrice
		outPrice = c.AnthropicOutputPrice
	default: // GeminiModel, GeminiModelChat, and any unknown
		inPrice = c.GeminiInputPrice
		outPrice = c.GeminiOutputPrice
	}
	return int64(inputTokens)*inPrice/1_000_000 + int64(outputTokens)*outPrice/1_000_000
}
