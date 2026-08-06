# The Agent, Complete — every wrong, every missing piece, and the mechanism for each

*2026-07-30. Source of truth for the perfection wave. Evidence comes from the live
database: runs `6a6b7c11…` (group into boards), `6a6b8100…` (make a film), and
`6a6b818f…` ("complete"), their event streams, and the element graph they left
behind.*

The one-sentence diagnosis: **the agent was built and evaluated on flat boards,
and every one of its subsystems — perception, geometry, quality review,
deduplication — silently degrades or turns off the moment the workspace becomes
nested.** Organizing your board is the first thing the agent does well, and it is
the act that lobotomizes every run after it.

Two kinds of items below. **W** = confirmed wrong (observed misbehaving in the
live runs). **M** = missing entirely (nothing misbehaved because nothing exists).
Each carries the mechanism — the *logic* of the fix, not just the wish — and an
acceptance test that a machine can check.

---

## PART I — The wrongs (W)

### W1 · Depth blindness — the root cause 🔴

**Observed:** both post-nesting runs read the workspace as *"5 items in scope"*
— five board tiles — while ~60 columns, notes, documents and checklists sat
inside them, invisible.

**Why:** `BuildBoardScope` walks the root board and descends one level into
*columns*, never into *nested boards*. A nested board contributes its title and
nothing else. Every downstream consumer of scope — preconditions, the duplicate
guard, layout occupancy, the self-review — inherits the blindness.

**Mechanism:** scope becomes a budgeted subtree walk, not a one-level read.
- Depth-first from the root, descending into `BOARD` children, to a depth cap
  (4) and an element budget (~400), breadth-first *within* each level so no
  single huge board starves the siblings.
- Every element lands in `scope.Elements` (so ids resolve, preconditions pass,
  the dup guard sees siblings, layout knows occupancy) even when its *text* is
  elided for token budget.
- The digest renders nested boards as indented sections:
  `BOARD b1 "Pre-Production" — 9 columns, 31 cards` followed by its columns and
  one level of their children, then `… 12 more (read_board 9d92b0… for the rest)`
  when the cap bites.
- Per-canvas occupancy: `scope.OccupiedByCanvas[boardID]` replaces the single
  root `Occupied`, computed during the same walk. (Feeds W3.)

**Acceptance:** a scope built over a board containing a nested board containing
a column containing a card includes all four in `scope.Elements`; the digest
mentions the card's text; `read board — N items` reflects the subtree count.

### W2 · Duplicate structure creation

**Observed:** the "complete" run created 18 near-duplicate empty columns —
including an exact-name `Editing` beside the existing `Editing` in the same
parent. The duplicate-sibling guard fired on nothing because it checks the
scope, and the scope couldn't see the sibling (W1).

**Mechanism:** two layers on top of W1.
- The guard compares **normalized** names (case-fold, strip punctuation and
  `&`/`and`, collapse whitespace) within the same parent, and treats a
  prefix/contains match ≥ 60 % of the shorter name as a duplicate
  (`Concept` vs `Concept & Premise`).
- **Idempotent create-redirect (the elegant half):** `create_column` /
  `create_board` aimed at a parent that already holds an *empty* container with
  a matching normalized name does not refuse — it returns the existing id as if
  the create had succeeded: *"'Concept' already exists here and is empty — use
  col-xyz; I did not create a second one."* The 18-empty-columns run would then
  have become 18 *fills*, which is exactly what "complete" meant.

**Acceptance:** staging `create_column "Editing"` into a parent that holds an
empty `Editing` stages nothing and returns the existing id; staging into a
parent holding a *non-empty* `Editing` refuses and names it.

### W3 · Geometry skipped for existing sub-boards 🔴

**Observed:** the committed op for column `Concept` was
`{parentId, section, index:0}` — **no position, no width**. The board renders
width-0 columns scattered over the real ones. The earlier fix covered only
boards *created in the same plan*; `placesOnCanvas` returns false for a parent
that is an *existing* board.

**Mechanism:**
- `placesOnCanvas` returns true when the destination parent is **any** board —
  the run's root, one the plan creates, or one that already exists in scope.
- `packCanvas` runs per destination canvas, seeded with that canvas's own
  occupancy from `scope.OccupiedByCanvas` (W1), so new columns join the
  existing row *on that canvas* and never overlap what is there.
