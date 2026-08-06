# The agent, audited — the short version

*A mobile-readable digest of `AGENT_FRONTIER_2026-07-31.md` (3,184 lines, 706 KB —
too heavy to preview on a phone, which is why this exists).*

**359 items found · 143 live wrongs · ~153 fixed and deployed · ~43% closed**

Two research fleets swept fifteen corners. Every claim in the full document carries
a `file:line` or a URL — nothing was taken on faith, including my own earlier claims,
four of which the research proved wrong.

> **How to read the status.** An item counts as fixed only where I read the code
> myself, or an agent reported it done and I spot-checked it. Anything unverified
> is listed as open. Claiming a fix I have not seen is the exact failure this whole
> audit exists to end.

---

## The corners, by weight

| Corner | Items | Live wrongs | What it covers |
|---|---:|---:|---|
| **DA** Scope & write path | 27 | 16 | What the agent sees, reaches, and can safely change |
| **AX** Accessibility | 38 | 15 | Keyboard, screen reader, the agent as an input channel |
| **DL** Data & trust | 22 | 14 | Retention, deletion, disclosure, what leaves the machine |
| **JN** Journeys & seams | 21 | 12 | Between two moments, boards, people, lifecycle states |
| **CV** Capability landing | 24 | 12 | What it makes, and whether the canvas actually shows it |
| **FR** Failure & recovery | 24 | 20 | States a person can reach and not get out of |
| **MP** Multiplayer | 19 | 10 | The agent on a board other people share |
| **SC** Scale & cost | 24 | 10 | The physics of a big board |
| **IL** Instrumentation | 27 | 10 | The signal a learning loop is made of |
| **DF** Film domain | 38 | 9 | Production as it is actually practised |
| **MO** Mobile & touch | 20 | 8 | A phone, where asking beats dragging |
| **CG** Cognition & memory | 18 | 5 | Judgement, measurement, what it remembers |
| **PL** Platform | 21 | 2 | APIs, spend governance, the operator surface |
| **IN** Industry frontier | 20 | 0 | What canvas products shipped in 2025–26 |
| **LP** The plan loop | 16 | 0 | Propose, approve, correct — and learn from it |

---

## The ten that mattered most

**1 · DA1 — a cross-tenant write escalation.** Two ordinary writes let *any*
authenticated user overwrite *any element in the database*, on a board they could
not read. Holding a clone was being treated as authority over what it points at.
**Fixed, exploit pinned as a test.**

**2 · FR1 — my own regression.** A reconciler killed every in-flight run every five
minutes, claiming "the server restarted." Raising the run deadline to eight minutes
to fix step starvation turned that from a lottery into a certainty. **Fixed.**

**3 · MP3 — your agent conversations were public.** The run journal — intent,
refinements, steer text — was broadcast to every board viewer, including anonymous
share-link holders. Now a five-key allowlist to the room, the full frame only to you.

**4 · CV1 — the sixth content-key mismatch.** `set_color` wrote `content.color`;
every renderer reads `content.backgroundColor`. Self-consistent, too: the digest read
the same wrong key, so the agent saw its own colours and believed the taxonomy existed.

**5 · DA2 — cross-board filing never worked.** A hundred-line subsystem — `filing.go`,
`DestinationBoardIDs`, `authorizeDestinations`, the `file_to` verb — 403'd on every
case it existed for. Two gates designed as AND, where the first predated delegation.

**6 · DA3 — the staged plan was write-once.** ~18 revise verbs refused every id the
same run had just created. "Make twelve cards and tag them all Q3" was impossible.
A hidden cause of the create-instead-of-edit failure you kept seeing.

**7 · DA4 — the agent was blind to checklists.** TASK elements never entered scope,
so three shipped verbs could never resolve an existing item — and the failure read
as hallucination rather than blindness.

**8 · CG1 — a settings field feeding nothing.** Account-wide standing notes were
collected, sanitised, truncated, translated… and never shown to the model. Six lines.

**9 · CG2 — `trustAgent` was declared and never assigned.** The agent could not tell
its own prior output from yours, which quietly falsified the digest's own stated
invariant that nothing enters unlabelled.

**10 · DL11 — deleting your account destroyed collaborators' work.** Hard-deleted,
not trashed, unrecoverable, with no ownership transfer anywhere. Now refused.

---

## What the research corrected in my own earlier documents

- **X1 is false** — import cannot ride the attachment path; vision rejects text types.
- **X2 is wrong** — the export endpoint already shipped; the defect is a lossy renderer.
- **S4 is wrong** — per-day caps exist; the gaps are different ones.
- **P4's mechanism was backwards.**

That is the most valuable thing a research fleet produces.

---

## Still unexplored after two sweeps

Named honestly, because nobody should mistake this for exhaustive:

- **Nobody has attacked this product.** DA1 was found by *reading*. No injection
  attempt, no route auth-bypass sweep, no SSRF probe, no fuzzing. *(In progress.)*
- **Is there any database backup at all?** Four documents do not establish one.
  Mongo is a standalone node — no transactions, no point-in-time recovery. *(In progress.)*
- **Nothing a real user actually did.** Every finding comes from reading code or
  literature. No telemetry, no session recording, no usability test.
- **Whether the Arabic is idiomatic.** No native reader has judged the register —
  and the domain research argues register is the whole question in this trade.

---

*Full detail — evidence, failure, mechanism, and the hard clause for each of the 359
items — is in `AGENT_FRONTIER_2026-07-31.md`.*
