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
	// Name of the replica set mongod runs as; empty means standalone. A
	// standalone node has no oplog and no multi-document transactions, which is
	// why the write path compensates by hand. Set this and `qomranote migrate`
	// initiates a single-node set on a member that has never been configured.
	// It does NOT put replicaSet= into the URI — that stays MONGO_URI's job.
	MongoReplicaSet string `mapstructure:"MONGO_REPLICA_SET"`

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
	// The two routing policies. Every turn used to pay authoring rates —
	// including the ones that only read the board, classified an ambiguity or
	// synthesised a summary — while the turns that actually need judgement got
	// the same model chosen for cost. Routing, tier annotation and the
	// per-phase spend ledger all shipped; these two are what let a deployment
	// point them at two real models.
	//
	// Both optional, and both defaulting to AGENT_MODEL: left empty (or set
	// equal to it) cognition.New returns the single client it always returned,
	// so an unchanged deployment behaves bit-identically.
	AgentModelFast   string `mapstructure:"AGENT_MODEL_FAST"`
	AgentModelStrong string `mapstructure:"AGENT_MODEL_STRONG"`
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
		"MONGO_REPLICA_SET",
		"R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_PUBLIC_BASE_URL",
		"AGENT_PROVIDER", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "AGENT_MODEL", "AGENT_DAILY_CAP_USD",
		"AGENT_MODEL_FAST", "AGENT_MODEL_STRONG",
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
	if err := cfg.refuseUnsafeProduction(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// placeholderSecret is the value docker-compose.yml supplies when nothing else
// does. It is in the repository, so it is public.
const placeholderSecret = "qomranote-api-secret-change-me"

// refuseUnsafeProduction stops the server rather than starting it wrong.
//
// OPS2. Two settings turn the whole authorization model off, and both had a
// default that runs: docker-compose.yml ships
// KEYCLOAK_ADMIN_CLIENT_SECRET=qomranote-api-secret-change-me — a credential
// committed to a public repository — and CORS_ORIGINS is a free string that
// accepts "*", which hands any page on the internet the ability to drive this
// API with the visitor's bearer token. Neither produced so much as a log line.
// A deployment can therefore be fully "working" and completely open, and the
// only signal is that nothing looks wrong.
//
// It refuses only under APP_ENV=production, and only for the two settings where
// the wrong value is unambiguous. The everyday stack sets APP_ENV=development
// and is untouched. Everything softer — plaintext issuers, the local storage
// driver — is a WARNING (see ProductionWarnings), because those have legitimate
// deployments behind a terminating proxy and refusing them would be the tool
// deciding it knows the topology better than the operator does.
func (c *Config) refuseUnsafeProduction() error {
	if !c.IsProduction() {
		return nil
	}
	var problems []string
	if strings.Contains(c.KeycloakAdminSecret, "change-me") || c.KeycloakAdminSecret == placeholderSecret {
		problems = append(problems,
			"KEYCLOAK_ADMIN_CLIENT_SECRET is still the placeholder from docker-compose.yml, "+
				"which is committed to this repository and therefore public — set a real secret")
	}
	for _, origin := range c.CORSOriginList() {
		if origin == "*" {
			problems = append(problems,
				"CORS_ORIGINS contains \"*\", which lets any page on the internet drive this API "+
					"with a signed-in visitor's token — list the origins explicitly")
			break
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("config: refusing to start in production:\n  - %s", strings.Join(problems, "\n  - "))
}

// ProductionWarnings names settings that are probably wrong in production but
// have honest deployments where they are not.
//
// Returned rather than logged here because config owns no logger, and returned
// rather than fatal because each of these is a judgement about a topology this
// package cannot see: an http issuer is correct behind a proxy that terminates
// TLS, and the local storage driver is correct on a single host with a backed-up
// volume. What is NOT acceptable is silence — the operator should have been told
// once, at boot.
func (c *Config) ProductionWarnings() []string {
	if !c.IsProduction() {
		return nil
	}
	var out []string
	if strings.HasPrefix(c.KeycloakIssuer, "http://") {
		out = append(out, "KEYCLOAK_ISSUER is http://, so bearer tokens cross the network in clear text "+
			"unless something in front of this terminates TLS")
	}
	if strings.HasPrefix(c.PublicAPIBase, "http://") {
		out = append(out, "PUBLIC_API_BASE is http://, so upload and attachment URLs are handed out as plaintext")
	}
	if c.StorageDriver == "local" {
		out = append(out, "STORAGE_DRIVER=local keeps every uploaded file on this host's disk — "+
			"`qomranote backup` does NOT include them, so back up the uploads volume separately")
	}
	// OPS3 — the database has no credentials, and nothing anywhere said so.
	//
	// docker-compose.yml starts `mongo:7` with no MONGO_INITDB_ROOT_* pair, wires
	// the API to `mongodb://mongo:27017`, and publishes "27017:27017" on the host.
	// So on any deployment that reuses that file, the whole product's
	// authorization model — Keycloak, the ACL resolver, the delegation grant, the
	// containment walk, every check this codebase has been hardening — sits behind
	// a TCP port that answers to anyone who can reach it, with no password. A
	// person who can open 27017 does not need to defeat those checks; they read
	// and rewrite every board, every comment and every journal row underneath
	// them. It is the shortest path past all of it and it produced no log line.
	//
	// A WARNING rather than a refusal, matching this function's doctrine: an
	// unauthenticated Mongo on a private network with the port unpublished is a
	// real, if unwise, topology, and refusing it would be this package claiming to
	// know a network it cannot see. What is not defensible is silence.
	if !mongoURIHasCredentials(c.MongoURI) {
		out = append(out, "MONGO_URI carries no username or password, so anything that can reach "+
			"the database port reads and rewrites every board without passing a single "+
			"permission check — enable authentication, and do not publish 27017")
	}
	if c.AnthropicAPIKey != "" || c.GeminiAPIKey != "" {
		if c.AgentDailyCapUSD <= 0 {
			out = append(out, "a model provider is configured with AGENT_DAILY_CAP_USD unset or 0, "+
				"which means no per-user daily spend ceiling")
		}
	}
	return out
}

// mongoURIHasCredentials reports whether a connection string carries a userinfo
// section — mongodb://user:pass@host — without parsing the URI.
//
// Deliberately textual and deliberately generous. A full url.Parse would have to
// take a position on mongodb+srv, on multi-host seed lists and on the dozen
// query options a real deployment carries, and getting any of that subtly wrong
// would make this warn about a database that IS authenticated — which is how a
// boot-time check trains an operator to ignore it. The one thing it must never
// do is claim credentials exist when they do not, and an '@' before the first
// '/' of the host section is the only place a Mongo URI can put them.
//
// The scheme is stripped first so a password containing '/' cannot matter and a
// hostname cannot be mistaken for userinfo.
func mongoURIHasCredentials(uri string) bool {
	rest := uri
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	// Everything past the host section (path, query, fragment) is not userinfo.
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return strings.Contains(rest, "@")
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
