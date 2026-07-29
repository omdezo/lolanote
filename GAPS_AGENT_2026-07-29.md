# QomraNote — Agent & Harness Gap Audit (29 July 2026)

Every finding below was read out of the code, not inferred. Line counts and
structural claims are measured. Where a gap is already tracked in
`GAPS_AUDIT_2026-07.md` it is cross-referenced rather than restated.

Severity: 🔴 blocks correctness or safety · 🟠 structural debt that will bite ·
🟡 feature depth · ⚪ polish

---

## Part 1 — Harness architecture (OOP)

The harness works, and its *runtime* design is sound: read-live/write-staged,
one transaction, delegation as attenuation. The problem is that none of that is
expressed in **types**. Behaviour lives in switches over string constants, and
state lives in one object that everything mutates.

### A1 🔴 `staging.Execute` is a 796-line switch

**Measured:** `internal/agent/tools.go` is 1 661 lines; one function is 796 of
them, dispatching 30 tool cases.

**Why it matters.** It already produced two production failures this month.
Eleven tools were implemented in the switch and absent from the catalogue —
compiling, passing tests, and unreachable — because *nothing in the type system
connects a tool's definition to its behaviour*. A scripted edit deleted the
catalogue block and no compiler complained, because a `case` and a `ToolDef`
are unrelated values that happen to share a string.

**The fix — make a tool an object.**

```go
// A Tool is one capability: what it offers the model, and what it does.
// The pairing is the point — a tool that cannot describe itself cannot be
// registered, so "implemented but never offered" stops being expressible.
type Tool interface {
    Definition() cognition.ToolDef
    Execute(ctx context.Context, rt *Runtime, raw json.RawMessage) Outcome
}

// Availability decides whether a tool is offered for a given run, replacing
// the boolean parameters that ToolCatalogue has already outgrown.
type Availability interface{ AvailableFor(task TaskSpec, deps Deps) bool }

type Registry struct{ tools map[string]Tool }
func (r *Registry) Register(t Tool)                          // one place, one call
func (r *Registry) Catalogue(task TaskSpec) []cognition.ToolDef
func (r *Registry) Dispatch(ctx, name string, raw json.RawMessage) Outcome
```

Each tool becomes a small type — `arrangeTool`, `mergeTool`, `lookAtTool` —
with its own file, its own test, and its own quota field. `TestToolCatalogue_…`
becomes unnecessary because the invariant is structural rather than asserted.

**Migration is incremental**: `Registry.Dispatch` can fall through to the
existing switch for tools not yet extracted, so this is not a rewrite.

### A2 🔴 `staging` is a 27-field god object

**Measured:** 27 fields, mixing five unrelated concerns —

| Concern | Fields |
|---|---|
| Dependencies | `elements labels txns images links emit` |
| Plan under construction | `plan created runID scope task` |
| Per-tool quotas | `newLabels connections urlsRead imagesSeen commented asked` |
| Security ledger | `outOfScope discovered` |
| Loop state | `finished everFinished reviewed question failedCalls placedThisRun movedThisRun pendingImages` |

**Why it matters.** Every new capability adds a field, and each is mutated from
inside the giant switch with no invariant anywhere. `placedThisRun` and
`movedThisRun` exist only to catch the plan contradicting itself — a rule that
belongs in one place and is currently enforced in three (`toolMove`,
`stagePlacements`, `Preconditions`).

**The fix — four objects with real responsibilities.**

```go
type Deps struct { Elements …; Labels …; Txns …; Images …; Links … } // injected, never mutated

type PlanBuilder struct{ … }        // owns Actions; enforces coherence on Add
func (b *PlanBuilder) Add(a Action) (string, error)   // rejects place-then-move HERE, once

type Quotas struct{ … }             // one counter type, per-tool limits
func (q *Quotas) Take(name string) error

type SecurityLedger struct{ … }     // out-of-scope ids, discovered boards
func (l *SecurityLedger) Refuse(kind, id string) error
func (l *SecurityLedger) ShouldQuarantine() bool

type Runtime struct{ Deps; *PlanBuilder; *Quotas; *SecurityLedger; Scope; Task }
```

The contradiction rule then lives in `PlanBuilder.Add` and cannot be bypassed,
instead of being re-implemented wherever someone remembers.

### A3 🟠 An action kind touches four files

Adding one capability today means edits in `plan.go` (const), `plan.go`
(compile case), `verify.go` (`shapeOf`), and `tools.go` (definition + execute).
Nothing enforces that all four happened — `create_table` shipped compiling and
uncompilable because the `CompileOps` dispatch was missed.

