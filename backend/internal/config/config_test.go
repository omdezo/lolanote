package config

import (
	"strings"
	"testing"
)

// OPS2 — two settings switch the authorization model off, and both had a
// default that starts cleanly.
//
// docker-compose.yml supplies KEYCLOAK_ADMIN_CLIENT_SECRET
// "qomranote-api-secret-change-me" when nothing else does. That string is in
// this repository, so it is a public credential; the API used it to talk to
// Keycloak's admin API and nothing anywhere said a word. And CORS_ORIGINS is a
// free-form string that happily accepts "*", which lets any page on the internet
// drive this API with a signed-in visitor's bearer token — the frontend's own
// test file already argues that case (TestCORSOriginsAreExplicit) and nothing
// enforced it.
//
// The refusal is scoped to APP_ENV=production and to the two values where wrong
// is unambiguous. Everything softer is a warning, because refusing an http
// issuer would be this package deciding it knows the deployment's topology
// better than the operator.

func TestProduction_RefusesTheCommittedPlaceholderSecret(t *testing.T) {
	cfg := &Config{
		AppEnv: "production", CORSOrigins: "https://app.example.com",
		KeycloakAdminSecret: placeholderSecret,
	}
	err := cfg.refuseUnsafeProduction()
	if err == nil {
		t.Fatal("production started with the secret that is committed to this repository")
	}
	if !strings.Contains(err.Error(), "KEYCLOAK_ADMIN_CLIENT_SECRET") {
		t.Errorf("the refusal does not name the setting to change: %v", err)
	}

	cfg.KeycloakAdminSecret = "a-real-rotated-secret"
	if err := cfg.refuseUnsafeProduction(); err != nil {
		t.Fatalf("a properly configured production was refused: %v", err)
	}
}

func TestProduction_RefusesWildcardCORS(t *testing.T) {
	cfg := &Config{
		AppEnv: "production", KeycloakAdminSecret: "real",
		CORSOrigins: "https://app.example.com, *",
	}
	err := cfg.refuseUnsafeProduction()
	if err == nil {
		t.Fatal(`CORS_ORIGINS "*" was accepted in production`)
	}
	if !strings.Contains(err.Error(), "CORS_ORIGINS") {
		t.Errorf("the refusal does not name the setting: %v", err)
	}
}

// The everyday stack runs APP_ENV=development with exactly these values. If the
// guard fired there it would be an unusable check that gets deleted.
func TestDevelopment_IsUntouched(t *testing.T) {
	cfg := &Config{
		AppEnv: "development", CORSOrigins: "*",
		KeycloakAdminSecret: placeholderSecret,
	}
	if err := cfg.refuseUnsafeProduction(); err != nil {
		t.Fatalf("development was refused: %v", err)
	}
	if w := cfg.ProductionWarnings(); len(w) != 0 {
		t.Fatalf("development produced production warnings: %v", w)
	}
}

