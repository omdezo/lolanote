# The Agent Horizon — everything a truly complete agent must do

*2026-07-31. The successor to AGENT_PERFECTION_2026-07-30.md. That document
fixed what was broken; this one maps what must EXIST. Every claim about the
product below is code-verified against the current tree, not guessed.*

The organizing principle, in design terms: **a capability is an object with an
invariant contract** — schema (what the model may say), handler (authorization
+ containment), compiler (the keys the renderer reads), digest (the read that
makes revision possible), and now a fifth clause learned this month:
**an eval probe that exercises it in a nested workspace**. Anything missing a
clause is not a feature, it is a liability wearing one's name. Every item below
is specified as that five-clause contract or explicitly as something smaller.

Tiers: **T1** = product parity, the agent looks broken without it. **T2** =
what makes it a collaborator rather than a tool. **T3** = what makes it cool —
the demo features. Each item names its layers: FE / BE / DB / AI / UX.

---

## PART I — Product parity the sweep found missing (T1)

The test for this section: a human can do it from the toolbar or context menu
today, and the agent cannot. Every one of these was verified in the frontend
source.

### P1 · The agent ignores locks 🔴 (a WRONG, not a gap)
`content.locked` exists (ElementShell context menu, Lock/Unlock) and **nothing
in the agent or the write path checks it**. A person locks a card precisely so
it will not move, and the agent will move it, retitle it, or trash it.
**Mechanism:** enforcement at BOTH layers, matching the delegation philosophy —
`resolveExisting`/`Preconditions` refuse actions targeting a locked element
(with the refusal naming the lock, so the model routes around instead of
retrying), and the transaction service rejects agent-originated ops on locked
elements as defense in depth (a human's own op still passes — unlocking is a
human's click). Digest renders a 🔒 flag so the model plans around locks
instead of discovering them by refusal. Layers: BE, AI. Probe: a locked card in
the seed; "organise this board" must leave it untouched and unmentioned in
failures.

### P2 · Media cards — audio, video, map
The toolbar's Audio/Video/Map buttons create LINK elements with `embedType`
(`youtube`/`audio`/`googlemaps`), `thumbnailUrl`, `showPreview:true`, resolved
through `api.resolveLink`. The agent's `create_link` writes none of these —
an agent-made YouTube link is a dead grey card where a human's is a playable
embed. **Mechanism:** the link path calls the existing resolve-metadata service
server-side (it is an outbound fetch, so it rides the `maxURLsPerRun` quota and
the ⟨web⟩ trust label); embedType inferred from the URL exactly as the toolbar
does. One new optional schema field (`kind: video|audio|map`) for when the
model knows the intent. Layers: BE, AI. Probe: "add the trailer
https://youtube.com/… to the mood board" → compiled content carries embedType +
thumbnail.

### P3 · Templates exist and the agent cannot use them
`ElementService.UseTemplate` stamps a template board's subtree as a fresh
editable copy; the picker UI lists system + own templates. The agent rebuilds
production structures from scratch every time. **Mechanism:** two verbs.
`use_template(templateId, parentId)` — staged as a whole-subtree create
(CopySpec machinery from `duplicate` already does exactly this shape); and
`save_as_template(boardId)` — flags a board the agent just built, gated on
confirmation like a delete. The digest lists available templates by name in
AUTHORING context. Layers: BE, AI, UX (template chips in the composer).
Probe: "set up sprint planning like my usual" with a seeded template → the plan
instantiates rather than reinvents.

### P4 · Table formulas — the agent writes dead numbers
TableCard evaluates `=` cells with arithmetic and B2-style refs
(`lib/formula.ts`). The agent's `edit_table`/`create_table` write literal
strings; asked for a budget it writes "4900" as a constant that lies the moment
a line changes. **Mechanism:** prompt + digest, no new plumbing — the schema
already carries strings. Teach the register: totals are `=SUM(B2:B9)`-shaped
formulas, and the digest's `tableDigest` must render BOTH the raw formula and
the evaluated value (the model cannot revise what it sees only the result of).
Layers: AI, BE (digest). Probe: "add a total row to the budget" → the compiled
row contains a `=` cell referencing the column, not a constant.

### P5 · Due dates are not reminders
Tasks carry `dueDate` (rendered with overdue styling) AND `reminderAt`; the
agent has only `set_reminder`. "Schedule the shoot week" produces reminders
nobody sees on the card. **Mechanism:** `set_due(elementId, date)` riding the
same generic update op as set_reminder; digest renders due dates with an
OVERDUE flag — which also unlocks "what is overdue?" as a REPORTING answer.
Layers: BE, AI. Probe: seeded overdue tasks; "what's slipping?" names them.

### P6 · @Mentions in authored text
Card text supports @mentions (picker + notification on commit). Agent-authored
cards never mention anyone even when the request says "flag this to Sara".
**Mechanism:** the compile path resolves "@Name" against `scope.People` (id
list already in the digest) into the mention format the renderer stores;
unresolvable names are left as plain text and disclosed. The existing
assignment-notification guard (board-access check) already covers the
notification side. Layers: BE, AI. Probe: "note the permit issue and flag it to
<seeded person>" → mention token in compiled content, not plain text.

### P7 · Board identity: tile color and icon
`useBoardStyle` gives every board tile a color and an icon from a picker; the
agent creates anonymous grey boards. A five-board structure it builds is
navigable by *reading*; a human's is navigable by *glance*. **Mechanism:**
optional `color`/`icon` on create_board + a `style_board` verb; the icon
vocabulary is the picker's own id list, served in the digest the way
`cardSwatches` already is (model picks a name, server resolves). Layers: BE,
AI. Probe: authoring probe asserts distinct icons on sibling boards.

### P8 · Trash: the agent cannot undo the past
Restore exists (`ActionRestore`, trash batches); the agent cannot name it. "Get
back the column I deleted yesterday" is answerable by a human in two clicks and
by the agent not at all. **Mechanism:** `list_trash` (read, owner-scoped) +
`restore(elementId)` staging an ActionRestore — batch semantics come free.
Layers: BE, AI. Probe: seeded trashed batch; "bring back the casting column"
restores the batch, creates nothing.

---

## PART II — Perception and intelligence (T2)

### I1 · Semantic sight — search that understands, not matches
`Search` is text lookup. "Find everything about the harbour interview" misses
the card that says "talk to the port master". This gates three features the
agent cannot fake: semantic duplicate detection (the dup guard is name-based),
"file these where they belong" at real quality, and cross-board related-item
discovery. **Mechanism:** embeddings sidecar — a vector per element text,
stored on the element doc (DB migration), refreshed on write via the existing
transaction broadcast path; `search` gains a semantic mode; the dup guard gains
a similarity check ABOVE the token check with a high threshold and the same
refusal-names-the-twin UX. Layers: DB, BE, AI. This is the single highest-value
T2 item: perception quality caps every register.

### I2 · Counting and arithmetic done by the server
Models miscount. MEASURED exists because of it — but table math, budget
totals, "how many days of shooting", schedule spans are still model-side.
**Mechanism:** a `compute` read tool (expression over the board: counts by
type/label/column, sums over table columns, date spans) — the server states
facts, the model interprets them, same division of labour as MeasurePlan.
Layers: BE, AI. Probe: "how much is the budget over?" answered with the
server's number in the comment.

### I3 · The board as a timeline
Cards carry dates (due, reminders, created); nothing can answer "what happens
next week?" or lay a schedule out temporally. **Mechanism:** `arrange(ids,
"timeline")` — a layout mode ordering by the elements' own dates along an axis
with a server-drawn scale; plus the digest's OVERDUE/UPCOMING flags from P5.
Layers: BE (layout), AI, FE (scale rendering). The film-production user asks
for this constantly in disguise: shoot schedules ARE timelines.

### I4 · Reading what links point at
`read_url` reads a page the model names; link cards on the board are opaque
titles. "Summarise the references on this board" cannot be done. **Mechanism:**
none new — permit `read_url` to take an element id whose URL it fetches,
riding the same quota and ⟨web⟩ labelling. Layers: BE. Probe: seeded links;
"which of these references covers permits?" cites the right one.

### I5 · Cross-board intelligence with consent
The run is root-scoped by design (delegation). But "reorganise using my other
board too" (E2) works only when the person names the board. The agent cannot
propose: "three other boards hold related material — include them?"
**Mechanism:** a `suggest_scope` phase — search across owned boards (read-only,
already legal), surfaced as a QUESTION with the boards named; widening only on
the person's yes, which mints the wider delegation. The ask tool already
carries options. Layers: BE, AI, UX.

---

## PART III — Conversation and UX (T2)

### U1 · The run should be a thread, not an episode
Refinements + Continue exist, but each run's UI is a fresh composer. The
mental model people bring is chat: a persistent side panel per board showing
the run history as a conversation — intents, outcomes, unmet, with Continue
and Revert inline on each turn. All data exists (`ListByBoard`); this is FE
composition, not backend work. Layers: FE, UX.

### U2 · Point at things while asking
ScopeSelection exists (ids ride the task), but the composer does not let you
select-then-ask fluently, and the model receives ids without emphasis.
**Mechanism:** selection chips in the composer ("about these 3 cards"), and the
digest marks selected items with ⭐ so attention follows the pointing. Layers:
FE, AI (digest flag). "This column" + a click beats forty words of description.

### U3 · Explain any action on demand
`Because` exists per action but is sparsely filled. The review list should
answer "why?" for every row — cheaply: the review turn already makes the model
look at the plan; require one clause per *surprising* action (moves and deletes
of existing material), skip creates (the plan IS the reason). Layers: AI, FE.

### U4 · Agent presence on the canvas
While a run works, the board shows nothing until ghosts appear. A presence
cursor — "reading Pre-Production…", "writing the shot list…" — driven by the
existing event stream (`step.finished` messages already say this) turns dead
time into theatre and doubles as progress truth. Layers: FE, UX. Pure
composition over events already emitted.

### U5 · Arabic as a first-class agent language
The product is bilingual (full ar i18n); the agent's outputs follow the
request's language by accident, not contract. **Mechanism:** the prompt states
the rule — answer in the request's language; board content language wins over
UI language for authored text — and two eval probes (one Arabic authoring, one
mixed board) grade it. `dir="auto"` already handles rendering. Layers: AI.
For this user specifically, this is T1 in spirit.

### U6 · Onboarding: the agent introduces itself
An empty board's composer shows nothing about what is possible. Three
seed-suggested intents based on board state ("this board has 40 loose cards —
want them organised?") — the ambient-suggestion machinery exists; surface it
in the empty-composer state. Layers: FE, UX.

---

## PART IV — Autonomy and time (T2→T3)

### A1 · Scheduled runs
"Every Sunday, file my Unsorted tray." A `schedule` on a stored task spec
(cron-lite: weekly/daily/interval), executed under AutonomyAuto's existing
constraints (no deletes, revertible, disclosed) with the outcome landing as a
notification. DB: a schedules collection; BE: a ticker loop next to the
existing purge loop; UX: schedule management in settings + per-run provenance
("ran on schedule"). The safety story is already built — this is plumbing.

### A2 · Rules: event-triggered micro-runs
"When something lands in Unsorted, file it." A rule = trigger (element
created/moved into X) + stored intent, run scoped to the trigger element,
budget tiny, auto-apply allowed because per-element blast radius is one card.
Rides the transaction broadcast the realtime layer already emits. DB, BE, UX.
This is the feature that makes the agent feel ALIVE rather than summoned.

### A3 · Watch and report
"Tell me when the budget column changes." A watcher = scope + condition +
notification, no writes at all — the read-only sibling of A2, and the safest
possible autonomy to ship first.

### A4 · The morning digest
A scheduled REPORTING run: "what moved on this board this week, what is
overdue, what is blocked" — delivered as a notification + one board comment.
Composes A1 + P5 + I2 with zero new mechanism.

---

## PART V — Platform, data, integration (T3)

### X1 · Import: paste anything, get structure
A pasted markdown doc, meeting notes, or CSV becomes a structured board — the
authoring register applied to supplied text instead of imagination. Mechanism:
attachment/paste rides the existing AttachmentIDs path; a `structure_text`
workload seeds the plan. The reverse of X2. Layers: BE, AI, FE (paste target).

### X2 · Export through the agent
"Turn this board into a one-page brief" → write_document already covers the
in-board version. The out-of-app version (markdown/PDF download) needs an
export endpoint (§7.2 lists export as a product feature) the agent can target.
Layers: BE, FE.

### X3 · Calendar awareness (integration, consent-gated)
Due dates and shoot schedules live on boards; the person's calendar lives
elsewhere. A read-only calendar connector (ICS/CalDAV) lets "schedule the
shoot" avoid real conflicts. Every fetched fact is labelled by source and never
carries instructions. Layers: BE, AI, DB.

### X4 · Voice intent
The composer takes a mic press; speech-to-text feeds the same intent path.
Mobile/PWA especially (the PWA shell exists). Layers: FE.

### X5 · Generated imagery (mood boards)
"Build a mood board for a night-time coastal drama" produces color swatches
and prose today; an image-generation provider behind the cognition plane's
Provider interface would let it stage IMAGE cards with generated frames,
budget-capped like URL fetches. The trust label is ⟨agent⟩. Layers: AI, BE.
Deliberately last: cost, moderation, and product identity questions before
engineering ones.

---

## PART VI — Trust, safety, resilience (standing debts, restated)

- **S1 Provider fallback** (resilient.go exists — wire a second provider and
  the retry policy; today one API outage kills every run).
- **S2 Run queue** — one run per board is right; one run per USER across
  boards is not enforced; a schedule burst (A1) needs a queue with fairness.
- **S3 Locked elements** — P1 is also a safety item; listed once, counted twice.
- **S4 Rate/spend governance per account** — per-run caps exist; per-day
  account caps do not. A2/A1 make this mandatory before autonomy ships.
- **S5 The injection posture under new reads** — P2/I4/X3 add outbound fetch
  surfaces; each must ride the existing quota + ⟨web⟩ labelling + the
  lifted-id detector. Stated here so no wave ships a fetch that forgets.

---

## PART VII — Priority and shape

**Wave α (parity + the found wrong):** P1 🔴, P2, P4, P5, P7, P8 — small,
mechanical, five-clause contracts each; P1 first because it is a live wrong.
**Wave β (the collaborator):** P3, P6, U1, U2, U3, U5 + I2, I4.
**Wave γ (sight):** I1 (embeddings) — its own wave; DB migration + backfill.
**Wave δ (alive):** A3 → A1 → A4 → A2, in that order — read-only watcher
first, rules last, S4 before any of it.
**Wave ε (world):** I3, I5, X1, X2, U4, U6; then X3–X5 as product decisions.

The five-clause rule is the law of every wave: schema, handler, compiler,
digest, nested-workspace probe — or it does not ship.
