// Package config is the single source of truth for runtime configuration.
// Values come from (highest precedence first): environment variables, a .env
// file in the working directory, and built-in development defaults.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// Config holds every tunable of the QomraNote API.
type Config struct {
	AppEnv   string `mapstructure:"APP_ENV"`   // development | production
	HTTPAddr string `mapstructure:"HTTP_ADDR"` // e.g. ":8080"
	LogLevel string `mapstructure:"LOG_LEVEL"` // debug | info | warn | error

	MongoURI string `mapstructure:"MONGO_URI"`
	MongoDB  string `mapstructure:"MONGO_DB"`

	// Keycloak. Issuer is the URL embedded in tokens (as browsers see it);
	// InternalBase is how the API container reaches Keycloak. They differ in
	// docker-compose split-horizon setups.
	KeycloakIssuer       string `mapstructure:"KEYCLOAK_ISSUER"`
	KeycloakInternalBase string `mapstructure:"KEYCLOAK_INTERNAL_BASE"`
	KeycloakRealm        string `mapstructure:"KEYCLOAK_REALM"`
	KeycloakAdminClient  string `mapstructure:"KEYCLOAK_ADMIN_CLIENT_ID"`
	KeycloakAdminSecret  string `mapstructure:"KEYCLOAK_ADMIN_CLIENT_SECRET"`
	KeycloakWebClient    string `mapstructure:"KEYCLOAK_WEB_CLIENT_ID"` // public client used to verify current passwords

	// Object storage: "local" (dev fallback, files served by the API) or "r2"
	// (Cloudflare R2 via its S3-compatible API, presigned direct uploads).
	StorageDriver     string `mapstructure:"STORAGE_DRIVER"`
	LocalStorageDir   string `mapstructure:"LOCAL_STORAGE_DIR"`
	PublicAPIBase     string `mapstructure:"PUBLIC_API_BASE"` // base URL for local-driver upload/download links
	R2AccountID       string `mapstructure:"R2_ACCOUNT_ID"`
	R2AccessKeyID     string `mapstructure:"R2_ACCESS_KEY_ID"`
	R2SecretAccessKey string `mapstructure:"R2_SECRET_ACCESS_KEY"`
	R2Bucket          string `mapstructure:"R2_BUCKET"`
	R2PublicBaseURL   string `mapstructure:"R2_PUBLIC_BASE_URL"`

	CORSOrigins string `mapstructure:"CORS_ORIGINS"` // comma-separated

	// AI agent. Leaving AnthropicAPIKey empty disables the agent entirely: the
	// API reports the feature as unavailable and no run can be admitted. That
	// is the intended default — the agent is opt-in per deployment.
	AgentProvider    string  `mapstructure:"AGENT_PROVIDER"` // anthropic | gemini; empty infers from the keys present
	AnthropicAPIKey  string  `mapstructure:"ANTHROPIC_API_KEY"`
	GeminiAPIKey     string  `mapstructure:"GEMINI_API_KEY"`
	AgentModel       string  `mapstructure:"AGENT_MODEL"`         // empty lets the provider choose its default
	AgentDailyCapUSD float64 `mapstructure:"AGENT_DAILY_CAP_USD"` // per user, per UTC day; 0 = uncapped
	// Rates for AGENT_MODEL, in USD per million tokens. Set these when the
	// model has no built-in price, otherwise every run costs a reported $0 and
	// AGENT_DAILY_CAP_USD can never trigger.
	AgentPriceInPer1M  float64 `mapstructure:"AGENT_PRICE_INPUT_PER_1M"`
	AgentPriceOutPer1M float64 `mapstructure:"AGENT_PRICE_OUTPUT_PER_1M"`
}

// Load reads .env (if present) and the environment into a validated Config.
func Load() (*Config, error) {
	loadDotEnv() // best-effort; absent in containers where env is injected

	v := viper.New()
	v.AutomaticEnv()

	defaults := map[string]string{
		"APP_ENV":                      "development",
		"HTTP_ADDR":                    ":8080",
		"LOG_LEVEL":                    "info",
		"MONGO_URI":                    "mongodb://localhost:27017",
		"MONGO_DB":                     "qomranote",
		"KEYCLOAK_ISSUER":              "http://localhost:8081/realms/qomranote",
		"KEYCLOAK_INTERNAL_BASE":       "",
		"KEYCLOAK_REALM":               "qomranote",
		"KEYCLOAK_ADMIN_CLIENT_ID":     "qomranote-api",
		"KEYCLOAK_ADMIN_CLIENT_SECRET": "",
		"KEYCLOAK_WEB_CLIENT_ID":       "qomranote-web",
		"STORAGE_DRIVER":               "local",
		"LOCAL_STORAGE_DIR":            "./data/uploads",
		"PUBLIC_API_BASE":              "http://localhost:8080",
		"R2_BUCKET":                    "qomranote",
		"CORS_ORIGINS":                 "http://localhost:5173,http://localhost:3000",
	}
	for key, val := range defaults {
		v.SetDefault(key, val)
		_ = v.BindEnv(key)
	}
	// Keys with no default still need explicit binding for Unmarshal to see them.
	for _, key := range []string{
		"R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_PUBLIC_BASE_URL",
		"AGENT_PROVIDER", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "AGENT_MODEL", "AGENT_DAILY_CAP_USD",
		"AGENT_PRICE_INPUT_PER_1M", "AGENT_PRICE_OUTPUT_PER_1M",
	} {
		_ = v.BindEnv(key)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if cfg.KeycloakInternalBase == "" {
		// Same-horizon default: discovery goes straight to the issuer.
		cfg.KeycloakInternalBase = strings.TrimSuffix(cfg.KeycloakIssuer, "/realms/"+cfg.KeycloakRealm)
	}
	if cfg.StorageDriver == "r2" && (cfg.R2AccountID == "" || cfg.R2AccessKeyID == "" || cfg.R2SecretAccessKey == "") {
		return nil, fmt.Errorf("config: STORAGE_DRIVER=r2 requires R2_ACCOUNT_ID, R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY")
	}
	return &cfg, nil
}

// loadDotEnv finds .env by walking up from the working directory.
//
// The file lives at the repository root, but `go run ./cmd/qomranote` is
// normally invoked from backend/. Looking only in the working directory meant
// local runs silently ignored .env and reported missing configuration that was
// in fact present one level up.
func loadDotEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for i := 0; i < 4; i++ { // repo roots are never far above a package
		candidate := filepath.Join(dir, ".env")
		if _, err := os.Stat(candidate); err == nil {
			_ = godotenv.Load(candidate)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// CORSOriginList splits the configured origins.
func (c *Config) CORSOriginList() []string {
	parts := strings.Split(c.CORSOrigins, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// IsProduction reports whether the app runs with production hardening.
func (c *Config) IsProduction() bool { return c.AppEnv == "production" }