- The viewport anchor applies only to the root canvas (the user is looking at
  it); sub-board canvases pack relative to their own occupied bounds.

**Acceptance:** a plan filing a column into an existing nested board compiles
an op whose location carries a position and a width, and the position does not
intersect the boxes of that board's existing columns.

### W4 · Step starvation and the silent half-answer 🔴

**Observed:** budget is 60 actions but **14 model turns**; the model stages ~2
actions per turn, so ~28 actions is the true ceiling. The "make a film" run was
cut at step 14 mid-flow — last column created and left empty — and shipped as
`COMPLETED` with **no summary and no unmet**. A half-answer indistinguishable
from a whole one.

**Mechanism:** three independent levers, all needed.
- **Batching:** the prompt tells the model explicitly that several tool calls
  per turn is the norm — "stage a whole column and its cards in one turn" —
  with a worked example. (The provider already supports parallel calls; the
  model was doing 2 out of politeness, and once did 17, so it can.)
- **Honest exhaustion:** the forced-finish path synthesizes what the model
  didn't get to say: `summary` = "ran out of room at step N of M — the plan is
  a prefix of the answer", and one `unmet` naming the containers staged but
  left empty. This also feeds W6: the next run inherits an actionable to-do.
- **Budget shape:** `maxSteps` rises to 24 (the cost cap already bounds spend);
  the pre-read stays server-side so reads of *sub-boards* the model chooses to
  make are what steps are spent on.

**Acceptance:** a run terminated by the step budget completes with a non-empty
summary containing "ran out", and unmet naming at least one unfilled container.
A prompt-conformance eval shows ≥ 4 actions per turn on an authoring run.

### W5 · The quality loop was structurally unreachable 🔴

**Observed:** the review turn — MEASURED, critique, the *"STOP — shelves with
nothing on them"* nudge, the register mismatch — is gated on `RenderSelfView`,
which returns `""` when nothing lands on the **root** canvas. Everything in
these runs was filed into sub-boards → the entire loop was skipped. The one
guard built precisely for the 18-empty-columns plan never ran.

**Mechanism:** decouple judgement from geometry.
- `reviewTurn` fires on **any non-empty plan**. The *view* section degrades
  gracefully: canvas render when there are root placements, otherwise a
  containment-tree render (`▣ Pre-Production › Casting (3 cards) › …`) built
  from the same destinations machinery.
- MEASURED, critique, hollow nudge and mismatch are computed and appended
  regardless of which view rendered.

**Acceptance:** a plan that files 18 empty columns into nested boards receives
the hollow STOP nudge in its review turn (asserted on a scripted provider), and
the events stream shows the review step.

### W6 · No memory between runs

**Observed:** "complete" arrived as a fresh, context-free run. The previous
run's forced finish had produced *no summary* (W4), so even the history block
had nothing. The system had already written the perfect instruction — *"the
structure was created but nothing was put inside it"* — and the next run never
saw it.

**Mechanism:** the digest gains a `PREVIOUS RUN` block: the last completed
run's intent, summary, and unmet list (from the runs store, same board, most
recent 1–2, ~600 tokens capped). Pointed, not archival: *"Two minutes ago this
person asked X; the run answered Y and left Z undone."* W4's honest exhaustion
guarantees the block always has substance.

**Acceptance:** a run started after a run with unmet `["fill columns A, B"]`
contains that text in its system context (unit test over digest assembly), and
the "complete" follow-up eval fills existing empty columns instead of creating
new ones.

### W7 · Ambiguity never triggers the ask tool

**Observed:** "complete" — one word, no referent — went straight to guessing.
The `ask` tool exists for exactly this.

**Mechanism:** a server-side nudge, not a hard gate (heuristics that block are
heuristics that one day block the right answer). When the intent is ≤ 3 words
or pronoun-only AND the previous-run block (W6) resolves it, proceed with that
reading *and say so*. When it is short AND nothing resolves it, the first
system message says: *"This request is one word. If the board plus the previous
run make the meaning obvious, proceed; otherwise ask() before staging
anything."*

**Acceptance:** eval probe: intent "complete" on a board whose previous run
left unmet → fills the named containers; intent "fix it" on a fresh board with
no history → the run asks.

