package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"qomranote/backend/internal/agent"
)

// A dependency the agent Config declares and serve.go never passes is a
// capability that ships and does nothing — silently, because a nil collaborator
// is indistinguishable from a quiet one.
//
// It has now happened three times. The worst was Attachments: only the image
// FETCHER was wired, so look_at could read a file while ownedAttachments —
// which returns nil the moment the repository is nil — discarded every id the
// composer sent. Asked to place a picture that was sitting right there, the
// agent answered "the request does not provide an attachment ID". True of what
// it had been handed, and a lie to the person who attached it. place_attachment
// had never once run in production.
//
// Reading the source is the only way to check this: the failure is an absent
// line, and nothing about an absent line is observable from inside the package.
func TestServeWiresEveryAgentDependency(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("serve.go"))
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	block := string(src)
	start := strings.Index(block, "agent.NewService(agent.Config{")
	if start == -1 {
		t.Fatal("could not find the agent config in serve.go; if it moved, move this test with it")
	}
	block = block[start : start+4000]
	if end := strings.Index(block, "})"); end != -1 {
		block = block[:end]
	}

	// Fields that are legitimately optional in a default deployment, each with
	// the reason it is allowed to be absent. Anything NOT listed here must be
	// wired, so adding a dependency forces a decision rather than defaulting to
	// silence.
	optional := map[string]string{
		"Memory":     "the learned-preference store is opt-in",
		"Links":      "URL reading is a separate deployment choice",
		"Attachment": "covered by Attachments",
	}

	var missing []string
	cfg := reflect.TypeOf(agent.Config{})
	for i := 0; i < cfg.NumField(); i++ {
		name := cfg.Field(i).Name
		if _, ok := optional[name]; ok {
			continue
		}
		// A word boundary, not a line start. Several fields share a line
		// (`Txns: txnSvc, TxnRepo: transactions,`), and a line-anchored match
		// reported seven wired dependencies as missing — a guard that cries
		// wolf is a guard somebody deletes.
		if !regexp.MustCompile(regexp.QuoteMeta(name) + `:\s`).MatchString(block) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("agent.Config declares %v and serve.go never sets them — each is a "+
			"capability that will fail silently in production", missing)
	}
}
