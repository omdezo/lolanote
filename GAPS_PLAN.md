# QomraNote — Gap Remediation Plan

For every gap identified in the system audit: a short **best-practice** note (what the
industry does and why) and the **QomraNote approach** (exactly how it lands in this
codebase). Ordered by the six execution batches. Each item is sized so it can be built
and verified independently.

Legend: 🔴 correctness/security · 🟠 core UX · 🟡 feature depth · 🔵 platform · ⚪ production

---

## BATCH 1 — Security & correctness 🔴

### 1.1 Transaction ops must be verified against the board subtree
**Best practice.** Never trust a client-declared scope. Authorization must be checked on
the *object being mutated*, not on a sibling identifier the client also sent. This is
IDOR (Insecure Direct Object Reference) prevention — OWASP A01.
**Approach.** In `TransactionService.Apply`, after resolving edit rights on `boardId`,
resolve each op's element and confirm its nearest-ancestor board equals `boardId` (reuse
`AccessResolver.Resolve`, which already returns the nearest board). For `create` ops,
validate the *parent* resolves to `boardId`. Cache board resolutions within the call to
avoid N extra walks. Reject the whole transaction on any mismatch (atomicity of intent).

### 1.2 Anonymous & password-protected view links
**Best practice.** Public share links should render without forcing an account (Notion,
Figma "anyone with the link"). Auth should be *optional* on the read path; the token is
the capability.
**Approach.** Frontend: switch Keycloak init to `onLoad: 'check-sso'` and only force
login when there's no `?share=` token. Add a public board-view mode (read-only canvas,
no rail) driven by `GET /shared/:token`. Add a password gate component that posts the
password via `X-Share-Password`. Backend already resolves tokens and checks the bcrypt
hash — wire the 401→prompt loop on the client.

### 1.3 Resync on WebSocket reconnect
**Best practice.** Socket-synced apps must reconcile after any gap (Figma, Linear). Two
options: (a) full refetch on reconnect (simple, correct), (b) sequence-number replay
(efficient). Milanote itself refetches the current board on wake (§9.6
`APP_START_CURRENT_BOARD_REFRESH`).
**Approach.** Adopt (a) now. The socket client already reconnects with backoff; on the
`onopen` *after* a prior disconnect (not the first connect), call
`useBoard.getState().refreshBoard()`. Track a `hadDisconnect` flag. Cheap, race-free,
matches Milanote's own model.

### 1.4 Trash cascade — hide descendants of trashed boards
**Best practice.** Soft-delete must cascade logically so deleted containers don't leak
children into queries. Either mark descendants or filter by an ancestor-deleted check.
**Approach.** When soft-deleting a container element, also stamp `deletedAt`/`deletedBy`
on its live descendants (bulk update) in `ElementService` delete paths and the delete op.
Restore reverses only those the same operation trashed — track with a
`trashBatchId` field so restoring a board doesn't resurrect items deleted earlier.

### 1.5 Attachment ownership + garbage collection
**Best practice.** Bind uploaded objects to their owner; verify ownership when referenced;
sweep orphans on a schedule. Direct-to-storage patterns (S3 presigned) always pair upload
with a "confirm + link" step and a lifecycle rule for unclaimed objects.
**Approach.** `complete` already checks `ownerId == caller`. Add: (a) on element
create/update, if content references an `attachmentId`, verify the attachment is owned by
the caller and mark it `linked`; (b) a `qomranote gc` CLI command + scheduled sweep that
deletes `presigned`/`uploaded`-but-unlinked attachments older than 24h from both Mongo and
storage.

### 1.6 WebSocket token out of the query string
**Best practice.** Tokens in URLs leak into logs, referrers, and history. Use a
short-lived one-time ticket exchanged over the authenticated REST channel, or a
`Sec-WebSocket-Protocol` bearer.
**Approach.** Add `POST /api/v1/realtime/ticket` → returns a 30-second single-use ticket
(random token stored in an in-process TTL map keyed to the principal). The WS handshake
takes `?ticket=` instead of `?token=`; the handler redeems it. Falls back cleanly and
keeps the browser-can't-set-WS-headers constraint satisfied.

### 1.7 Multi-op atomicity
**Best practice.** A transaction should be all-or-nothing. MongoDB supports multi-document
ACID transactions **only on replica sets**.
**Approach.** Convert the compose `mongo` service to a single-node replica set
(`--replSet rs0` + an init one-shot). Wrap `TransactionService.Apply`'s op loop in a
Mongo session `WithTransaction`. Until the replica set lands (Batch 6), keep the
current best-effort loop but pre-validate *all* ops (ACL + existence) before applying any,
so failures are caught before mutation — a partial-write guard.

