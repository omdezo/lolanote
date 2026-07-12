# QomraNote — Master Implementation Plan

> A production-grade Milanote clone, engineered from the ground up using the findings of the
> **Milanote Deep Research Report** (July 2026). Every architectural decision below is traceable
> to a section of that report (cited as §x.y).

---

## 0. Vision & Non-Negotiables

QomraNote is a **visual, board-based workspace for creative work**: everything is a draggable
card (element) on a freeform, infinitely-nestable canvas. The clone reproduces Milanote's
*mechanisms*, not just its looks:

| Non-negotiable | Source |
|---|---|
| Go backend (Echo + Zap + Cobra) | user requirement |
| MongoDB document store, 24-hex ObjectIds | §9.4 — Milanote validates `^[0-9a-fA-F]{24}$` |
| Everything is a typed **element** with `location.parentId` containment | §9.4 |
| Every mutation is a **transaction** `{id, changes, undoChanges}` → powers undo/redo AND realtime broadcast | §9.5 |
| Element-granular realtime sync over WebSockets — **deliberately no CRDT/OT** | §9.9 |
| Presigned direct-to-storage uploads (Cloudflare R2, S3-compatible) — bytes never transit the API | §9.10 |
| Keycloak OIDC auth (replaces Milanote's email/Google/Apple auth) with first-login bootstrap | user requirement + §3.1 |
| Docker + docker-compose + Makefile | user requirement |
| TypeScript React SPA, Tiptap rich text, GSAP animation | §9.3, §9.7 + user requirement |

---

## 1. System Architecture (three-tier, mirrors §9.2)

```
┌─────────────────────────────────────────────────────────────────────┐
│ CLIENT TIER                                                         │
│  React 18 + TypeScript SPA (Vite)                                   │
│  Zustand normalized element store  ·  Tiptap editor  ·  GSAP        │
│  keycloak-js (OIDC PKCE)  ·  native WebSocket client                │
└──────────────┬───────────────────────────────┬──────────────────────┘
               │ JSON REST /api/v1             │ WS /ws?board=<id>
┌──────────────▼───────────────────────────────▼──────────────────────┐
│ API / SYNC TIER — Go 1.26                                           │
│  Echo v4 HTTP server · Zap structured logging · Cobra CLI           │
│  OIDC middleware (Keycloak JWKS verify) · gocloak admin client      │
│  Realtime Hub: per-board rooms, transaction broadcast, presence     │
│  Services (business logic) → Repositories (interfaces) → Mongo      │
│  R2 presigner (aws-sdk-go-v2, custom endpoint)                      │
└──────┬──────────────────┬──────────────────────────┬────────────────┘
       │                  │                          │
┌──────▼──────┐  ┌────────▼────────┐  ┌──────────────▼───────────────┐
│  MongoDB 7  │  │ Keycloak 26     │  │ Cloudflare R2 (S3 API)       │
│  (local)    │  │ realm: qomranote│  │ presigned PUT/GET, creds in  │
│             │  │ + Postgres      │  │ .env (placeholders for now)  │
└─────────────┘  └─────────────────┘  └──────────────────────────────┘
```

**Request path (write):** client applies change optimistically → POSTs a *transaction* →
service validates ACL → applies to Mongo → appends to `transactions` collection →
hub broadcasts to every other socket in the board room → remote clients apply the same
`changes` payload their own dispatch would have produced (§9.9 "one code path for local
and remote mutations").

---

## 2. Domain Model (mirrors §9.4, Appendix A)

### 2.1 Element — the single core abstraction

Every visible thing is an element. **19 types**, identical to Milanote's closed set:

`BOARD, ALIAS, COLUMN, CARD, LINK, LINE, IMAGE, FILE, COMMENT_THREAD, TASK_LIST, TASK,
CLONE, SKETCH, ANNOTATION, COLOR_SWATCH, DOCUMENT, TABLE, SKELETON, UNKNOWN`

(`SKELETON` = loading placeholder rendered client-side; `UNKNOWN` = forward-compatibility
fallback — old clients render unrecognized types gracefully, §9.4.)

```jsonc
// collection: elements
{
  "_id": ObjectId,             // 24-hex — the client validates ^[0-9a-fA-F]{24}$
  "type": "CARD",
  "location": {
    "parentId": ObjectId,      // board / column / task_list that owns it
    "section": "CANVAS",       // CANVAS | UNSORTED | TRASH  (§3.3, §3.4)
    "position": { "x": 120.5, "y": 340.0 },  // canvas coordinates
    "index": 3,                // ordering inside COLUMN / TASK_LIST / UNSORTED
    "width": 300, "height": 0  // 0 = auto height
  },
  "content": { ... },          // typed payload, varies by element type (see 2.2)
  "acl": {                     // only meaningful on BOARD elements; cascades (§6.1)
    "ownerId": "keycloak-sub",
    "editors": ["sub", ...],
    "publicEditLink": null | "token",
    "publicViewLink": null | { "token", "allowFeedback", "password?" }
  },
  "createdBy": "keycloak-sub",
  "createdAt": ISODate, "updatedAt": ISODate,
  "deletedAt": null | ISODate, // Trash: soft delete, 3-month retention (§3.4)
  "deletedBy": null | "sub"
}
```

**Core query** (§9.4): *"give me all children of this parent"* → compound index
`{ "location.parentId": 1, "deletedAt": 1 }`.

### 2.2 Content payloads per type

| Type | content payload |
|---|---|
| BOARD | `{ title, icon, color, isTemplate }` |
| CARD (note) | `{ doc: <Tiptap JSON>, backgroundColor, textPreview }` |
| DOCUMENT | `{ doc, title, cardView }` — full-width writing view (§4.2) |
| IMAGE | `{ attachmentId, url, thumbUrl, caption, naturalW/H, rotation, colorStrip[] }` |
| FILE | `{ attachmentId, url, filename, mimeType, size }` |
| LINK | `{ url, title, description, thumbnailUrl, showPreview, showDescription, embedType }` (§4.4 server-side metadata fetch) |
| LINE | `{ fromId, toId, fromPoint?, toPoint?, curve, color, weight, startArrow, endArrow, label }` (§4.12) |
| COLUMN | `{ title, collapsed }` — children ordered by `location.index` (§4.9) |
| TASK_LIST | `{ title }` |
| TASK | `{ text, done, dueDate?, reminderAt?, assigneeId?, indent }` (§4.11) |
| COLOR_SWATCH | `{ hex, displayFormat: HEX\|RGB\|HSL\|NONE }` (§4.14) |
| SKETCH | `{ strokes: [{points,color,width}], background }` — SVG strokes (§4.13) |
| ANNOTATION | `{ targetImageId, strokes }` — scales with image (§4.13) |
| COMMENT_THREAD | `{ pinnedToId?, resolved }` — messages live in `comments` collection (§4.17) |
| ALIAS | `{ targetBoardId }` — board shortcut (§4.16) |
| CLONE | `{ cloneSourceId }` — synced note; all instances point at one CARD's content (§4.15) |
| TABLE | `{ cells: [[{v,type,formula?}]], colWidths[] }` (§4.10 — formulas evaluated client-side, HyperFormula-style) |

### 2.3 Transactions (mirrors §9.5)

```jsonc
// collection: transactions  (history / audit / undo source of truth)
{
  "_id": ObjectId,
  "boardId": ObjectId,            // room key for broadcast
  "userId": "keycloak-sub",
  "ops": [{
      "elementId": ObjectId,
      "action": "create|update|move|delete|restore",
      "changes":     { ... },     // forward patch (deep-merge semantics)
      "undoChanges": { ... }      // precomputed inverse — client replays for undo
  }],
  "clientId": "uuid",             // originating socket — excluded from echo broadcast
  "createdAt": ISODate
}
```

One multi-select drag = **one** transaction with many ops (`ELEMENT_MOVE_MULTI`, §9.5).

### 2.4 Other collections

- `users` — mirror of Keycloak identity + app data: `{ _id, keycloakSub (unique), email, displayName, avatarUrl, homeBoardId, plan, createdAt }`. Created lazily on first authenticated request (bootstrap creates the private **Home board**, §3.1 — never shareable, never exportable).
- `comments` — `{ _id, threadId, authorId, doc, reactions: {emoji:[subs]}, createdAt, editedAt }`. Only authors edit their own; no removal from thread once posted (§4.17).
- `labels` — `{ _id, ownerId, name, color, usageCount }` + element `labelIds` array (§4.18).
- `share_links` — token-indexed lookup for read-only/view links incl. password hash + welcome message (§6.1).
- `notifications` — `{ userId, kind: mention|assignment|comment|boardChange, boardId, elementId, read }` (§6.2).
- `attachments` — upload registry: `{ _id, ownerId, key, bucket, filename, contentType, size, status: presigned|uploaded }` (§9.10).

### 2.5 Mongo indexes (created by `qomranote migrate`)

```
elements:      { location.parentId: 1, deletedAt: 1 }      // board load
elements:      { type: 1, "content.cloneSourceId": 1 }      // clone fan-out
elements:      { "acl.ownerId": 1, type: 1 }                // "my boards"
elements:      { deletedBy: 1, deletedAt: 1 }               // trash view
elements:      text index on content.textPreview/title      // global search (§3.5)
transactions:  { boardId: 1, createdAt: -1 }
users:         { keycloakSub: 1 } unique
share_links:   { token: 1 } unique
comments:      { threadId: 1, createdAt: 1 }
attachments:   { ownerId: 1, createdAt: -1 }
```

---

## 3. Backend — Go, OOP structure

### 3.1 Package layout (clean architecture, dependency-inverted)

```
backend/
├── cmd/qomranote/main.go        → cli.Execute()
├── internal/
│   ├── cli/                     Cobra commands: serve, migrate, seed, version
│   ├── config/                  Viper + env config struct (single source of truth)
│   ├── logger/                  Zap constructor (dev/prod encoders)
│   ├── domain/                  PURE domain layer — no mongo/echo imports
│   │   ├── element.go           Element, ElementType, Location, ContentFor(type)
│   │   ├── transaction.go       Transaction, Op, Action
│   │   ├── user.go / comment.go / label.go / sharelink.go / attachment.go / notification.go
│   │   ├── errors.go            typed sentinel errors (ErrNotFound, ErrForbidden…)
│   │   └── repository.go        ALL repository interfaces (ports)
│   ├── repository/mongo/        adapters implementing domain interfaces
│   ├── service/                 business logic — constructor-injected repos (OOP)
│   │   ├── element_service.go   create/update/move/multi-move/trash/restore/clone
│   │   ├── board_service.go     children fetch, breadcrumbs, ACL cascade (§3.2, §6.1)
│   │   ├── transaction_service.go  validate → apply → persist → broadcast
│   │   ├── user_service.go      first-login bootstrap (Home board) (§3.1)
│   │   ├── share_service.go     4 sharing mechanisms (§6.1)
│   │   ├── search_service.go    board-scoped + global search (§3.5)
│   │   ├── upload_service.go    presign flow (§9.10)
│   │   ├── link_service.go      server-side URL metadata fetch (§4.4)
│   │   ├── comment_service.go / label_service.go / trash_service.go
│   ├── auth/                    Keycloak: OIDC JWKS verification middleware + gocloak
│   ├── realtime/                Hub, Room, Client — gorilla/websocket
│   ├── storage/                 R2Presigner (aws-sdk-go-v2 S3, custom endpoint)
│   └── transport/http/          Echo server, router, middleware, handlers/ (thin)
```

**OOP discipline:** every service is a struct with private fields + `NewXxxService(deps…)`
constructor; every dependency is an interface declared in `domain/repository.go`;
handlers depend on services, services on repository interfaces, repositories on Mongo.
Nothing imports upward. This is Go's idiomatic equivalent of class-based OOP with DI.

### 3.2 The libraries ("perfection packages")

| Package | Role |
|---|---|
| `github.com/labstack/echo/v4` | HTTP framework (user-required) |
| `go.uber.org/zap` | structured logging (user-required) |
| `github.com/spf13/cobra` + `viper` | CLI + config (user-required) |
| `go.mongodb.org/mongo-driver` | MongoDB official driver |
| `github.com/coreos/go-oidc/v3` | Keycloak token verification via JWKS |
| `github.com/Nerzal/gocloak/v13` | Keycloak Admin API (user lookup, service accounts) |
| `github.com/gorilla/websocket` | realtime transport (§9.9) |
| `github.com/aws/aws-sdk-go-v2` (config/credentials/s3) | Cloudflare R2 presigned URLs (§9.10) |
| `github.com/go-playground/validator/v10` | request DTO validation |
| `github.com/joho/godotenv` | .env loading in dev |
| `golang.org/x/net/html` | link-card metadata scraping (§4.4) |

### 3.3 REST API surface (`/api/v1`, JSON; full spec)

```
Auth / bootstrap
  GET  /api/v1/me                       → bootstrap user (creates Home board on first call)
  GET  /api/v1/users/lookup?email=      → collaborator search (via gocloak)

Boards & elements
  GET  /api/v1/boards/:id               → board element + ACL + breadcrumb path (§3.2)
  GET  /api/v1/boards/:id/children      → all live child elements (+descendants of columns)
  GET  /api/v1/boards/:id/unsorted      → unsorted-tray elements (§3.3)
  POST /api/v1/elements                 → create element (server assigns ObjectId)
  GET  /api/v1/elements/:id
  PATCH /api/v1/elements/:id            → deep-merge content/location update
  POST /api/v1/elements/:id/duplicate   → deep copy (columns copy children) (§5)
  POST /api/v1/elements/:id/clone       → convert to synced-note CLONE pair (§4.15)
  GET  /api/v1/elements/:id/clones      → sibling boards list for footer (§4.15)

Transactions (the write path — §9.5)
  POST /api/v1/transactions             → {boardId, clientId, ops[]} → apply + broadcast
  GET  /api/v1/boards/:id/transactions  → history page (audit)

Trash (§3.4)
  GET    /api/v1/trash                  → mine, split deletedBy me / others
  POST   /api/v1/trash/:id/restore
  DELETE /api/v1/trash                  → empty trash (irreversible)
  DELETE /api/v1/trash/:id              → permanent single delete

Uploads (§9.10 presign flow)
  POST /api/v1/attachments/presign      → {filename,contentType,fileSize} → {attachmentId, presignedUrl, publicUrl}
  POST /api/v1/attachments/:id/complete → mark uploaded, patch owning element

Links (§4.4)
  POST /api/v1/links/resolve            → {url} → {title, description, thumbnail, embedType}

Sharing (§6.1 — all four mechanisms)
  GET    /api/v1/boards/:id/share
  POST   /api/v1/boards/:id/share/editors        {email}
  DELETE /api/v1/boards/:id/share/editors/:sub
  POST   /api/v1/boards/:id/share/link           {kind: edit|readonly|view, allowFeedback, password?, welcomeMessage?}
  DELETE /api/v1/boards/:id/share/link/:kind
  GET    /api/v1/shared/:token                    → public resolve (view links need no auth)

Search (§3.5)
  GET /api/v1/search?q=&boardId=&sort=viewed|modified

Comments (§4.17)
  POST /api/v1/threads/:id/comments
  PATCH /api/v1/comments/:id             (author-only)
  POST /api/v1/comments/:id/reactions    {emoji}

Labels (§4.18)
  GET/POST /api/v1/labels · PATCH/DELETE /api/v1/labels/:id
  POST /api/v1/elements/:id/labels       {labelId}   · DELETE …/labels/:labelId

Notifications (§6.2)
  GET /api/v1/notifications · POST /api/v1/notifications/read

Export (§7.2)
  GET /api/v1/boards/:id/export?format=markdown|text|json   (linearized)

System
  GET /healthz · GET /readyz             (compose healthchecks)
```

**Conventions:** camelCase JSON; RFC-7807-style error envelope
`{"error":{"code","message","details"}}`; cursor pagination where lists can grow;
21-second client timeout honored (§9.10); request-ID + Zap request logging middleware;
CORS locked to the frontend origin.

### 3.4 Realtime protocol (`/ws?board=<id>&token=<jwt>` — §9.9)

Envelope: `{"event": string, "data": object}`.

Server→client events:
- `transaction.applied` — the ops of a committed transaction (skips originating `clientId`)
- `presence.state` / `presence.join` / `presence.leave` — who is viewing the board
- `element.editing` — remote "is editing" indicators (§9.9)
- `pong`

Client→server: `subscribe`, `editing {elementId, on}`, `presence.cursor` (throttled — the
SOCKET_THROTTLE analog for continuous drags, §9.9), `ping`.

Concurrency model is **element-granular last-writer-wins**, exactly like Milanote (§9.9):
no Yjs/Automerge/OT anywhere; two users on different cards merge trivially; same card
resolves server-authoritatively.

### 3.5 Keycloak integration

- Realm `qomranote` auto-imported at compose-up (`keycloak/realm-export.json`).
- Clients: `qomranote-web` (public, PKCE S256, redirect `http://localhost:5173/*`) and
  `qomranote-api` (confidential service account with `view-users` role for email→user lookup).
- Backend middleware: `go-oidc` verifier against the realm JWKS (issuer configurable for
  the docker-internal hostname split), extracts `sub/email/name` into request context.
- First authenticated `/me` call creates the `users` row + private Home board (§3.1).
- Dev realm ships a test user: `demo / demo1234`.

### 3.6 Cloudflare R2 (object storage)

R2 is S3-API-compatible → `aws-sdk-go-v2` with `BaseEndpoint =
https://<ACCOUNT_ID>.r2.cloudflarestorage.com`, region `auto`. The presign flow is
Milanote's exactly (§9.10): client asks `POST /attachments/presign` → API returns a
15-minute presigned PUT → browser PUTs bytes straight to R2 → client confirms
`/complete`. Keys: `u/<userId>/<attachmentId>/<sanitized-filename>`.
Credentials are **placeholders in `.env`** until the user supplies them; a
`STORAGE_DRIVER=local` fallback stores files on disk through the API so the app is fully
usable before R2 credentials arrive.

---

## 4. Frontend — TypeScript React SPA

### 4.1 Stack

Vite + React 18 + TypeScript (strict) · Zustand (normalized element store — the Redux/
Immutable analog of §9.3 with less ceremony) · **GSAP** (canvas zoom tweens, tray/panel
slide-ins, card drop animations) · **Tiptap** (§9.7 — same editor Milanote uses) ·
keycloak-js (OIDC PKCE) · native WebSocket client with reconnect/backoff ·
`@use-gesture/react`-style custom pointer handlers for drag (kept dependency-light).

### 4.2 Store shape (normalized, mirrors §9.3/§9.6)

```ts
elements: Record<Id, El>            // the normalized element graph
childrenByParent: Record<Id, Id[]>  // derived index = Mongo's core query
selection: Set<Id>
undoStack / redoStack: Txn[]        // §9.5 — replay undoChanges
board: { id, breadcrumb[], acl }
presence: Record<clientId, {user, cursor}>
```

Every local mutation goes through `commitTransaction(ops)`: apply optimistically →
push inverse onto undoStack → POST /transactions → WS echoes to others. Remote
`transaction.applied` events run through the *same* `applyOps` reducer (§9.9's
"one code path").

### 4.3 Feature build-out (all mapped to research)

| Feature | Mechanism |
|---|---|
| Infinite canvas | transformed layer `translate(pan) scale(zoom)`; Ctrl+wheel zoom, `Z` fit-all (§3.5), GSAP-tweened |
| Notes (CARD) | Tiptap: headings, small text, code/quote block, colors, lists w/ Tab indent, shortcuts (§4.1) |
| Boards + nesting + breadcrumbs | BOARD cards open on double-click; breadcrumb drag-to-move-up (§3.2) |
| Unsorted tray | slide-out right column; quick-capture Ctrl/⌘+Enter targets it (§3.3) |
| Trash | per-account view, deleted-by-me/others, restore by action, empty-trash (§3.4) |
| Columns | vertical containers, drag in/out + reorder by index, collapse, count badge, "Group into Column" on multi-select (§4.9) |
| Images | drag-drop upload → presign → R2; captions, resize (§4.3) |
| Links | paste URL → server metadata fetch → preview card; board URLs become ALIAS (§4.4) |
| Lines | drag from corner circle of selected card; curved via center handle; arrowheads, color, label; follow their cards (§4.12) |
| To-dos | TASK_LIST + TASK, checkbox, indent, due dates (§4.11) |
| Color swatches | hex entry + picker, HEX/RGB/HSL display (§4.14) |
| Sketch | SVG stroke capture drawing editor (§4.13) |
| Comments | floating COMMENT_THREAD cards with reply threads (§4.17) |
| Multi-select | click-drag marquee, shift-click; one multi-move transaction (§9.5) |
| Undo/redo | Ctrl+Z / Ctrl+Shift+Z replaying undoChanges (§9.5) |
| Search | Ctrl+F overlay, current board + everywhere (§3.5) |
| Sharing dialog | editors by email, edit/readonly/view links (§6.1) |
| Realtime presence | avatars + live cursors + remote-editing outline (§9.9) |
| Keyboard map | Mousetrap-style global shortcut layer (§5) |

### 4.4 Design language

Milanote-adjacent but original: near-white warm canvas with subtle dot-grid, dark ink
sidebar/topbar, cards with soft shadows + 2px radius, drag ghosts at 0.6 opacity,
accent color `#6c5ce7` (Qomra purple), GSAP micro-interactions (≤200 ms, ease `power3.out`).

---

## 5. Infrastructure

### 5.1 docker-compose services

| Service | Image | Notes |
|---|---|---|
| `mongo` | mongo:7 | volume-backed, healthcheck `mongosh ping` |
| `keycloak-db` | postgres:16-alpine | Keycloak's store |
| `keycloak` | quay.io/keycloak/keycloak:26.0 | `start-dev --import-realm`, realm auto-imported |
| `api` | multi-stage Go build (distroless-style slim) | depends_on healthy mongo+keycloak |
| `web` | multi-stage Node build → nginx:alpine | SPA + `/api` `/ws` reverse proxy |

### 5.2 Makefile targets

`make up / down / logs / build / rebuild` (compose) · `make dev-api / dev-web` (hot local dev)
· `make tidy / test / vet / lint` (Go) · `make migrate / seed` (CLI passthrough)
· `make typecheck` (frontend) · `make clean`.

### 5.3 Configuration

Single `.env` at repo root consumed by compose AND the API (godotenv in dev).
`.env.example` documents every variable. Cloudflare R2 block ships as placeholders:

```
STORAGE_DRIVER=local            # flip to r2 when credentials arrive
R2_ACCOUNT_ID=__FILL_ME__
R2_ACCESS_KEY_ID=__FILL_ME__
R2_SECRET_ACCESS_KEY=__FILL_ME__
R2_BUCKET=qomranote
R2_PUBLIC_BASE_URL=             # optional public bucket/custom domain
```

---

## 6. Execution Order

1. **PLAN.md** (this file) ✅
2. Backend skeleton: config → logger → domain → repositories → services → auth → realtime → handlers → CLI. Compile-verify with `go build ./... && go vet ./...`.
3. Frontend: API/auth/store/realtime core → canvas engine → element components → panels (unsorted/trash/search/share) → polish. Compile-verify with `tsc && vite build`.
4. Keycloak realm export, Dockerfiles, docker-compose, Makefile, .env, README.
5. End-to-end smoke: `make up`, bootstrap user, create/move/edit elements in two browser tabs, verify realtime + undo + trash + search.

## 7. Deliberate scope decisions (v1)

Matching Milanote's own restraint (§2.3) and its documented gaps (§10):

- **In v1, fully working:** boards/nesting/breadcrumbs, notes, documents, columns, images
  (upload+presign), links w/ metadata, lines, tasks, swatches, sketches, comments,
  clones (synced notes), aliases, labels, trash, unsorted, search, sharing (editors +
  links), realtime sync + presence, undo/redo, export (markdown/text/json), templates flag.
- **Deferred (like Milanote defers):** character-level co-editing (never — by design §9.9),
  mobile shells, web clipper, PDF/PNG spatial export, table formula engine (table cells
  editable, formula evaluation stubbed client-side), email/push reminder delivery
  (notification records are created; SMTP wiring later), Pexels stock search (needs API key).

Every deferred item has its extension point already in the schema/API.