**The fix — behaviour on the kind.**

```go
type ActionSpec interface {
    Kind() ActionKind
    Validate(a Action) error                       // was shapeOf's case
    Compile(a Action, scope *BoardScope) ([]domain.Op, error) // was CompileOps' case
}
var actionSpecs = map[ActionKind]ActionSpec{…}     // one registry, exhaustively tested
```

A test then asserts every registered `ActionKind` has a spec, closing the same
class of gap as A1.

### A4 🟠 `Service` is 1 025 lines with seven responsibilities

Admission, the state machine, run execution, apply/commit, revert, audit, and
drift detection all live on one struct with 16 dependencies.

**The fix.** `Admission` (quota, ACL, single-flight), `RunCoordinator` (state
machine + execution), `Committer` (compile, authorize destinations, apply,
verify), `Reverter`, `Insights` (audit + drift). `Service` becomes a façade that
wires them — useful for tests, which currently need the whole world to check
one decision.

### A5 🟡 No seam for a second workload

Everything assumes one board and one intent. A scheduled tidy, a workspace-wide
inbox sweep, or a "watch this board" job has nowhere to live. A `Workload`
interface (scope compiler + prompt + tool set + verifier) would let those exist
without touching the runtime.

---

## Part 2 — Backend gaps

### B1 🔴 Whole layers have no tests

**Measured** — packages with `.go` files and zero `_test.go`:

`transport/http` · `repository/mongo` · `realtime` · `auth` · `config` ·
`storage` · `cli` · `repository/memory` · `agent/agentmem`

The HTTP surface — every handler, every route guard, every error envelope — is
untested, as is all Mongo persistence. The agent has 45 tests; the layer that
exposes it has none.

**The fix.** Handler tests over `httptest` with a fake service (the interfaces
already exist), and `mongo` tests behind a build tag against a container. Start
with the routes that carry authorization decisions.

### B2 🔴 Open items from `GAPS_AUDIT_2026-07.md` §1

None of these are agent-specific and all remain open. Restated only as a
checklist, since that document has the detail:

- Attachment orphan leak + no ownership check on referenced `attachmentId`
- Reparenting permits containment cycles (`MoveAcrossBoards`)
- SSRF DNS-rebinding TOCTOU in link metadata — *now reachable by the agent via
  `read_url`*, which raises its priority
- Notification spam to arbitrary subs via unchecked `assigneeId`/`mentions` —
  *the agent can now set `assigneeId`*, same escalation
- Realtime `Send` on a closed channel can panic the process
- Label attach does not verify label ownership (the agent's path does; the
  human path does not)
- Content-controlled `isHome`/`isTemplate`
- Account/email enumeration via `/users/lookup`
- Duplicate / UseTemplate / ConvertToClone are non-transactional and un-broadcast
- Local blob URLs unauthenticated and leak `sub`
- Multi-op ACID is best-effort (no replica set → no `WithTransaction`)

Two of these matter more now than when they were written, because the agent
reaches them.

### B3 🟠 No per-user rate limit, only a daily cost cap

`checkDailyCap` bounds spend per UTC day. Nothing bounds *frequency*: a loop
firing runs every second is admitted until the cap is reached, and each holds a
board slot and a goroutine.

**The fix.** A token bucket per tenant in `Admission`, and a run-concurrency
limit per tenant independent of the per-board single-flight.

### B4 🟠 Runs execute in-process with no lease

A run is a goroutine; the boot reconciler cleans up what a crash left. That is
honest for seconds-long work, but a restart mid-run loses the spend and the
user sees a run that silently reverts to failed. An activity lease and a
resume-from-journal path would make it survivable.

### B5 🟡 No per-board agent opt-out

Some boards should be off limits. There is no way to say so — `agentInstructions`
can ask, but nothing enforces.

### B6 🟡 Digest budget is global, not adaptive

`maxNestedItems = 120` regardless of model context window or how much of the
budget the prompt already consumed. A large board silently elides while a small
one wastes headroom.

---

## Part 3 — Frontend gaps

### F1 🟠 `AgentBar.tsx` is 706 lines holding seven components

`Pill`, `Ask`, `Working`, `Decide`, `PlanList`, `PlanRow`, `Done`, plus
`useAgentShell` and a local `BoltIcon`. Each has distinct state and lifecycle.

