# QomraNote

**Get organized. Stay creative.** A production-grade Milanote clone: a visual,
board-based workspace where everything — notes, images, links, files, tasks,
tables, sketches — is a draggable card on freeform, infinitely-nestable
canvases. Built from the mechanisms documented in the *Milanote Deep Research
Report* (see [PLAN.md](PLAN.md) for the full architecture, every decision
cited back to the research).

```
React 18 + TypeScript + Zustand + Tiptap + GSAP        (frontend)
Go 1.26 · Echo · Zap · Cobra · gorilla/websocket        (backend)
MongoDB 7 (elements + transactions)                     (data)
Keycloak 26 (OIDC PKCE, realm auto-imported)            (auth)
Cloudflare R2 via presigned URLs (local-disk fallback)  (files)
Docker Compose + Makefile                               (ops)
```

## Quickstart

```bash
cp .env.example .env       # already done on first checkout
make up                    # or: docker compose up -d --build
```

| Service  | URL                    | Credentials        |
|----------|------------------------|--------------------|
| Web app  | http://localhost:3000  | `demo` / `demo1234` (or register) |
| API      | http://localhost:8080  | Bearer tokens from Keycloak |
| Keycloak | http://localhost:8081  | `admin` / `admin` (dev only) |
| MongoDB  | localhost:27017        | —                  |

First login bootstraps your account and private **Home board** automatically.

Optional: `make seed` loads the built-in template board library.

### Local development (hot reload)

```bash
docker compose up -d mongo keycloak keycloak-db   # infra only
make dev-api    # Go API on :8080
make dev-web    # Vite dev server on :5173 (proxies /api and /ws)
```

## Core mechanics (mirroring Milanote's architecture)

- **Everything is an element** — 19 typed kinds (`BOARD, CARD, LINK, LINE,
  IMAGE, FILE, COLUMN, TASK_LIST, TASK, CLONE, SKETCH, COLOR_SWATCH,
  COMMENT_THREAD, DOCUMENT, TABLE, ALIAS, ANNOTATION, SKELETON, UNKNOWN`)
  identified by 24-hex ObjectIds, positioned by a `location.parentId`
  hierarchy. "All children of this parent" is *the* core query.
- **Every mutation is a transaction** carrying forward `changes` plus
  precomputed inverse `undoChanges` — one pattern powers optimistic UI,
  undo/redo (Ctrl+Z / Ctrl+Shift+Z), the audit trail, and realtime broadcast.
- **Element-granular realtime sync** over WebSockets — deliberately no
  CRDT/OT. Different cards merge trivially; the same card resolves
  last-writer-wins. Presence, live cursors, and remote-editing indicators
  ride the same socket.
- **Presigned direct-to-storage uploads** — the browser PUTs file bytes
  straight to Cloudflare R2; the API only signs. A local-disk driver keeps
  everything working before R2 credentials exist.
- **Sharing cascades downward** — invite editors by email, editor links,
  read-only links with feedback (comment/react), password protection.
  The Home board can never be shared or exported.
- **Capture-then-organize** — each board has an Unsorted tray; quick capture
  with Ctrl/⌘+Enter; drag out to file items. Trash keeps deletions 3 months.

## Using the app

| Action | How |
|---|---|
| New note | double-click the canvas (or toolbar 📝) |
| New board | toolbar 🗂️, double-click to enter, breadcrumbs to go back |
| Move card(s) | drag; shift-click or marquee for multi-select (one transaction) |
| Connect cards | toolbar ↗ or the card's edge anchor, then click the target |
| Zoom | Ctrl+scroll · fit-all with `Z` · pan with scroll / middle-drag / space-drag |
| Upload | drop files anywhere, or toolbar 🖼️ |
| Link card | toolbar 🔗 or drop a URL — server fetches title/thumbnail; YouTube/Vimeo embed live |
| Synced note | select a note → ⧉ — edits update every copy (CLONE) |
| To-dos | toolbar ☑️, Tab/Shift+Tab to indent, checkbox to complete |
| Column | toolbar ▤, drop cards into it, collapse with ▾, count badge |
| Search | Ctrl/⌘+F across everything you own |
| Share | topbar **Share** (owner only) — editors, edit links, read-only links |
| Export | topbar ⬇ — Markdown/plain-text linearization |
| Trash | topbar 🗑 — restore, delete forever, empty |
| Due dates & reminders | task row 📅 — pick a due date and a reminder time; reminders arrive as notifications |
| Text direction | automatic per paragraph/field from the first letter (Arabic → RTL); override per element via right-click → Text direction, or the ↔ button in the note format bar |
| Arabic numerals | typing digits inside Arabic text produces ٠١٢٣٤٥٦٧٨٩ automatically; Latin context keeps 0-9 |
| Settings | avatar menu → **Settings** — account, notifications, appearance, preferences, localization, toolbar, privacy |

