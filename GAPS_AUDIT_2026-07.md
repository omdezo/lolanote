# QomraNote — System Gap Audit (July 2026)

Anchored to the system's stated goal (PLAN.md §0): *a production-grade Milanote clone —
a visual, board-based workspace where every element is a typed card, every mutation a
transaction, sync is element-granular realtime, and the whole thing runs as a hardened
Docker stack.* This audit measures the **current code** against that goal and against the
project's own `GAPS_PLAN.md` (which described fixes as *planned* — many are only partly
delivered).

Method: four parallel deep reads of the backend (security + completeness), frontend, and
infra/tests, each citing `file:line`. The critical finding was hand-verified.

Severity: 🔴 critical/security · 🟠 correctness/core-UX · 🟡 feature depth · 🔵 platform · ⚪ production/ops

---

## 0. The one that needs fixing today

### 🔴 CRITICAL — Cross-tenant data disclosure via a forged CLONE element
`board_service.go:104-118`. When assembling a board's children, the server collects every
`CLONE` child's `content.cloneSourceId` and calls `GetMany(sourceIDs)`, appending the
**full source elements with no `RequireView` check**. The transaction create path lets any
user create a `CLONE` on their own board pointing at *any* element id
(`transaction_service.go:141-155,444-448` only check the parent is within the caller's board).

**Exploit:** attacker creates `{type:"CLONE", content:{cloneSourceId:"<victim card id>"}}`
on their own board, then `GET /boards/<own>/children` returns the victim's private card
content. Full read-ACL bypass across tenants.

**Fix:** before appending resolved clone sources, run each through `access.RequireView`
(or resolve the source's nearest board and confirm the caller can view it); drop the ones
they can't. Add a regression test — this is exactly the IDOR class `transaction_service_test.go`
already covers for the write path, now needed on the read path.

---

## 1. Security & correctness gaps 🔴

Verified state of `GAPS_PLAN.md` Batch 1 (what actually shipped):

| Claim | State | Evidence |
|---|---|---|
| 1.1 Transaction IDOR guard (ops verified vs board subtree) | ✅ solid | `transaction_service.go:60-231`, memoized board walks; tested |
| 1.2 Anonymous + password-protected view links | ✅ | `share_service.go:192-214` bcrypt; `X-Share-Password` header |
| 1.3 Resync on WS reconnect | ✅ | `socket.ts:13,47-55` `hadDisconnect`→`refreshBoard()` |
| 1.4 Trash cascade + `trashBatchId` | ✅ | `transaction_service.go:403-424`, `elements.go:180-187` |
| 1.5 Attachment ownership + GC | ◐ **partial** | GC sweeps only `presigned`; see below |
| 1.6 WS one-time ticket (no token in URL) | ✅ | `auth/tickets.go`, `handlers.go:230-248` |
| 1.7 Multi-op atomicity | ◐ **partial** | pre-validation only; no Mongo `WithTransaction` |

New findings beyond Batch 1:

- 🔴 **CLONE read-ACL bypass** — §0 above.
- 🟠 **Attachment orphan leak + no link check** (`upload_service.go`, `maintenance_service.go:43`). There is no `linked` attachment state; element create/update never verifies the caller owns a referenced `attachmentId`, and GC only reaps `status=presigned`. An **uploaded** blob whose element is later deleted is never collected → storage leaks forever; a foreign `attachmentId` can be referenced freely. (`GAPS_PLAN` 1.5a unmet.)
- 🟠 **Reparenting allows containment cycles** (`transaction_service.go:184-194`, `MoveAcrossBoards:298-368`). Move validates the new parent is in-board but never checks it isn't a *descendant* of the moved element. Move a board into its own child → subtree detaches from Home, becomes unreachable; only the depth-64 walk cap prevents a loop.
- 🟠 **SSRF residual: DNS-rebinding TOCTOU** in link metadata fetch (`link_service.go:29-44`). Host IPs are checked, then `DialContext` re-resolves the **hostname** — a short-TTL domain can pass the check as a public IP and dial `169.254.169.254`/`127.0.0.1`. Fix: dial the already-validated IP.
- 🟠 **Notification spam/phishing to arbitrary subs** (`transaction_service.go:103-134`, `comment_label_service.go:66-78`). `assigneeId` and `mentions[]` are trusted from the client with no board-access check; message text is attacker-influenced. Anyone knowing a victim's `sub` can push crafted notifications.
- 🟠 **Realtime `Send` on closed channel can panic the process** (`realtime/client.go:93-109`). `Send` races the `close(c.send)` done from another goroutine (readPump defer / auth timer). `sync.Once` guards the close body but not a concurrent send → send-on-closed-channel panic. Reachable via slow-consumer disconnect + auth-timer rollover. Guard with a done-channel `select` or recover.
- 🟡 **Label attach doesn't verify label ownership** (`comment_label_service.go:212-230`) — stamps another owner's `labelId`, bumps their `usageCount`.
- 🟡 **Content-controlled privileged flags** (`isHome`, `isTemplate`) read from freely-patchable `content` (`transaction_service.go:520-523`, `elements.go:88`). An editor of a shared board can set `content.isHome:true` to break the owner's delete/share/export guards.
- 🟡 **Account/email enumeration** (`handlers.go:71-81`, `user_service.go:128-143`) — `/users/lookup` confirms any email→{sub,name,email} to any authenticated caller; ROPC password verify only globally rate-limited.
- 🟡 **Duplicate / UseTemplate / ConvertToClone are non-transactional & un-broadcast** (`element_service.go:38-196`) — multi-insert outside the txn log, no broadcast; partial failure orphans copies, peers don't see results until refetch.
- 🔵 **Local blob URLs unauthenticated + leak subs** (`router.go:15`, keys `u/{sub}/{id}/{file}`) — public, no expiry, owner `sub` in the URL. (Path traversal itself is handled, `local.go:39-45`.)

**Multi-op ACID (1.7):** still best-effort. A failure on op *N* leaves ops 1..N-1 persisted with no rollback — Mongo is standalone (no replica set), so `WithTransaction` isn't even available. This is the schema-level gap behind the whole "every mutation is a transaction" promise.

**Confirmed sound** (checked, not vulnerable): `$`-operator/NoSQL injection (typed DTOs; `MergePatch` whitelists `content/location/labelIds`), ReDoS (query escaped, capped), body-size limits (64M global + per-route reader caps), CSRF (bearer, no cookies), CORS (explicit allowlist), JWT verifier (`azp` pinned), error leaks (generic 500s), path traversal.

---

## 2. Backend feature completeness 🟠🟡

Route surface vs PLAN §3.3: **no endpoint is missing**; several extras shipped (`/boards`, `/templates`, `/me/*`, `/elements/move`, `/realtime/ticket`). Gaps are in *depth*:

- 🟠 **Search is stubbed** (`handlers.go:188`, `board_service.go:153`, `elements.go:211-228`). Spec is `?q=&boardId=&sort=viewed|modified`; the handler parses **only `q` + `limit`**. `boardId` and `sort` are ignored, and there is **no "viewed" tracking** in the schema, so `sort=viewed` is unimplementable as-is. Search is a non-indexed `$regex` scan.
- 🟠 **The text index is dead** (`indexes.go:23-27` vs `elements.go:211-228`). A Mongo `$text` index is built but never used — search runs `$regex` instead. Either wire `$text` or drop the index; §2.5's stated purpose is unserved.
- 🟡 **ALIAS is inert** (`element.go:17`). `content.targetBoardId` is never dereferenced anywhere server-side — no follow logic, no export case. Stored as a generic element with no behavior (frontend fakes the look).
- 🟡 **TABLE formulas server-side: none** (deferred per §7) — evaluation is client-only.
- 🟡 **SKETCH/ANNOTATION**: pass-through only; content never validated, not exported.
- 🟡 **`subscribe` WS message unhandled** (`client.go:136-173`) — board scope is fixed at handshake; the spec-listed client message is a silent no-op.
- 🟡 **Clone fan-out event-name drift** — `fanOutCloneUpdates` re-broadcasts `transaction.applied` rather than the spec's targeted `element.updated` (`transaction_service.go:253`). Functionally equivalent.
- 🔵 **Content is schemaless & unvalidated per-type** (`element.go:109`) — server validates only that `type` is known; no per-type payload shape check for any of the 19 types.

Domain model: all 19 element types defined; CLONE/COMMENT_THREAD/BOARD/COLUMN/etc. fully handled. Export markdown/text/json are **all three real** (`board_service.go:183-223`).

---

## 3. Frontend feature & UX gaps 🟠🟡🔵

The frontend is far more complete than `GAPS_PLAN` implies — context menus, clipboard, toasts+ErrorBoundary, notifications bell, boards drawer, template picker, label chips/filter, task dates/assignees, line toolbar, @mentions, PWA, IndexedDB cache, and per-content bidi all shipped. Remaining gaps:

- 🟠 **One `window.prompt` remains** (`UnsortedTray.tsx:48`) — the global Ctrl/⌘+Enter quick-capture still uses the native dialog; everything else uses the portal `prompt()`. (Batch 2.4 not fully done.)
- 🟠 **ShareDialog missing controls** (`ShareDialog.tsx`, backend supports them): no **password entry**, no **welcome-message** setter, and **readonly vs view** are merged into one (spec has 3 link kinds). `PasswordGate` consumes a password that the UI can never set.
- 🟠 **Cross-board move: board-card drop missing** (`ElementShell.tsx:124-138`) — only breadcrumb-crumb drop works; dropping a selection onto a board *card* (GAPS 2.6) isn't handled.
- 🟡 **Clone footer never lists sibling boards** — `api.cloneInstances` is defined (`client.ts:90`) but **never called** (`NoteCard.tsx:141`).
- 🟡 **Comment pin-to-card missing** (`cards.tsx:395-507`) — no `pinnedToId` drag/follow marker.
- 🟡 **Note→Document convert** helper: absent. **Line Shift-constrain** to H/V: absent (`LineLayer.tsx` draw path).
- 🟡 **Formula engine is hand-rolled, not HyperFormula** (`lib/formula.ts`) — supports `+-*/`, refs, ranges, `SUM/AVG/MIN/MAX/COUNT`, circular detection; narrower than the spec's HyperFormula. Table cell **types** (currency/percentage/date/checkbox) are only partly rendered (numeric auto-detect only).
- 🟡 **Labels manager**: create/assign only; no rename/recolor/delete.
- 🔵 **Export PNG/PDF/ZIP: none wired** (GAPS 5.3) — only markdown/text/json in the topbar. No `html-to-image`/`jsPDF`/`JSZip` in deps.
- 🔵 **Plan usage-meter UI: none** (`SettingsDialog.tsx:240` shows plan name only).
- 🔵 **IMAGE ignores `thumbUrl`** (`cards.tsx:208-231`) — always loads full-size originals.
- 🔵 **Accessibility: minimal** — icon buttons rely on `title=` (no `aria-label`); modals have no `role=dialog`/focus trap; ContextMenu/popovers are mouse-only (no arrow-key nav); canvas not keyboard-operable.
- 🔵 **Global RTL chrome not mirrored** — per-content bidi is excellent, but `documentElement.dir` is never set to `rtl`; the app shell stays LTR in Arabic (by design per `i18n.ts:2-4`, but a gap for an Arabic-first product).
- 🔵 **Mobile/responsive: none** (deferred per §7) — no media queries; pointer handlers give partial touch only.

---

## 4. Infrastructure, tests & production readiness ⚪

This is the weakest area versus the "production-grade" goal. `GAPS_PLAN` Batch 6 is largely **not delivered** despite being written as the plan.

**Tests — thin.** Only **2 Go test files** (`transaction_service_test.go` — IDOR/partial-write/trash-cascade; `settings_test.go`) and **2 frontend tests** (`direction`, `formula`). A good in-memory repo fake exists (`repository/memory/memory.go`). **Untested:** ShareService and AccessResolver (both promised in GAPS 1.8/6.1), and the entire realtime hub, upload, export, board, link, comment, account, auth, mongo-adapter, and storage layers. No component/store tests, no integration or e2e, no coverage gate, no `-race`.

**CI** (`.github/workflows/ci.yml`) runs `go vet`/`go test`/`go build` + `tsc`/`vitest`/`vite build`. **Missing:** golangci-lint (no config), frontend ESLint, `docker compose config` (explicitly promised), docker image build, coverage, `-race`, and security scanning (govulncheck/npm audit/Trivy). `--passWithNoTests` lets frontend test loss pass silently.

**Data integrity / ACID.** 🔴 for the goal: Mongo is `mongo:7` **standalone, unauthenticated, port exposed**, **no replica set** → no multi-doc ACID, contradicting the transaction model. **No backup/restore tooling** of any kind (named volumes only). `migrate` is idempotent maintenance, not a versioned migration system.

**Secrets & hardening.** Insecure defaults shipped everywhere: Keycloak `admin/admin` (`docker-compose.yml:42-43`), API client secret `qomranote-api-secret-change-me` in **both** compose and `realm-export.json:46`, `demo/demo1234` baked into the realm, **no Keycloak password policy**, Keycloak runs **`start-dev`** not `start`. Redirect URIs are localhost-only. `.env` is correctly gitignored (no real secret leak) but `.env.example` ships the insecure default as the working value.

**TLS / headers.** No TLS anywhere; no reverse-proxy TLS profile. nginx has gzip + some security headers but **only on `/` and `/assets/`** — none on `/api/` or `/ws`, and **no CSP anywhere** (all promised in 6.2). Web (nginx) container runs as **root** with no healthcheck; body-limit mismatch (nginx 128m vs API 64M).

**Observability.** No `/metrics` (Prometheus promised, absent); request ID is generated (`server.go:35`) but **not in the logs**, and HTTP access logs are Debug-level (off in prod); no Sentry/error hook. Rate limiter is a **single global** in-memory bucket, not the promised per-route limiters (and won't work across replicas). `/readyz` genuinely pings Mongo+Keycloak (good).

**Production readiness misc.** `validator/v10` **not in the project** (all validation hand-rolled) despite PLAN §3.2 listing it; **no cursor pagination** (fixed caps only); **no OpenAPI/`/api/docs`**; **no plan enforcement** (`User.Plan` exists but is never read; no 402 anywhere; no 100-item/10-file caps). Scheduled trash-purge + attachment-GC **is** wired into `serve` (good, 6.4 met). **`deploy/` directory is empty** — no production deployment artifact of any kind exists.

**Reminder delivery.** Notification records are created and preferences honored in-app, but **no SMTP/email is ever sent** (deferred per §7 — consistent, extension point noted).

---

## 5. Doc drift (the plan oversells the code)

`GAPS_PLAN.md` is written as accomplished fact but several items are aspirational:
- **6.2** claims Mongo replica-set ACID, Keycloak `start`, TLS profile, CSP — none present.
- **6.3** claims `/metrics`, request-IDs-in-logs, Sentry hook, per-route rate limits — none/partial.
- **6.1** claims AccessResolver/ShareService tests + `docker compose config` in CI — none.
- **1.5** claims attachment linking + full GC — only presigned-sweep half shipped.

---

## 6. Suggested priority order

**Now (security):**
1. 🔴 CLONE read-ACL check (§0) — one function, add a test.
2. 🟠 SSRF dial-the-validated-IP; notification/mention/assignee board-access checks; realtime send-on-closed-channel guard.
3. 🟠 Attachment link-verify + collect uploaded-orphans; reparent cycle check; `isHome`/`isTemplate` moved out of client-patchable content.

**Next (correctness & core UX):** wire search `boardId`/`sort` (+ decide `$text` vs regex); ShareDialog password/welcome/3-kinds; last `window.prompt`; board-card drop; clone footer.

**Then (hardening for "production-grade"):** Mongo replica set + auth (unlocks real ACID) and backups; rotate all default secrets + Keycloak `start` + password policy; CSP + TLS profile + nginx headers on all routes; ShareService/AccessResolver/hub tests + golangci-lint + docker-build in CI; `/metrics` + request-IDs-in-logs; plan enforcement; populate `deploy/`.

**Feature depth (as scoped):** PNG/PDF/ZIP export, comment pin, note→document, line Shift-constrain, labels manager, table cell types, accessibility pass, global RTL chrome.
