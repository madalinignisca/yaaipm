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
	OpenAIAPIKey string
	OpenAIModel  string
	// Per-provider debate scorer models (issue #63). The scorer runs on
	// every accepted round plus the background retry sweep, so each
	// provider defaults to its cheapest priced model rather than reusing
	// the refiner's — except Gemini, whose refiner model is already
	// flash-class, so the scorer follows GEMINI_MODEL. Claude defaulted to
	// Sonnet only because no cheap Claude tier was priced yet; it is Haiku
	// now (issue #119), which is 1/3 the input and 1/5 the output price.
	ScorerModelGemini           string
	ScorerModelOpenAI           string
	ScorerModelClaude           string
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

	// Auth rate limiting. Defaults match what was hardcoded before this was
	// configurable (0.5 req/s, burst 5) so production behavior is unchanged
	// unless someone opts in. It is configurable because the E2E suite drives
	// register -> login -> 2FA-setup back to back per user, which exhausts a
	// burst of 5 and gets the TOTP verify POST throttled; the suite then fails
	// in a way that looks nothing like rate limiting (issue #136).
	AuthRateLimitRPS   float64
	AuthRateLimitBurst int

	// Concurrent Argon2id computations. Each holds ~64 MB, so this times that
	// is the memory floor the container must be able to hold (#142).
	AuthHashConcurrency int
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

// authRateLimitRPS reads the auth rate limit in requests/second. Non-numeric, zero and
// negative values fall back to the default rather than being honored: these
// tune a security control, and "0" must never be a way to silently switch off
// rate limiting via a typo in a Secret.
// Production auth rate-limit bounds. Named constants rather than literals at
// the call site so the value a misconfiguration falls back to is greppable.
const (
	defaultAuthRateLimitRPS   = 0.5
	defaultAuthRateLimitBurst = 5

	// 2 x 64 MB = 128 MB of Argon2id working memory. The server container is
	// limited to 256Mi and idles near 80 MiB, so this leaves headroom rather
	// than racing the OOM killer. Raise it only alongside that limit.
	defaultAuthHashConcurrency = 2
)

func authRateLimitRPS() float64 {
	if v := strings.TrimSpace(os.Getenv("AUTH_RATE_LIMIT_RPS")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultAuthRateLimitRPS
}

// constant. Keeping it a parameter rather than hardcoding the default inside
// keeps the default visible at the call site next to the env var name.
//

// authRateLimitBurst reads the auth rate limiter burst size. Same reasoning: a
// non-positive burst would be a denial of service against ourselves, so it
// falls back rather than applying.
func authHashConcurrency() int {
	if v := strings.TrimSpace(os.Getenv("AUTH_HASH_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultAuthHashConcurrency
}

func authRateLimitBurst() int {
	if v := strings.TrimSpace(os.Getenv("AUTH_RATE_LIMIT_BURST")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultAuthRateLimitBurst
}

// envTrimmed reads an env var and strips leading/trailing whitespace. Use for
// values whose format can never legitimately contain surrounding whitespace
// (model names, API keys, URLs, hex keys) so a stray newline from a base64 or
// heredoc typo in a Secret/.env is absorbed rather than silently corrupting the
// value (issue #111). Do NOT use for passwords/secrets that may contain
// intentional edge whitespace (SESSION_SECRET, SMTP_PASSWORD).
func envTrimmed(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func Load() (*Config, error) {
	dbURL := envTrimmed("DATABASE_URL")
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

	// Hoisted out of the Config literal below because ScorerModelGemini
	// defaults to it: production pins GEMINI_MODEL to a preview flash
	// model, and a scorer silently running a different Gemini model than
	// everything else would be a surprising split.
	geminiModel := envOrDefault("GEMINI_MODEL", "gemini-2.5-flash")

	aesKey := envTrimmed("AES_ENCRYPTION_KEY")
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
		AuthRateLimitRPS:     authRateLimitRPS(),
		AuthRateLimitBurst:   authRateLimitBurst(),
		AuthHashConcurrency:  authHashConcurrency(),
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
		GeminiAPIKey:         envTrimmed("GEMINI_API_KEY"),
		GeminiModel:          geminiModel,
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

		AnthropicAPIKey:       envTrimmed("ANTHROPIC_API_KEY"),
		AnthropicModel:        envOrDefault("ANTHROPIC_MODEL", "claude-sonnet-4-6"),
		AnthropicModelContent: envOrDefault("ANTHROPIC_MODEL_CONTENT", "claude-opus-4-6"),
		OpenAIAPIKey:          envTrimmed("OPENAI_API_KEY"),
		OpenAIModel:           envOrDefault("OPENAI_MODEL", "gpt-5-mini"),

		// Scorer models (issue #63). Literals rather than ai.Model*
		// constants: internal/config does not import internal/ai, and
		// the refiner model defaults above are literals for the same
		// reason. Constants named in comments so a grep finds both.
		ScorerModelGemini: envOrDefault("SCORER_MODEL_GEMINI", geminiModel),
		ScorerModelOpenAI: envOrDefault("SCORER_MODEL_OPENAI", "gpt-5-mini"),       // ai.ModelGPT5Mini
		ScorerModelClaude: envOrDefault("SCORER_MODEL_CLAUDE", "claude-haiku-4-5"), // ai.ModelClaudeHaiku45

		AnthropicInputPrice:         envInt64("ANTHROPIC_INPUT_PRICE", 300),
		AnthropicOutputPrice:        envInt64("ANTHROPIC_OUTPUT_PRICE", 1500),
		AnthropicContentInputPrice:  envInt64("ANTHROPIC_CONTENT_INPUT_PRICE", 1500),
		AnthropicContentOutputPrice: envInt64("ANTHROPIC_CONTENT_OUTPUT_PRICE", 7500),

		S3Endpoint:        envTrimmed("S3_ENDPOINT"),
		S3AccessKeyID:     envTrimmed("S3_ACCESS_KEY_ID"),
		S3SecretAccessKey: envTrimmed("S3_SECRET_ACCESS_KEY"),
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