// The warnings are the only place an operator is ever told that their uploaded
// files sit outside every backup, or that the assistant has no daily ceiling.
func TestProductionWarnings_NameWhatIsUnprotectedRatherThanRefusing(t *testing.T) {
	cfg := &Config{
		AppEnv: "production", KeycloakAdminSecret: "real",
		CORSOrigins:     "https://app.example.com",
		KeycloakIssuer:  "http://keycloak.internal/realms/qomranote",
		PublicAPIBase:   "http://api.internal",
		StorageDriver:   "local",
		AnthropicAPIKey: "sk-test", AgentDailyCapUSD: 0,
	}
	if err := cfg.refuseUnsafeProduction(); err != nil {
		t.Fatalf("these are warnings, not refusals — a proxy-terminated deployment is legitimate: %v", err)
	}
	joined := strings.Join(cfg.ProductionWarnings(), "\n")
	for _, must := range []string{"KEYCLOAK_ISSUER", "PUBLIC_API_BASE", "STORAGE_DRIVER=local", "AGENT_DAILY_CAP_USD"} {
		if !strings.Contains(joined, must) {
			t.Errorf("no warning mentions %s:\n%s", must, joined)
		}
	}
	if !strings.Contains(joined, "backup") {
		t.Error("the local-driver warning does not say that `qomranote backup` excludes uploaded files, " +
			"which is the fact an operator only discovers during a restore")
	}

	// A correctly configured production is silent. MongoURI now has to carry
	// credentials to qualify — see OPS3: an unauthenticated database is not a
	// correct production configuration, whatever else is right.
	clean := &Config{
		AppEnv: "production", KeycloakAdminSecret: "real",
		CORSOrigins:    "https://app.example.com",
		KeycloakIssuer: "https://id.example.com/realms/qomranote",
		PublicAPIBase:  "https://api.example.com",
		StorageDriver:  "r2",
		MongoURI:       "mongodb://qomranote:s3cret@mongo:27017/qomranote?authSource=admin",
	}
	if w := clean.ProductionWarnings(); len(w) != 0 {
		t.Fatalf("a correct production configuration still warned: %v", w)
	}
}

// OPS3 — nothing anywhere told an operator that their database has no password.
//
// docker-compose.yml starts mongo:7 with no root credentials, points the API at
// mongodb://mongo:27017, and publishes the port on the host. Every permission
// check in this codebase is reachable around, not through, by anyone who can
// open 27017 — and the product booted, ran and looked healthy while that was
// true.
func TestProductionWarnings_NameTheUnauthenticatedDatabase(t *testing.T) {
	// The exact string docker-compose.yml hands the API.
	cfg := &Config{
		AppEnv: "production", KeycloakAdminSecret: "real",
		CORSOrigins:    "https://app.example.com",
		KeycloakIssuer: "https://id.example.com/realms/qomranote",
		PublicAPIBase:  "https://api.example.com",
		StorageDriver:  "r2",
		MongoURI:       "mongodb://mongo:27017",
	}
	joined := strings.Join(cfg.ProductionWarnings(), "\n")
	if !strings.Contains(joined, "MONGO_URI") {
		t.Fatalf("an unauthenticated database produced no warning:\n%s", joined)
	}
	// It is a warning, not a refusal: a private network with the port unpublished
	// is a real topology, and refusing would be this package asserting it knows a
	// network it cannot see.
	if err := cfg.refuseUnsafeProduction(); err != nil {
		t.Fatalf("an unauthenticated database was refused rather than reported: %v", err)
	}
}

// The check must never claim credentials exist when they do not, and must never
// warn about a database that IS authenticated — a boot warning an operator has
// learned to ignore is worse than no warning.
func TestMongoURICredentials_ReadsTheUserinfoAndNothingElse(t *testing.T) {
	authenticated := []string{
		"mongodb://user:pass@mongo:27017",
		"mongodb://user:pass@mongo:27017/qomranote?authSource=admin",
		"mongodb+srv://user:pass@cluster0.example.mongodb.net/qomranote",
		// A password containing the characters that terminate the host section.
		"mongodb://user:p%2Fw%3Fd@mongo:27017/qomranote",
		"mongodb://user@mongo:27017", // x.509 / Kerberos: a username and no password
	}
	for _, uri := range authenticated {
		if !mongoURIHasCredentials(uri) {
			t.Errorf("%q was read as unauthenticated; the operator would be warned about a database that is fine", uri)
		}
	}
	anonymous := []string{
		"",
		"mongodb://mongo:27017",
		"mongodb://mongo:27017/qomranote",
		"mongodb://mongo1:27017,mongo2:27017/qomranote?replicaSet=rs0",
		// An '@' that is not userinfo: it is past the host section.
		"mongodb://mongo:27017/qomranote?appName=api@prod",
	}
	for _, uri := range anonymous {
		if mongoURIHasCredentials(uri) {
			t.Errorf("%q was read as authenticated; the whole point is that this case is reported", uri)
		}
	}
}
