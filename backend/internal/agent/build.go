package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"
)

// The experiment identity of a run: which code, which prompt, which tools,
// which budgets.
//
// Every wave of work in this product changes a prompt, a budget, a tool set or
// a model, and until now the run row recorded none of it. So an apply rate that
// moved after a deploy could not be attributed, a regression could not be
// bisected, and the honest answer to "did that wave help?" was "we cannot tell"
// — which is exactly how the eval suite's pass rate came to be a number a
// person typed into a markdown file rather than a number anything measured.
//
// Four hashes, computed once. No schema per experiment and no feature-flag
// system: with a build stamp on the row, "the last 200 runs on build A versus
// build B" is one group-by.

// GitSHA is stamped at link time:
//
//	-ldflags "-X qomranote/backend/internal/agent.GitSHA=$(git rev-parse HEAD)"
//
// When it is empty the module's own VCS stamp is used, so an ordinary `go
// build` in a git tree still produces an attributable run.
var GitSHA string

var (
	buildOnce  sync.Once
	buildStamp BuildStamp
)

// CurrentBuild returns this process's build identity. Computed on first use and
// then constant: the prompt, the catalogue and the budgets cannot change while
// the process runs, so hashing them per run would be pure waste.
func CurrentBuild() BuildStamp {
	buildOnce.Do(func() { buildStamp = computeBuild() })
	return buildStamp
}

func computeBuild() BuildStamp {
	// The catalogue is hashed at its widest — every tool the deployment can
	// ever offer. Hashing the per-run subset would make the stamp vary with the
	// run's autonomy rather than with the code, which is the opposite of what a
	// grouping key is for.
	catalogue, err := json.Marshal(ToolCatalogue(true, true))
	if err != nil {
		catalogue = []byte(err.Error())
	}
	d := DefaultBudget()
	budgets := fmt.Sprintf("steps=%d actions=%d tokens=%d cost=%.4f deadline=%s scope=%d",
		d.MaxSteps, d.MaxActions, d.MaxTokens, d.MaxCostUSD, d.Deadline, maxScopeElements)
	return BuildStamp{
		GitSHA:        gitSHA(),
		PromptHash:    shortHash([]byte(systemPrompt)),
		CatalogueHash: shortHash(catalogue),
		BudgetsHash:   shortHash([]byte(budgets)),
	}
}

func gitSHA() string {
	if GitSHA != "" {
		return GitSHA
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

// shortHash is twelve hex characters — enough to separate builds, short enough
// to read in a log line or a group-by key.
func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}