**The fix.** One file per state under `src/agent/bar/`, with the shell reduced
to a state→component map. This is also what makes them individually testable —
there is currently one agent test file in the entire frontend, which I added
after a crash.

### F2 🔴 The agent UI is English-only

**Measured:** `src/components/Toolbar.tsx` imports `t` from `../i18n`. No file
under `src/agent/` does. Every string — presets, buttons, outcomes, the drift
hint, error toasts — is hard-coded English in an app that is otherwise
translated and RTL-aware.

**The fix.** Route all agent copy through `t()`, and add the keys. The composer
already sets `dir="auto"` on input; the *chrome* does not.

### F3 🟠 Two run states have no dedicated outcome copy

`outcomeText` handles `COMPLETED`, `REVERTED`, `PARTIAL`, `DISCARDED`,
`CANCELLED` and falls through for the rest. `FAILED` and `DENIED` therefore
render a generic message — a permission refusal reads identically to a model
error, which is exactly when a person needs to know which it was.

### F4 🟡 No optimistic or partial rendering of a long plan

A 40-action plan appears at once when the run finishes. The ticker shows staged
actions during the run, but the review list does not build incrementally.

### F5 🟡 The review list is mouse-only

No keyboard navigation between rows, no way to drop an action from the
keyboard. The rest of the app has a full keyboard map.

### F6 ⚪ Attachment feedback is thin

No size or type validation before upload, no progress for a large PDF, and no
indication that a document costs more than an image.

---

## Part 4 — User experience gaps

### U1 🔴 No cost preview before a run

The meter shows spend *after*. A person choosing between "organize this board"
and "read this 60-page PDF and organize it" gets no signal that one costs 50×
the other until it has happened.

**The fix.** Estimate from the compiled digest size plus attachments, and show
it on the send button when it exceeds a threshold. The pieces exist:
`CompileScope` knows the item count, `visionTypes` knows the attachments.

### U2 🟠 Revert is all-or-nothing after apply

Before applying, individual actions can be dropped. Afterwards the only option
is reverting the entire run. "That was mostly right, undo the third column" has
no answer.

**The fix.** The journal records inverse ops per element; a per-action revert is
a filtered replay of the same inverses.

### U3 🟠 The agent cannot say "I could not do that part"

A run either proposes or fails. If two of five requested things are impossible,
nothing distinguishes them from the three it did — the summary is prose the
user has to read carefully.

**The fix.** A structured `Unmet []string` on the Plan, rendered as a distinct
block, not buried in the summary.

### U4 🟡 The drift hint checks once per board open

No re-check after the board changes, so a board that becomes messy while you
work is never mentioned; and a board you tidy keeps the stale hint until
navigation.

### U5 🟡 No history surface for the agent

`Audit` exists as an endpoint and appears as two lines in the composer. There is
no way to see everything the agent has done on a board, or to revert something
from last week.

### U6 ⚪ No affordance explaining what the agent can do

The composer offers three presets and a text field. A new user has no way to
discover labels, arrange, connections, filing, or attachments short of guessing.

---

## Part 5 — Features not built

| Feature | Why it is not trivial |
|---|---|
| 🟡 `create_image` placeholder | "Add an image placeholder to each scene" currently produces a note *about* an image. Needs an IMAGE element with no attachment, and a renderer that shows an empty frame. |
| 🟡 Screenshot → structured elements | `look_at` reads an image; turning its layout into elements is a distinct job the playbook only gestures at. |
| 🟡 Sketch cleanup | Vision can read a sketch; converting strokes to elements and connections is real work. |
| 🟡 Scheduled / recurring runs | No scheduler, no way to express "tidy this every Monday". |
| 🟡 Workspace map | Cross-board overview board, generated and kept current. |
| 🟡 Agent-authored templates | Turning a board that worked into a reusable template. |
| ⚪ Voice capture | Explicitly deferred. |

---

## Suggested order

1. **A1 + A3** — the Tool and ActionSpec registries. Both classes of silent
   failure they close have already happened, and every later capability is
   cheaper once they exist.
2. **B1 (HTTP handlers)** and **F2 (i18n)** — the two places where "untested"
   and "unusable for half the audience" are the honest descriptions.
3. **U1 + U3** — cost preview and unmet requests. Both are about the user
   knowing what they are getting before and after, which is where trust lives.
4. **A2 + A4** — the god-object splits. Large, mechanical, and best done once
   the registries above have already moved code out of them.
5. **B2** — the pre-existing audit items, prioritising SSRF and notification
   spam, which the agent now reaches.