### 1.8 Backend tests for the security-critical paths
**Best practice.** Security fixes without regression tests rot. Table-driven Go tests with
an in-memory repo fake.
**Approach.** Introduce `repository/memory` fakes implementing the domain interfaces, and
`*_test.go` for `AccessResolver`, `TransactionService` (the IDOR case), and
`ShareService`. Run in CI (Batch 6). This is the seed of the test suite.

---

## BATCH 2 — Daily-use UX 🟠

### 2.1 Drag cards out of / reorder within columns
**Best practice.** Kanban DnD uses fractional indexing (Figma, Trello) so a reorder is a
single write, never a renumber. Drop position derived from pointer-vs-midpoint of siblings.
**Approach.** Remove the `inColumn` drag lock. In `ElementShell`'s drop handler, compute
the target: if released over a column body, insert at the sibling gap under the pointer and
assign `index = midpoint(prev, next)`; if released over open canvas, reparent to the board
with a canvas position. Reuse the existing fractional `location.index`.

### 2.2 Right-click context menu
**Best practice.** A single reusable menu component positioned at the pointer, closed on
outside-click/Escape, actions contextual to selection. Never the native menu.
**Approach.** New `ContextMenu` component + `useContextMenu` store slice. Right-click an
element opens: Edit, Duplicate (⌘D), Synced copy, Lock/Unlock, Group into Column, Create
shortcut, Add label, Delete. Right-click canvas: Paste, Select all, New note/board here.
Right-click a board card adds Convert to template.

### 2.3 Clipboard (copy / cut / paste)
**Best practice.** Support both internal clipboard (rich element copy) and the system
clipboard (paste image/URL/text). Use the async Clipboard API; fall back to an in-memory
buffer for internal element copy.
**Approach.** ⌘C/⌘X serialize the selection to an in-memory buffer (and a JSON blob to the
system clipboard). ⌘V: if system clipboard has an image → upload+place; a URL → link card;
QomraNote JSON → deep-clone via the duplicate service at the paste point; plain text →
note. Paste position tracks the last cursor location.

### 2.4 Replace every `window.prompt/confirm`
**Best practice.** Modal/popover input keeps focus, styling, validation, and cancel
semantics; native dialogs break flow and can't be styled.
**Approach.** A small `Prompt`/`Confirm` promise-based helper (`await prompt({title,
placeholder})`) rendered through a portal. Replace link/audio/map/video/line-label inputs
and the empty-trash confirm.

### 2.5 Toasts + error boundary
**Best practice.** Every async failure needs a visible, non-blocking signal; a top-level
error boundary prevents white-screens.
**Approach.** A `toast` store + `<Toaster>` (success/error/info, auto-dismiss). Route
`commitTransaction` rollbacks and API errors through it instead of silent
`console.error`+reload. Wrap `<App>` in an `ErrorBoundary` with a recover action.

### 2.6 Cross-board move (drag onto breadcrumb / board card)
**Approach.** Extend the drop handler: releasing a drag over a breadcrumb crumb or a board
card reparents the selection into that board's Unsorted (Milanote's rule, §5), with a toast
confirming the destination.

---

## BATCH 3 — Discovery 🟠

### 3.1 Notifications bell + inbox
**Best practice.** Poll or push; badge unread count; mark-read on open; deep-link to source.
**Approach.** Bell in the topbar with an unread badge (from `GET /notifications?unread=true`,
polled every 30s and pushed via a new `notification.new` socket event). Dropdown lists
items with actor name, message, relative time; clicking navigates to the board and marks
read.

### 3.2 "Shared with me" + boards list
**Approach.** A home-drawer / launcher panel listing `GET /boards` grouped into "My boards"
and "Shared with me" (owner vs editor). Board tiles reuse the canvas tile styling. This is
the missing entry point for invited collaborators.

### 3.3 Template picker
**Best practice.** Offer templates on empty boards and via a dedicated gallery; "use" stamps
a fresh editable copy.
**Approach.** `GET /templates` (already serving seeded system templates) drives a gallery
modal. "Use template" calls the duplicate service into the current board. Empty-board state
shows suggested templates. "Convert to template" sets `content.isTemplate` via the context
menu; a badge marks template boards.

### 3.4 Labels UI (backend already complete)
**Best practice.** Inline label chips on cards; a label manager; filter-by-label.
**Approach.** Label chips render on elements (color dot + name). A label popover
(create/pick/assign, auto-color) from the context menu and toolbar. A filter bar that dims
non-matching cards. All endpoints exist — this is pure frontend.

---

## BATCH 4 — Feature depth 🟡

### 4.1 Tasks: due dates, reminders, assignment
**Approach.** Extend TASK content: `dueDate`, `reminderAt`, `assigneeId`. UI: a date
popover and an assignee picker (reusing the collaborator lookup). Assignment writes a
notification (backend `NotifyAssignment` exists). Reminder *delivery* (email/push) is
deferred with the record created — documented as the SMTP-wiring extension point.