## Settings

The avatar menu (top-right) opens the settings dialog — every change saves
automatically and syncs to your account:

- **Account settings** — change your name, email, and password (all written
  through to Keycloak), see your plan, and delete your account (danger zone;
  purges every board you own plus the Keycloak identity).
- **Emails & notifications** — per-kind switches (mentions, comments,
  sharing, task assignments, reminders, board activity) enforced server-side
  at notification creation; email delivery + digest cadence stored for the
  SMTP extension point.
- **Appearance** — light/dark/system theme, nine accent colors, canvas dot
  grid, card shadows, comfortable/compact density.
- **Preferences** — what double-click creates (note/board/nothing), wheel
  scrolls vs zooms, snap-to-grid (20 px), spell check, canvas hints.
- **Localization** — English/العربية live UI language, first day of week,
  date format, 12/24-hour time.
- **Toolbar options** — show/hide any of the 18 tools in the left rail.
- **Privacy** — presence visibility (invisible mode hides you from presence,
  cursors, and editing indicators), email visibility, and **Download my
  data** (full JSON export of everything you own).

## Auth, sessions & security

- **Branded login** — Keycloak serves the custom `qomranote` login theme
  (`keycloak/themes/`, mounted read-only), styled like the app in light and
  dark. Registration, reset-password, and error pages inherit it.
- **JWT verification** — signature + issuer + expiry via realm JWKS, plus an
  authorized-party pin: only tokens minted for `qomranote-web` are accepted
  (`KEYCLOAK_WEB_CLIENT_ID`).
- **No tokens in URLs** — bearer tokens are Authorization-header-only, and
  the WebSocket handshake takes a 30-second single-use ticket exchanged over
  the authenticated REST channel.
- **Sockets die with their credential** — a WebSocket closes when the JWT
  that opened it expires (close code 4401); the client reconnects with a
  fresh ticket and refetches the board.
- **Sessions** — access tokens 15 min; SSO 4 h idle / 12 h max, or 14/30
  days with *Remember me*; refresh tokens rotate on every use. The SPA keeps
  tokens in memory only, refreshes single-flight, retries one 401 with a
  forced refresh, and shows a "session expired" toast before re-login.
- **Web tier** — immutable caching for hashed assets, `no-cache` for
  index.html (deploys land without hard refresh), nosniff / frame-deny /
  referrer / permissions headers.

## Switching file storage to Cloudflare R2

1. Cloudflare dashboard → **R2** → create bucket (default name `qomranote`).
2. **Manage R2 API Tokens** → create an S3 auth token (Object Read & Write).
3. Fill in `.env`:
   ```
   STORAGE_DRIVER=r2
   R2_ACCOUNT_ID=<your account id>
   R2_ACCESS_KEY_ID=<token key id>
   R2_SECRET_ACCESS_KEY=<token secret>
   R2_BUCKET=qomranote
   R2_PUBLIC_BASE_URL=          # optional public/custom-domain base
   ```
4. `docker compose up -d api` (or restart `make dev-api`). Presigned uploads
   now go straight to R2 — no other change needed.

## CLI (Cobra)

```
qomranote serve      # run the API server
qomranote migrate    # ensure Mongo indexes, purge expired trash (90 days)
qomranote seed       # seed the system template library
qomranote version    # build version
```

## API surface

Full REST spec in [PLAN.md §3.3](PLAN.md) — `/api/v1`: bootstrap (`/me`),
boards/children/unsorted, element CRUD via transactions, trash, presigned
attachments, link metadata, sharing (4 mechanisms), search, comments,
labels, notifications, export, plus `/ws` for realtime. Errors use a
consistent `{"error":{"code","message"}}` envelope; all mutating routes are
Keycloak-token protected.

## Project layout

```
backend/    Go API — cmd/qomranote (CLI), internal/{domain,repository/mongo,
            service,auth,realtime,storage,transport/http}
frontend/   React SPA — src/{api,auth,store,realtime,canvas,components}
keycloak/   realm-export.json (auto-imported: realm, clients, demo user)
PLAN.md     the master implementation plan (research-cited)
```