### W8 · Moves strand connectors

**Observed:** four LINE elements still sit on the root canvas, each joining two
columns that now live in *different* nested boards. Lines render only when both
endpoints resolve on their own canvas → four invisible arrows. The delete
cascade (built earlier) never covered *moves*.

**Mechanism:**
- In the transaction write path, after a move: every LINE on the element's old
  canvas that references it is re-homed — if both endpoints now share a canvas,
  the line moves there with them; otherwise it is soft-deleted in its own batch
  (it is invisible anyway, and trash is reversible).
- `SweepOrphanConnectors` extends to catch **cross-canvas** lines, not just
  dead-endpoint lines, so existing strands get cleaned on the next `migrate`.
- Design intelligence (prompt): when a grouping run moves connected elements
  into different containers, redraw the relationship *between the containers* —
  the arrows between stages should survive as arrows between boards.

**Acceptance:** service test — moving one endpoint of a connected pair into a
nested board leaves no live LINE on the old canvas pointing across canvases;
moving *both* endpoints to the same board moves the line with them. Migrate
sweep counts the 4 existing strands.

### W9 · Shell-in-shell structure

**Observed:** board `Pre-Production` containing a column named `Pre-Production`
(twice, one a pre-existing duplicate). Mechanically correct, unreadable as
design.

**Mechanism:** a critique line, not a hard rule: when a plan moves a column
into a board with a matching normalized name, the review says: *"Board X will
contain a single column with its own name — move the CARDS into the board and
leave the redundant shell behind, or rename it to what distinguishes it."*
Prompt gains the same guidance in the ORGANISING register.

**Acceptance:** unit test on the critique output for such a plan.

### W10 · Telemetry lies

**Observed:** every run reports `usage.calls: 1` regardless of turns;
`cachedTokens` inconsistent. Minor, but it hid W4's starvation.

**Mechanism:** increment per provider call at the call site; carry cache reads
through the same accumulator. **Acceptance:** a 3-turn scripted run reports
`calls: 3`.

---

## PART II — The missing (M)

### M1 · The review UI is blind exactly where the agent now works

Ghost previews render only for root-canvas placements. A plan that files 30
elements into nested boards shows five untouched tiles and a text list — the
person approves what they cannot see. **Mechanism:** each board tile that
receives staged children gets a ghost badge (`+7 inside`, hover lights the dock
rows, click filters the review list to that destination). Honest depth-preview
without rendering foreign canvases. **Acceptance:** frontend test — a plan
filing into a nested board renders a badge with the right count.

### M2 · No continuation — a big job cannot span runs

The film build was cut at 30 actions with no way to say "keep going" except
typing a fresh, context-free prompt. **Mechanism:** when a run ends with unmet,
the outcome card offers **Continue** — one click starts a new run whose intent
is server-composed from the unmet list, with W6 carrying the thread. The two
runs link (`continuesRunId`) for the audit trail. **Acceptance:** API test —
continuation run's task carries the prior unmet as intent and links back.

### M3 · Documents are write-once

`write_document` exists; nothing can *edit* a document's body — the same
create-only failure the tables had. **Mechanism:** `set_note_text` accepts
DOCUMENT targets (compiles `textPreview` + `doc` + keeps title), digest already
shows document bodies (built yesterday), so the model can read-then-revise.
**Acceptance:** capability test compiling a document edit; content map carries
both keys.

### M4 · No way to merge or dissolve redundant columns

The user's board now holds duplicate columns (`Dev & Scoping` ×2, `Editing`
×2) and the agent has no repair verb — `merge_notes` covers cards only.
**Mechanism:** `merge_columns(keepId, dropId)`: moves `dropId`'s children to
the end of `keepId` (ordering via existing OrderPlan), then trashes the empty
shell. Destructive → preview-gated like delete. **Acceptance:** capability test
— children re-parented in order, shell trashed, inverse restores.

### M5 · The corpus never leaves flatland 🔴

Every probe seeds a flat board; the entire W-series lived undetected because
the evals measure a world the product leaves after one organizing run.
**Mechanism:** `seedNestedWorkspace` — root board, 3 nested boards, columns
with cards inside, one empty column, a previous-run record with unmet — plus a
**G-series** of probes:
- G1 "add three casting cards" → cards land in the casting column *inside* the
  nested board, with positions/index, no new top-level structure.