### 4.2 Table cell types + formula engine
**Best practice.** A spreadsheet grid needs a formula parser/evaluator with a dependency
graph; HyperFormula is the open-source standard (what Milanote uses).
**Approach.** Add `hyperformula` to the frontend. Cells gain `{v, type, formula}`; type
auto-detection (number/currency/percentage/date/checkbox); `=` opens a formula input;
HyperFormula evaluates and the grid shows results. Column resize + row/column drag-reorder
+ typed rendering.

### 4.3 Line controls
**Approach.** A line toolbar (on selection): color, weight, arrowhead ends, and a draggable
center handle to set `curve`; double-click straightens; Shift constrains to H/V while
drawing. All fields already exist in the LINE content model.

### 4.4 Comments: pin-to-card, mentions, live
**Approach.** Dragging a comment onto a card sets `pinnedToId` and renders a small marker
that follows the card. `@` in the input opens a mention picker (collaborator lookup) →
writes a mention notification. New comments broadcast over the socket. Author names resolve
via a users-batch lookup endpoint.

### 4.5 Clone live fan-out
**Approach.** When a CARD that has CLONE instances is edited, broadcast the update to every
board room holding an instance (server looks up instances by `cloneSourceId` and pushes a
targeted `element.updated`). The clone footer lists sibling boards from the existing API.

### 4.6 Notes formatting depth
**Approach.** Install `@tiptap/extension-link`, `-color`, `-highlight`, `-text-style`. Add
text color, highlight, inline link, ordered list, small-text, card background-color picker,
and Milanote's shortcut map. "Long note → convert to Document" helper.

---

## BATCH 5 — Platform 🔵

### 5.1 PWA (installable desktop app)
**Best practice.** Web manifest + service worker with an app-shell cache = installable PWA
(exactly Milanote's Windows "desktop app", §9.14).
**Approach.** `vite-plugin-pwa` with a manifest (name, icons, theme) and a Workbox service
worker caching the app shell + static assets. This delivers the documented install story.

### 5.2 Local cache (instant startup / offline read)
**Best practice.** Render from a local mirror first, then refresh from network (§9.6).
**Approach.** Mirror the element store to IndexedDB (`idb`) keyed by board; on `openBoard`,
hydrate from cache immediately, then reconcile with the network fetch. Enables the "renders
from cache first" fast-start behavior.

### 5.3 Export depth
**Approach.** Add JSON export (implement the already-routed format), PNG spatial snapshot
(render the canvas layer to canvas via `html-to-image`), PDF (jsPDF from the PNG), and
"Generate ZIP of files" (client zips attachment blobs). Per-document export reuses the doc
content.

### 5.4 Thumbnail pipeline
**Best practice.** Never render full-size originals; generate derivatives on upload. RAW/
TIF/PSD need server-side decoding.
**Approach.** On `complete`, enqueue a thumbnail job: for browser-safe images generate a
resized WebP; for RAW/PSD/TIF decode server-side (govips/imaging) to a preview. Store
`thumbUrl` on the attachment/element. (Sized as its own service; documented interface.)

### 5.5 Plan enforcement
**Approach.** Enforce the free-tier 100-item lifetime meter and 10-file cap server-side
(count on create, 402 when exceeded) and surface a usage meter + upgrade prompt in the UI.

---

## BATCH 6 — Production ⚪

### 6.1 Tests + CI
**Approach.** Go table tests (services, resolver, IDOR) with memory fakes; Vitest +
React Testing Library for store reducers and key components; a GitHub Actions workflow
running `go test`, `go vet`, `tsc`, `vite build`, and `docker compose config`.

### 6.2 Hardened infra
**Approach.** Mongo as an authenticated single-node replica set (enables ACID txns);
Keycloak `start` (not dev) with generated secrets via env; TLS-terminating reverse proxy
profile; per-route body limits; nginx gzip + security headers + CSP.

### 6.3 Observability + limits
**Approach.** Prometheus metrics middleware (`/metrics`), request tracing IDs already in
logs → structured export, Sentry-compatible error hook, and a token-bucket rate limiter on
auth/upload/transaction routes. `/readyz` actually pings Mongo + Keycloak.

### 6.4 API polish
**Approach.** `validator/v10` on all request DTOs; cursor pagination on list endpoints; an
OpenAPI spec generated/served at `/api/docs`; scheduled trash-purge + attachment-GC inside
`serve` (not just the manual CLI).

---

## Execution notes
- Each batch ends with `go build && go vet`, `tsc && vite build`, a container rebuild, and a
  browser smoke test of the new surface.
- Backend stays clean-architecture: new logic in services, new ports in `domain`, adapters
  in `repository/mongo`. Frontend stays store-driven with the transaction pipeline as the
  single write path.
- Deferred-with-extension-point items (reminder delivery, server thumbnail decode of RAW,
  full CRDT) are called out where they appear rather than silently skipped.