- G2 "complete" after a seeded previous-run-with-unmet → fills the named empty
  column; **zero** new columns.
- G3 "make a film…" with maxSteps forced low → completes with "ran out" summary
  and unmet (honest exhaustion).
- G4 grouping probe → no live cross-canvas lines afterwards.
- G5 scope probe → digest mentions a card 3 levels deep.
- G6 duplicate probe → `create_column "Editing"` beside existing empty one
  redirects instead of duplicating.
**Acceptance:** the G-series exists, runs in `agent-eval`, and passes.

### M6 · Robustness debt (listed, deliberately deferred)

Provider fallback, a run queue (one run per board today), live re-sync on
concurrent edits beyond the fingerprint. Real, not in this wave — they change
infra, not agent quality, and nothing in the observed failures touches them.

---

## PART III — The dependency graph and the wave plan

```
W1 (sight) ──┬─▶ W2 (dedup)      ──┐
             ├─▶ W3 (geometry)     ├─▶ M5 (nested evals prove it all)
W4 (budget) ─┼─▶ W5 (review)      │
             └─▶ W6 (memory) ─▶ W7 ─▶ M2 (continue)
W8 (connectors), W9, W10, M1, M3, M4 — parallelizable after W1
```

Execution: sequential waves in one working tree (the files overlap too much for
parallel checkouts), each wave one Opus 5 coding agent at high/xhigh effort,
Fable 5 organizing at the start and a final Opus verifier proving every
acceptance line. Full live corpus (old F-series + new G-series) runs once at
the end, followed by container rebuild and deploy.

| Wave | Items | Effort | Files (primary) |
|---|---|---|---|
| 1 SIGHT | W1, W2 | xhigh | digest.go, boardstate.go, tools.go |
| 2 GEOMETRY | W3 | xhigh | layout.go |
| 3 LOOP | W4, W5, W7 | xhigh | planner.go, selfview.go, toolhandlers.go |
| 4 MEMORY | W6, M2, W10 | high | store.go, digest.go, service.go, frontend outcome card |
| 5 FABRIC | W8, W9, M3, M4 | high | transaction_service.go, capabilities.go, critique |
| 6 PROOF | M5, M1 | high | agentcorpus.go, agenteval.go, GhostLayer.tsx |
| 7 VERIFY | all acceptance lines | xhigh | read-only + test runs |

**After the wave, one open item for the user:** the junk the broken agent left
on the real board — 18 empty columns (run `6a6b818f…` is one click of per-run
revert), duplicate columns (new `merge_columns` can repair), 4 stranded lines
(`migrate` sweeps). Reverting live user data is the user's click, not the
plan's.

---

# RESULT (2026-07-31)

Executed by an 8-agent fleet — Fable 5 organizing, six Opus 5 coding waves
(high/xhigh), an Opus 5 xhigh verifier — with every wave and the organizer's
guidance human-reviewed at its gate. All W-items and M-items landed; the
verifier's six findings were closed by hand afterwards.

**Final live corpus: 33 probes, 31–33 passing per run** (~$0.68/sweep).
Deterministic failures: none. Two probes are probabilistic at the margin of the
shipping model (gemini-2.5-flash), both passing on re-run:

- **B4** occasionally restructures while answering a reporting question.
- **G2** ("complete" as a follow-up) holds at roughly 5-in-6. It took three
  escalations: prompt prose (1/3) → review-turn directive with the last word
  (1/4) → **a once-only finish refusal that QUOTES the previous run's own
  written leftover** (5/5 in isolation). The mechanism finding of the whole
  effort: *the model argues with assertions and complies with quotes.* Both
  remaining edges are graduated, disclosed, and human-review-gated — an
  overreaching plan reaches the person labelled as such, never silently.

Deploy notes: containers rebuilt and live; `migrate` swept exactly the 4
stranded cross-canvas arrows the verifier could not reach (root board now holds
0 live LINEs); the widened membership hash invalidated any pre-deploy PROPOSED
plan once, as predicted (organizer risk #3).

Left deliberately with the user: revert of the 18-empty-column run (one click),
`merge_columns` repair of the pre-existing duplicate columns, Gemini key
rotation, and committing the tree.
