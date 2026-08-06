# QomraNote SaaS Platform Plan

**Roles, admin dashboards, the enterprise tier, plans, limits, and billing — researched 2026-08-04.**

This document is a plan, not a change. Nothing described here is implemented. It is grounded
in three research passes run on 2026-08-04: (1) a line-by-line inventory of the existing
backend and frontend (every claim carries `file:line`), (2) live competitor pricing/packaging
research (Milanote, Miro, Mural, Figma, Notion, Canva, ClickUp, Loop — source URLs in §6),
and (3) enterprise-SaaS architecture research (RBAC models, Keycloak 26 Organizations, SCIM,
metering, Stripe, audit logging — source URLs inline).

**Part I** (§1–18) is the product and architecture plan — what to build and why.
**Part II** (§II.0–II.8) is the implementation guide — how each phase lands in this codebase:
files to create, files to touch, code shapes, and wiring points anchored to real locations.

---

## 1. Where the product stands today

The inventory that this plan builds on. Everything below was verified in the code, not assumed.

### What exists

| Capability | Where | State |
|---|---|---|
| Authentication | `backend/internal/auth/oidc.go:48-53` | Keycloak OIDC, PKCE. Exactly four claims read: `email`, `name`, `preferred_username`, `azp`. No roles, no groups. |
| Per-board ACL | `backend/internal/service/access.go:17-30` | Computed role lattice `None → View → Feedback → Edit → Owner`, resolved by walking the containment chain (`access.go:127-161`). Never persisted. |
| Share links | `domain/element.go:95-103, 188-194` | Edit link + view link (password, require-account, feedback). Provenance tracked (`access.go:110-123`). |
| Plan field | `domain/models.go:15` | `User.Plan string // free \| pro` — written once as `"free"` (`service/user_service.go:66`), **read by zero backend code paths**, shown as read-only text in the UI (`SettingsDialog.tsx:320-322`). |
| Upload limits | `service/upload_service.go:32-36` | Global constants (100 MB image / 5 GB file), not plan-derived. Comment admits: "Plan-style limits… generous 'pro' defaults here." |
| The one real meter | `agent/service.go:691-828` | AI agent model spend: per-user (`Run.Tenant`) and per-board USD ledgers aggregated from `agent_runs.usage.costUsd` (`repository/mongo/agent.go:182-216`), enforced at admission and mid-run against `AGENT_DAILY_CAP_USD` + `ACL.AgentPolicy.DailyCapUSD`. |
| Delegated authority | `domain/delegation.go` | The attenuated-principal pattern: capability list, containment roots, consequence ceiling, content-key allowlist, expiry, approval record — enforced per-op in `service/transaction_service.go:854+`. |
| Stored policy object | `domain/element.go:132-184` | `AgentPolicy{Allow, AutoApply, DailyCapUSD, ChargedTo}` — the only owner-authored policy record in the product. |
| Admin CLI | `backend/internal/cli/` | 8 Cobra commands (`serve`, `migrate`, `seed`, `backup`, `restore`, evals). No HTTP admin surface. |
| Keycloak admin client | `auth/keycloak.go` | Five operations only: find-by-email, update profile, verify/set password, delete user. |
| Commit event seam | `domain/transaction.go:123-125` + `service/commitbus.go` | In-process publish of committed transactions — the natural hook for usage counters and audit fan-out. |
| Tenant purge registry | `service/account_service.go:42-44` | `TenantPurger` pattern — per-subsystem tenant-scoped cleanup, reusable for org deletion. |

### What is completely missing

- **No role concept anywhere.** Not in the realm (`keycloak/realm-export.json` has no `roles` block), not in claims parsing, not on `Principal` or `User`. Every authenticated human is identical.
- **No org/workspace/team entity.** `tenant` throughout the codebase is a strict synonym for one Keycloak `sub`. Sharing is per-user ACL + bearer links; there is no membership record, no seat model, no invitation entity (invite = immediate `Editors` append).
- **No entitlements or plan enforcement.** No plan catalogue, no plan→limit mapping, no Stripe, no customer/subscription id, no trial, no upgrade path.
- **No metering except agent spend.** Storage is never aggregated (`Attachment.Size` exists per row, is summed nowhere); boards/elements/collaborators per user are never counted.
- **No admin HTTP surface.** `router.go` is the complete API; every route is principal-scoped. No user list, no impersonation, no suspension, no cross-tenant read.
- **No audit log.** The transaction journal and agent run journal are user-scoped content history; administrative actions (role grants, exports, config changes) have no ledger.
- **No SSO/SCIM/MFA policy, no metrics endpoint, no org-level anything.**

---

## 2. Design principles

These follow the codebase's own architecture (constructor-injected services over domain
interfaces, `service/access.go:1-4`) and the conclusions of the architecture research.

1. **Org RBAC coarse, resource ACL fine.** Keep the existing per-board ACL exactly as it is —
   it is the ReBAC half of the model (the same split Miro/Notion/Slack use). Org roles set
   defaults and admin override; they do not replace board sharing. **Do not adopt
   SpiceDB/OpenFGA/Zanzibar** — a Mongo membership collection plus service-layer checks covers
   this product; a separate authz service is unjustified operational overhead until deeply
   nested cross-service sharing exists.
2. **Keycloak authenticates; the app authorizes.** Keycloak 26 Organizations provides org
   membership, domain-routed login, and per-org IdP brokering (enterprise SSO) — but it has
   **no per-org roles** (open upstream ask, GitHub discussion #36597). App roles live in Mongo,
   resolved per request. One new realm role only: `platform-admin`, for QomraNote staff.
3. **One authority pattern, not two.** Anything that lets one principal act with elevated or
   borrowed power — support impersonation, org-admin content override — is expressed as an
   **attenuated, expiring, audited grant**, reusing the `Delegation` design
   (`domain/delegation.go`). We already have proof this pattern survives adversarial pressure.
4. **Enforce at the service layer, next to the write.** Middleware answers coarse questions
   (authenticated? platform-admin? org suspended?). Quota reservation happens atomically in the
   service, immediately before the side effect it guards — never check-then-act across a network hop.
5. **Fail closed, allowlist polarity.** Same rule the delegation content-keys chose
   (`delegation.go:63-74`): an entitlement dimension absent from a plan's map is denied/zero,
   not unlimited. A missing org membership is `RoleNone`, not member.
6. **Meter in native units internally, sell abstract units externally.** Agent spend stays a USD
   ledger (it already is); users see *credits* via a rate card. Model price changes then adjust
   the rate card, not the schema. (This is the pattern the 2025-26 market converged on —
   Notion killed its standalone AI add-on May 2025; Miro/Figma/Canva all sell credits.)
7. **Every admin action is an audit event.** The audit collection is append-only — no update or
   delete code path exists against it, by construction.
8. **Personal accounts keep working unchanged.** An org is a layer users can be invited into;
   a user with no org runs exactly today's code paths. No forced migration.
9. **Bilingual from day one.** Every new surface ships with `en` + `ar` strings and RTL layout,
   like the rest of the product (`frontend/src/i18n/index.ts`).

---

## 3. Tenancy model: Organizations

### 3.1 Entities (new Mongo collections)

```
organizations
  _id            ObjectId
  name           string
  slug           string        // unique, url-safe
  kcOrgId        string        // Keycloak Organization id (once provisioned; empty until §12)
  plan           string        // free | pro | business | enterprise
  status         string        // active | suspended | deleting
  ownerSub       string        // the founding owner (also in memberships)
  settings       {                                   // org-level policy
    allowPublicLinks      bool                       // sharing restriction
    allowGuestInvites     bool
    defaultBoardRole      string  // edit | view — what plain members get on org boards
    agent: { allow, autoApply, monthlyCreditPool, perMemberDailyCredits }  // org AgentPolicy
    trashRetentionDays    int     // 90 default, configurable Business+
    auditRetentionDays    int     // per-tier ceiling, §9
  }
  verifiedDomains  [ { domain, verifiedAt, token } ] // domain capture, Business+
  createdAt, updatedAt

org_memberships
  _id       ObjectId
  orgId     ObjectId   (indexed, compound unique with userSub)
  userSub   string     // Keycloak sub — same key the ACL uses
  role      string     // owner | admin | member | guest
  seatKind  string     // full | guest   (guests are unbilled, §6)
  invitedBy string
  joinedAt  time
  status    string     // active | suspended

org_invitations
  _id, orgId, email, role, token(hash), invitedBy, expiresAt, acceptedAt?
  // today's invite = immediate Editors append (share_service.go:159);
  // org invites need a real pending record because the invitee may have no account yet.
```

`domain.User` gains nothing. Org membership is resolved by query, cached per request. A user
may belong to multiple orgs (consultant pattern — Slack/Notion both allow it); the client
carries an active-org context.

### 3.2 Org boards

Boards stay elements with ACLs. An org board is a board whose ACL carries a new optional field:

```go
// domain/element.go ACL — addition
OrgID string `bson:"orgId,omitempty" json:"orgId,omitempty"`
```

Resolution (one new arm in `roleFromACL`, `access.go:216`): if `acl.OrgID` is set and the
caller has an active membership in that org, grant `defaultBoardRole` (edit by default) for
members, `RoleOwner`-equivalent for org admins **via override, not via ACL mutation** — the
override is an explicit, audited code path (§11.4), so a board's ACL always reads truthfully.
Named `Editors`, links, and the owner keep working unchanged; strongest grant still wins
(`access.go:151`). Guests get nothing from org membership — only what boards name them on.

Each org gets a **Home board per member** (unchanged) plus an **org root board** ("Workspace")
created at org creation, owned by the org (`ACL.OrgID` set, `OwnerID` = founding owner).

### 3.3 What does NOT change

The Home board rule (never shared/exported), trash, undo, realtime rooms, the transaction
pipeline, the agent's delegation checks — all unchanged. The agent acquires org awareness only
through policy resolution (org settings become one more layer in `MayRun`/`MayAutoApply`
resolution, evaluated before the board's own `AgentPolicy`).

---

## 4. RBAC: the role matrix

Two new persisted layers above the existing computed board role. Coarse by design — Miro's
four-way admin split (Company/Content/User/Security admin) is a later Enterprise
differentiator, not the starting point.

### 4.1 Platform layer (QomraNote staff)

| Role | Grants | Carried as |
|---|---|---|
| `platform-admin` | The internal operator dashboard (§11): org/user search, plan override, suspension, audited impersonation, global metrics. | Keycloak **realm role**, parsed from `realm_access.roles` in the JWT (new claim in `oidc.go` tokenClaims), surfaced as `Principal.PlatformRoles []string`. |

No other platform role initially. `platform-admin` grants **zero content access** — reading a
user's board requires an impersonation grant (§11.4), never the role alone.

### 4.2 Org layer (persisted in `org_memberships.role`)

| Capability | owner | admin | member | guest |
|---|---|---|---|---|
| Billing, plan changes, delete org | ✔ | — | — | — |
| Transfer org ownership | ✔ | — | — | — |
| Members: invite/remove/change role | ✔ | ✔ | — | — |
| Org settings & security policies | ✔ | ✔ | — | — |
| Domain verification, SSO/SCIM config (Ent.) | ✔ | ✔ | — | — |
| Audit log view/export | ✔ | ✔ | — | — |
| Usage & analytics dashboards | ✔ | ✔ | — | — |
| Content override on org boards (audited) | ✔ | ✔ | — | — |
| Create org boards | ✔ | ✔ | ✔ | — |
| Default role on org boards | owner-like | owner-like | `defaultBoardRole` | none |
| Personal boards outside the org | ✔ | ✔ | ✔ | ✔ |

### 4.3 Board layer (existing, unchanged)

`RoleNone → RoleView → RoleFeedback → RoleEdit → RoleOwner` (`access.go:20-26`), resolved per
element, cascade-down, strongest-grant-wins. This remains the single content authorization
mechanism.

### 4.4 The authorization package

New `backend/internal/authz` package, mirroring the shape of `AccessResolver`:

```go
type OrgResolver struct { memberships MembershipRepository; cache requestCache }

func (r *OrgResolver) Membership(ctx, orgID, sub) (OrgRole, error)     // RoleNone if absent — fail closed
func (r *OrgResolver) RequireOrgRole(ctx, orgID, sub, atLeast OrgRole) error
func (r *OrgResolver) OrgsOf(ctx, sub) ([]Membership, error)
```

Route middleware only for the two coarse gates:
- `/api/v1/admin/*` → require `Principal.HasPlatformRole("platform-admin")`, else 404 (not 403 —
  the surface's existence is not advertised).
- `/api/v1/orgs/:id/*` → resolve membership once, stash in context; handlers assert the
  fine-grained role.

---

## 5. AuthN changes (Keycloak)

Incremental — each step is independently shippable.

1. **Parse roles.** Extend `tokenClaims` (`oidc.go:48-53`) with
   `RealmAccess struct { Roles []string } \`json:"realm_access"\`` and (later)
   `Organization map[string]any` for the KC 26 org claim. Add `PlatformRoles` to
   `domain.Principal`. Zero behavior change until a route reads it.
2. **Realm additions** (`keycloak/realm-export.json` + a migration note for existing realms,
   since the export only imports on first boot — same caveat the README documents for
   `allow-origin.sh`): add realm role `platform-admin`; assign to staff accounts by hand.
3. **Organizations (Enterprise phase, §12):** enable KC 26 Organizations; one KC org per
   customer org (`organizations.kcOrgId`); domain-based login routing; per-org IdP brokering
   (SAML/OIDC) for enterprise SSO. App membership stays authoritative in Mongo; a webhook/sync
   job reconciles KC org membership → `org_memberships` for SSO-provisioned users.
4. **SCIM (Enterprise phase):** target Keycloak's native SCIM preview (~26.6+ — version-check
   against the release notes of the deployed KC before committing; the open-source
   `scim-for-keycloak` extension is EOL since KC 21) or a thin SCIM 2.0 endpoint of our own
   that writes `org_memberships` directly. Decision deferred to §12; the membership collection
   is designed so either source of truth can drive it.
5. **MFA policy (Enterprise):** enforced via Keycloak per-org authentication flow binding;
   surfaced in the org console as a toggle; member MFA status read via admin API for the
   security posture panel (§12.3).

---

## 6. Pricing & packaging

### 6.1 The competitive picture (verified 2026-08, sources at bottom of section)

| Product | Free gate | Entry | Business | Enterprise gates | AI |
|---|---|---|---|---|---|
| Milanote | 100 cards + 10 uploads | $9.99 Personal | flat $49 team | **none — no enterprise tier at all** | none |
| Miro | 3 boards | $8 | $20 | SSO, SCIM, audit, residency; **30-seat minimum** | 10/25/50 credits per seat/mo; Ent. 2,500 pooled |
| Mural | 3 murals | $9.99 | $17.99 (**SSO here**) | SCIM, audit, residency, BYOK | bundled, unmetered publicly |
| Figma/FigJam | 3+3 files | Collab $3 / Full $16 | Org $55 | Ent $90: SSO, SCIM, logs, EU hosting | 500–4,250 credits/mo by seat |
| Notion | free solo; capped for 2+ | $10 | $20 (**SAML + AI bundled**) | SCIM, audit, DLP/SIEM, zero-retention | add-on died 5/2025 → bundled + usage credits |
| Canva | 5GB storage | ~$12.50 Pro | $25 Business | SSO/SCIM custom | 50 free / 500 Pro credits/mo |

Structural findings that shape our ladder:

- **Milanote — our closest product comp — has no per-seat ladder, no SSO, no AI, no
  enterprise anything.** The commercial opening is literally "Milanote's product with Miro's
  commercial ladder."
- **The SSO tax is a live wedge.** Miro/Figma gate SSO at Enterprise (sso.tax documents the
  resentment); Mural ($17.99) and Notion ($20) broke ranks and sell SAML mid-tier. We follow
  Mural/Notion and market it.
- **Miro's 30-seat Enterprise minimum turns away 10–29-seat security-conscious teams.** We take
  no minimum.
- **AI packaging converged** on: bundled into the ~$20 tier + credit metering + usage billing
  for heavy use. Standalone AI add-ons are dead.
- **Whiteboard incumbents publish no total storage quota** (they meter boards); Canva-style
  legible GB quotas are an upgrade trigger for an image-heavy product like ours. We publish GB.

### 6.2 The QomraNote ladder

Prices are per seat, per month, billed annually (monthly ≈ +25%). Guests are always free and
unbilled (`seatKind: guest`).

| | **Free** | **Pro — $9** | **Business — $18** | **Enterprise — custom (~$30 effective, no seat minimum)** |
|---|---|---|---|---|
| Boards | **3 top-level boards** (nested sub-boards uncounted) | Unlimited | Unlimited | Unlimited |
| Cards/elements | 200 total (2× Milanote's 100) | Unlimited | Unlimited | Unlimited |
| Collaborators per board | 3 editors | Unlimited | Unlimited | Unlimited |
| Orgs / workspace | — | — | ✔ (org entity, org boards, roles) | ✔ multi-workspace |
| Guests | — | 5 | Unlimited | Unlimited |
| File size cap | **10 MB** (2× Notion's 5) | 100 MB | 500 MB | Custom |
| Total storage | **1 GB** | 20 GB/seat | 100 GB/seat, pooled | Custom |
| AI agent credits (§7.4) | **25/mo** (2.5× Miro free) | 100/seat/mo | 300/seat/mo, **pooled org-wide** | Custom pool, starts 5,000/mo (2× Miro Ent.) |
| Version/trash retention | 30 days trash | 90 days (today's `TrashRetention`) | Configurable ≤ 365 | Configurable + legal hold (roadmap) |
| **SAML SSO** | — | — | **✔ (no SSO tax — market this)** | ✔ + enforcement (block password login) |
| SCIM provisioning | — | — | — | ✔ |
| Audit log | — | — | 90-day, console + CSV | 365-day + **JSON API** + SIEM roadmap |
| Admin console | — | — | ✔ (§10) | ✔ + enterprise dashboard (§12) |
| Analytics | — | — | Basic usage panel | Full engagement + security analytics |
| Support | Community | Standard | Priority | CSM + SLA |

Enforcement values live in a versioned plan catalogue (§7.1), not in code constants — the
existing `upload_service.go:32-36` constants become the fallback ceiling.

Personal `User.Plan` maps: `free` → Free, `pro` → Pro (existing field finally becomes real).
Business/Enterprise are org plans (`organizations.plan`); a member's effective entitlements =
max(personal plan, plan of the active org context) per dimension.

### 6.3 AI credit economics

Internal metering stays USD (the existing ledger, `agent/service.go:708-727`). Rate card:
**1 credit = $0.02 of metered model spend**, rounded up per run. A typical run today costs
$0.05–$0.35 (`Budget.MaxCostUSD` default 0.35, `contracts.go:300`), i.e. **3–18 credits/run**:

- Free 25 credits ≈ 3–8 real runs/mo ≈ $0.50 COGS ceiling — cheap acquisition of the hero feature.
- Pro 100 credits ≈ $2.00/mo COGS ceiling on a $9 seat — worst case 22% of revenue, typical far less.
- Business 300 pooled ≈ $6.00 ceiling on $18 — pooling smooths heavy/light users, same 33% worst-case bound.
- Overage (Business+): purchasable credit packs, $5 per 200 credits (2× COGS margin), Stripe metered item.

The existing per-run cost clamp, mid-run budget re-check, and unpriced-model refusal
(`service.go:736-828`) all remain — credits are a **presentation and quota layer above** the
USD ledger, never a replacement for it.

### Sources (§6)

milanote.com/plans · miro.com/pricing · help.miro.com/hc/en-us/articles/360017731013 ·
mural.co/pricing · figma.com/pricing · help.figma.com/hc/en-us/articles/13838684089751 ·
notion.com/pricing · sso.tax · felloai.com/canva-pricing · getmonetizely.com SaaS pricing
benchmark 2025. Caveats: Mural publishes no AI meter; Miro/Mural publish no total storage
quota; Canva figures cross-checked from secondary 2026 sources (pricing page blocked fetching).

---

## 7. Entitlements & metering

### 7.1 Plan catalogue

A versioned, in-repo policy map (Go, table-driven — reviewable in diff, testable), exposed
read-only at `GET /api/v1/plans`:

```go
type Entitlements struct {
    TopLevelBoards   Limit  // n or Unlimited
    TotalElements    Limit
    EditorsPerBoard  Limit
    Guests           Limit
    FileBytesMax     int64
    StorageBytes     int64  // per seat where pooled=false
    StoragePooled    bool
    AgentCreditsMo   int
    CreditsPooled    bool
    TrashDays        int
    AuditDays        int
    Features         map[Feature]bool // FeatSSO, FeatSCIM, FeatAuditAPI, FeatOrg, FeatAnalytics…
}
var Catalogue = map[Plan]Entitlements{ … }   // absent dimension = zero/denied (principle 5)
```

### 7.2 Usage counters

One document per subject per period, atomically maintained:

```
usage_counters
  _id       "u:<sub>:2026-08" | "org:<orgId>:2026-08" | "u:<sub>:total"
  dims      { boards: n, elements: n, storageBytes: n, agentCredits: n, seats: n }
  updatedAt
```

**Reservation is one atomic op** — the pattern the metering research confirmed
(`findOneAndUpdate` + `$inc` guarded by a `$lt` precondition; document-level atomicity makes
check+increment race-free with no transaction):

```go
// backend/internal/entitle — the single enforcement door
func (s *Service) Reserve(ctx, subject Subject, dim Dimension, n int64) error
// filter: {_id: key, "dims.storageBytes": {$lt: limit - n + 1}}  update: {$inc: …}
// miss ⇒ ErrQuotaExceeded{Dim, Used, Limit, Plan} → HTTP 402/409 with upgrade hint
func (s *Service) Release(ctx, subject, dim, n)         // delete/trash-restore paths
func (s *Service) Peek(ctx, subject) (Usage, Entitlements)  // dashboards, warning banners
```

Call sites (service layer, immediately before the side effect):
- `TransactionService` create ops → `elements` (and `boards` when `Type==BOARD` at top level)
- `UploadService.Presign` (`upload_service.go:49`) → `storageBytes` reserve by declared size;
  `Complete` reconciles to actual; delete/purge paths release. Backfill job: one `$group` over
  attachments by owner seeds the counter (the aggregation that §5-inventory confirmed never existed).
- `ShareService.InviteEditor` → `editorsPerBoard` (Free: 3)
- Agent admission (`agent/service.go:499-505`) → `agentCredits` (converted from the USD check
  it already performs; the USD caps remain as the hard backstop)
- `OrgService.AddMember` → `seats` (drives Stripe seat quantity, §8)

**Soft-limit UX** (per the metering research): warn at 80% (banner + notification via the
existing `NotificationKind` system), block at 100% with the specific dimension and a one-click
upgrade path. Grace: reads never blocked; only new writes in the exceeded dimension.

### 7.3 Storage accounting details

`Attachment.Size` is already stored per row (`domain/models.go:86`) and checked per file at
presign — the only additions are the counter reserve/release calls and the backfill. R2 and
local drivers need no change (bytes never transit the API; the presign path is the choke point).

### 7.4 Credits ledger

```
credit_ledger
  _id, subject ("u:…"|"org:…"), period "2026-08",
  entries [ { runId, costUSD, credits, at } ],   // append-only, joins to agent_runs
  granted, used, purchased
```

Derived from the run's existing `Usage.CostUSD` at run completion (hook: the same place
`StateAt` is stamped). `GET /agent/usage` (`handlers_agent.go:138`) extends to return credits
alongside USD; the capabilities payload (`handlers_agent.go:463`) gains
`credits: {remaining, monthly, pooled}` so the bar can show allowance before refusal — the
same "plannable, not discovered by refusal" principle the USD budget UI already follows
(`frontend/src/api/types.ts:528`).

---

## 8. Billing (Stripe)

- **Stripe Billing, direct — no entitlement vendor initially.** (Stigg/Lago/Orb reconsidered
  only if the plan catalogue outgrows a Go policy map.) Meters API (post-2025-03-31 Stripe;
  legacy usage-records is gone), one subscription per billing subject:
  - Pro: per-seat licensed price (qty 1)
  - Business: per-seat licensed price (qty = active `seatKind: full` memberships, updated on
    membership change) + metered credit-overage item
  - Enterprise: sales-led; invoice billing; same subscription objects under the hood
- New collection `billing_accounts { subject, stripeCustomerId, subscriptionId, plan, status,
  currentPeriodEnd, delinquentAt? }`.
- **Webhooks drive entitlements; Mongo is the source of truth for usage, Stripe for money.**
  Handler rules (from the research): verify signature, 200-immediately + process async, store
  raw payloads, idempotent by event id. `checkout.session.completed` → set plan;
  `invoice.payment_failed` → `past_due` + banner; 5-day grace, then plan reverts to Free
  entitlements (data untouched, writes gated).
- Endpoints: `POST /api/v1/billing/checkout` (Stripe Checkout session),
  `POST /api/v1/billing/portal` (customer portal — Stripe hosts card/invoice UI, we build
  almost no billing UI), `POST /api/v1/billing/webhook` (unauthenticated route + signature
  verification, registered outside the auth groups like `/healthz`, `router.go:11`).
- The settings dialog's read-only plan row (`SettingsDialog.tsx:320-322`) becomes the upgrade
  entry point.

---

## 9. Audit logging

Append-only from day one; enterprise differentiators later ride on it.

```
audit_events
  _id        ObjectId (time-ordered)
  orgId      ObjectId?        // absent for personal-account events
  actor      { sub, impersonatedBy?, agentRunId?, platformRole? }
  action     string           // dotted taxonomy below
  target     { type, id, label }
  outcome    "ok" | "denied" | "error"
  ip, ua     string           // from the request
  requestId  string           // joins to zap logs + transactions (Principal.RequestID already flows)
  meta       { before?, after?, … }   // structured, indexed keys — not freeform strings
  at         time
  chain      string?          // sha256(prev.chain + canonical(this)) — tamper evidence, Enterprise
```

**Taxonomy** (from the SSOJet/Notion baselines, mapped to our product):
`auth.login`, `auth.login_failed`, `auth.mfa_changed` · `org.created/updated/deleted`,
`org.member_invited/joined/removed/role_changed`, `org.domain_verified`,
`org.policy_changed`, `org.sso_configured` · `billing.plan_changed`, `billing.payment_failed` ·
`share.link_created/revoked`, `share.editor_invited/removed`, `share.external` (link used by a
non-member — feeds §12.2) · `data.export`, `data.account_deleted` ·
`admin.impersonation_started/ended`, `admin.plan_override`, `admin.suspension` ·
`agent.run_admitted/applied/reverted` **with the delegation grant summary** — logging
delegated-authority AI actions is explicitly on the 2026 enterprise audit baseline, and we are
unusually well-positioned because the grant already records scope, approver, and expiry.

Written via one helper — `audit.Emit(ctx, Event)` — called from the service layer; the
`TransactionSubscriber` commit bus (`domain/transaction.go:123`) fans content-level events in
without touching the write path. **No update/delete code path exists** against the collection;
retention is a per-org TTL boundary enforced by a sweep that honors the org's tier ceiling
(30/90/365 — Miro's own configurable-retention pattern). Indexes: `(orgId, at)`,
`(orgId, action, at)`, `(actor.sub, at)`.

Surfaces: Business — console viewer + CSV export. Enterprise — cursor-paginated JSON API
(`GET /api/v1/orgs/:id/audit?cursor=…`), SIEM webhook streaming deferred (roadmap, §12.5).

---

## 10. The org-admin console (customer-facing, Business+)

Frontend: new route area `/org/:slug/admin` in the SPA (code-split; hidden entirely unless the
active membership is admin+). Backend: `/api/v1/orgs/:id/*` per §4.4.

| Panel | Contents | Backing |
|---|---|---|
| **Overview** | Seats used/paid, storage, credit pool gauge, WAU sparkline, plan + renewal | `usage_counters`, `billing_accounts`, membership counts |
| **Members** | List (name, email, role, seat kind, last active, MFA badge*), invite by email (pending list, resend/revoke), role change, remove, suspend | `org_memberships`, `org_invitations`; last-active from `agent_runs`/`transactions` maxima; *MFA Enterprise |
| **Security** | Toggles: public share links, guest invites, default board role; verified domains (DNS TXT flow); session policy* and MFA enforcement* (Enterprise) | `organizations.settings`, audited on change |
| **AI governance** | Org agent policy: allow/deny, auto-apply ceiling, monthly credit pool, per-member daily credit cap; spend & credit analytics; recent org-board runs (links into the existing per-board audit view, `handlers_agent.go` audit route) | extends the existing `AgentPolicy` resolution chain — org policy evaluated before board policy in `MayRun`/`MayAutoApply` |
| **Audit log** | Filterable viewer (actor, action, date), CSV export (itself an audited `data.export` event) | `audit_events` |
| **Billing** | Plan card, seat count, upgrade/downgrade → Stripe Portal | §8 |

Everything here is also API — the console consumes the same `/api/v1/orgs/:id/*` endpoints an
Enterprise customer could script against.

---

## 11. The platform admin dashboard (QomraNote staff)

Separate concerns, separate surface: `/api/v1/admin/*` (realm-role-gated, 404 to everyone
else) and an `/admin` SPA area. This is the operator tooling the CLI can't provide.

### 11.1 Directory
Org and user search (email/slug/sub), detail views: plan, status, usage counters, billing
state, membership graph, recent audit trail. **No board content anywhere in these views.**

### 11.2 Plan & entitlement overrides
Set plan/status manually (sales-led Enterprise, refunds, abuse). Extends `UserRepository`
with the plan mutator that today does not exist (§1). Every override → `admin.plan_override`.

### 11.3 Suspension
Org or user: `status: suspended` blocks writes at the same middleware that resolves org
context; Keycloak `enabled=false` for hard lock (first use of the admin client beyond its
current five operations, `auth/keycloak.go`).

### 11.4 Impersonation — the Delegation pattern, reused
Support acts as a user only under an **attenuated grant**, minted server-side exactly like the
agent's (`domain/delegation.go` axes: on-behalf-of, containment, capability list, expiry ≤ 30
min, consequence ceiling `ReversibleWrite` — destructive ops refused):

```go
type SupportGrant struct {  // sibling of Delegation, enforced at the same choke points
    TicketRef string        // required — the research flags impersonation as the highest-
    ReadOnly  bool          // sensitivity audit event; ticket linkage is the norm
    …                       // OnBehalfOf, ExpiresAt, audited start/end events
}
```
The user is notified (existing `NotificationKind` machinery) unless legal hold requires
otherwise. Session banner in the UI while active.

### 11.5 Global health
Aggregate metrics (orgs by plan, DAU/WAU, storage totals, agent spend vs. revenue, error
rates). Requires the metrics plumbing that doesn't exist yet — Prometheus `/metrics` +
Grafana, fed by the existing zap/`RequestID` seams; dashboard reads the same aggregates.

---

## 12. The Enterprise tier — a dashboard that earns its price

The user-stated requirement: enterprise gets *actually useful* capability, not just team
control. From the research, 2026 table stakes are SSO+SCIM+audit-API+analytics; the
differentiators are security insight and governance. The Enterprise org's console (§10) gains:

### 12.1 Engagement analytics
WAU/MAU per org and per team, boards created/edited over time, top boards by activity,
inactive-member list (feeds seat reclamation). All computable from `transactions`
(`boardId, userId, createdAt` indexes already exist, `indexes.go:33-45`) + `agent_runs` — no
new event pipeline required for v1.

### 12.2 External sharing report — the highest-value/lowest-cost security feature we can ship
Every org board with a live share link or a non-member editor: who created it, when, password
y/n, expiry, and `share.external` audit events showing actual outside access. This is a pure
read over existing `ACL` data (`element.go:95-103`) that incumbents charge Enterprise Guard
add-on money for. One aggregation, one table, immediate CISO value.

### 12.3 Security posture panel
SSO enforcement state, MFA coverage (% members enrolled, via KC admin API), password-login
usage after SSO enablement, admin-role count, domain coverage (% members on verified domains).

### 12.4 License utilization (Figma's 2025 pattern)
Active vs. paid seats, per-seat last-active, one-click downgrade of idle members to guest
seats, approval queue for new seat requests beyond the purchased count.

### 12.5 Governance & compliance
- **SSO**: per-org SAML/OIDC via KC Organizations brokering (§5.3) + enforcement toggle.
- **SCIM**: §5.4; provisioning events land in the audit log.
- **Audit-log JSON API** + retention to 365 days + hash-chain tamper evidence (§9).
- **AI governance, enterprise grade**: org model allowlist (e.g. Anthropic-only), zero-retention
  posture documentation, per-team credit sub-pools, full delegation-grant audit trail — this
  is a genuine differentiator; none of the incumbents audit *agentic* board changes because
  none of them have a delegated write path like ours.
- **Deferred deliberately** (add-on-grade, priced separately if ever): DLP/content
  classification, eDiscovery/legal holds, data residency, BYOK. Each needs infrastructure
  (content scanning, region cells) that would dilute the core build. Named on the roadmap so
  sales can answer honestly.

---

## 13. API surface additions (complete)

```
# plans & billing
GET    /api/v1/plans
POST   /api/v1/billing/checkout | portal
POST   /api/v1/billing/webhook                      # unauth + signature

# orgs (member-facing)
POST   /api/v1/orgs                                 # create (Business checkout flow)
GET    /api/v1/orgs                                 # my orgs
GET    /api/v1/orgs/:id                             # profile + my role
POST   /api/v1/orgs/:id/invitations/accept

# org admin (role: admin+)
GET|PATCH  /api/v1/orgs/:id/settings
GET    /api/v1/orgs/:id/members
POST   /api/v1/orgs/:id/invitations                 # + DELETE /:invId, POST /:invId/resend
PATCH  /api/v1/orgs/:id/members/:sub                # role/seat/status
DELETE /api/v1/orgs/:id/members/:sub
POST   /api/v1/orgs/:id/domains                     # + verify, DELETE
GET    /api/v1/orgs/:id/usage                       # counters + entitlements
GET    /api/v1/orgs/:id/analytics                   # §12.1
GET    /api/v1/orgs/:id/sharing-report              # §12.2
GET    /api/v1/orgs/:id/audit  (+ /export.csv)      # Enterprise: cursor JSON API
GET|PUT /api/v1/orgs/:id/agent-policy
POST   /api/v1/orgs/:id/transfer-ownership          # owner only
DELETE /api/v1/orgs/:id                             # owner only, staged like DeleteAccount

# platform admin (realm role, 404-gated)
GET    /api/v1/admin/orgs | /admin/orgs/:id | /admin/users | /admin/users/:sub
PATCH  /api/v1/admin/orgs/:id/plan | status
PATCH  /api/v1/admin/users/:sub/plan | status
POST   /api/v1/admin/impersonations                 # mint SupportGrant; DELETE ends
GET    /api/v1/admin/metrics
GET    /metrics                                     # Prometheus, network-gated not route-gated
```

Existing routes gain enforcement only (quota reserve calls); no existing contract changes.
Error envelope stays `{"error":{"code","message"}}` with a new machine field
`"quota": {dim, used, limit, plan}` on 402-class refusals.

---

## 14. Data model additions (complete)

| Collection | Purpose | Key indexes |
|---|---|---|
| `organizations` | tenant entity | `slug` unique, `verifiedDomains.domain` |
| `org_memberships` | who + role + seat | `(orgId, userSub)` unique, `userSub` |
| `org_invitations` | pending invites | `(orgId, email)`, `token` hashed, TTL on `expiresAt` |
| `usage_counters` | atomic quota docs | `_id` composite key (no secondary needed) |
| `credit_ledger` | AI credits per period | `(subject, period)` unique |
| `billing_accounts` | Stripe linkage | `subject` unique, `stripeCustomerId` |
| `audit_events` | append-only ledger | `(orgId, at)`, `(orgId, action, at)`, `(actor.sub, at)`, per-tier TTL |
| `support_grants` | impersonation | `(actorSub, at)`, TTL on `expiresAt` |

Domain changes: `ACL.OrgID` (one optional string, §3.2) · `Principal.PlatformRoles` +
`Principal.OrgContext` · `UserRepository.SetPlan` (the mutator that doesn't exist today).
`domain.User` otherwise untouched.

**Mongo operational prerequisite** (flagged by the repo's own audit, `AUDIT_SUMMARY.md`
"Is there any database backup at all?"): billing and entitlements make the standalone-Mongo
situation untenable. Move to a 3-node replica set (enables transactions where multi-doc
consistency matters — e.g. membership + seat counter + Stripe qty — and PITR via oplog).
This is the one infrastructure change the plan requires rather than recommends.

---

## 15. Frontend plan

- **New areas**: `/org/:slug/admin` (§10 console) and `/admin` (§11 operator) — code-split,
  role-gated at route load *and* per-render (the API is the real gate; UI gating is UX).
- **Org context switcher** in the top bar (personal / each org), driving board lists and the
  active entitlement context.
- **Quota surfaces**: storage/board/credit gauges in settings; 80% warning banners; blocked-
  write dialogs that name the dimension and deep-link the upgrade. The agent bar's existing
  allowance display extends to credits (§7.4).
- **Upgrade flow**: plan comparison page (the §6.2 table), Stripe Checkout redirect, Stripe
  Portal for management — we build almost no payment UI.
- **State**: one new zustand store (`orgStore`: memberships, active org, entitlements, usage),
  hydrated alongside `/me`; settingsStore untouched.
- **i18n**: every string in `en` + `ar` from the first PR; admin tables RTL-verified. Fix in
  passing: the `accessibility` settings group exists in `types.ts:178` with no backend
  counterpart — reconcile before extending the settings payload.

---

## 16. Implementation phases

Each phase is shippable and independently valuable; later phases never block earlier revenue.

| Phase | Scope | Key acceptance criteria |
|---|---|---|
| **0 — Foundations** (small) | Parse `realm_access` → `Principal.PlatformRoles`; `authz` package skeleton; `audit_events` + `audit.Emit` wired into share/account/agent services; Prometheus `/metrics`; Mongo replica-set migration + backup runbook | Token with `platform-admin` reaches a stub `/admin/ping`; every share-link creation lands an audit row; `mongodump` replaced by PITR-capable procedure |
| **1 — Orgs & membership** | Collections; org CRUD; invitations (email + accept flow); `ACL.OrgID` resolution arm; org boards; org context in client | Two users in one org see the org root board with correct default roles; strongest-grant tests green; invite of a non-account email completes after signup |
| **2 — Entitlements & metering** | Plan catalogue; `entitle.Reserve/Release/Peek`; counters + storage backfill; enforcement at the five call sites (§7.2); credits ledger + rate card; warning UX | Free account blocked at 4th top-level board with upgrade hint; storage counter matches `$group` backfill within one reconcile cycle; agent run debits credits and the bar shows remaining |
| **3 — Billing** | Stripe products/prices; checkout + portal + webhook; seat-quantity sync; grace/dunning; plan mutators | Card upgrade flips entitlements < 5 s after webhook; failed payment → banner → 5-day grace → Free gating with data intact; seat add/remove reflects in next invoice |
| **4 — Org-admin console** | §10 panels (overview, members, security, AI governance, audit viewer, billing) | Org admin completes invite→role-change→remove entirely in UI; every action visible in the audit viewer it ships with; CSV export audited |
| **5 — Platform dashboard** | §11: directory, overrides, suspension, impersonation via SupportGrant, health | Impersonation requires ticket ref, expires ≤ 30 min, user notified, both endpoints audited; suspended org's writes refuse at middleware |
| **6 — Enterprise** | KC Organizations + per-org SSO brokering + enforcement; SCIM (decision per §5.4); audit JSON API + hash chain; §12 analytics, sharing report, posture, license utilization; org agent governance | A test org logs in via external SAML IdP; SCIM create/deactivate round-trips to membership; sharing report matches a hand-audit of ACLs; credit sub-pools enforce |
| **7 — Hardening** | Load tests on counters; SOC 2 gap review against §9 retention/immutability; pen-test pass over `/admin` and org boundaries (the repo's audit history shows cross-tenant reads are the class to fear — DA1) | Counter contention test at 100 writes/s clean; org-boundary fuzz (member of A probing B) all-404/403; audit collection provably append-only (no code path + Mongo role without delete) |

Sequencing rationale: 0–3 unlock revenue with zero UI beyond upgrade buttons; 4 makes
Business sellable; 5 makes operating it sane; 6 makes Enterprise real. Estimated relative
weight: 2 and 6 are the heavy phases; 0, 3, 5 are small-to-medium; 1, 4 medium.

## 17. Testing strategy

Mirror the house style — the repo pins every fixed bug with a test and keeps evals for the
agent (`toolchoice_test.go`, `agenteval*`):

- **Boundary tests as first-class citizens**: member-of-A-touches-B matrix across every
  `/orgs/:id/*` route; platform routes as non-staff (expect 404, not 403); org-admin content
  override on a *personal* board must refuse.
- **Counter races**: parallel reserves at the limit — exactly `limit` succeed (the atomic
  `findOneAndUpdate` contract), property-tested.
- **Entitlement table tests**: every plan × every dimension, including the fail-closed default.
- **Webhook idempotency + replay**: same Stripe event twice → one state change.
- **Audit completeness**: a middleware-level assertion in tests that every mutating admin/org
  route emitted ≥ 1 audit event (the pattern that keeps "every write records its source"
  an invariant, like `Principal.RequestID` does today, `models.go:170-177`).
- **Delegation-reuse tests for SupportGrant**: expired grant refuses; destructive op refuses;
  every session start/end pair present in audit.

## 18. Risks & open questions

1. **Keycloak realm migration** — `realm-export.json` imports only on first boot; existing
   deployments need a scripted migration (the `allow-origin.sh` precedent). Applies to the new
   realm role now and Organizations later.
2. **KC native SCIM maturity** — preview-grade, version-dependent (~26.6+); the §5.4 fallback
   (own SCIM endpoint writing `org_memberships`) is the hedge. Decide at phase 6 entry.
3. **Effective-entitlement ambiguity** — max(personal, org) per dimension is simple but means
   an org downgrade can strand personal content over limit; the grace rule (reads never
   blocked) is the mitigation. Needs explicit UX copy.
4. **Seat-count drift** vs. Stripe quantity — reconcile job + invoice-preview check, not
   fire-and-forget updates.
5. **Multi-org agent charging** — a run on an org board by a member with a personal Pro plan:
   charge order is board's org pool → runner's personal credits (mirrors the existing
   `ChargedTo` runner/owner design, `element.go:145`). Locked here so implementation doesn't
   improvise.
6. **Pricing validation** — §6.2 numbers are competitively reasoned, not market-tested; ship
   behind a price table constant, expect one revision cycle. Grandfather early users on change.
7. **Deliberately out of scope**: DLP, eDiscovery/legal hold, data residency, BYOK, SIEM
   streaming, custom org roles, realm-per-tenant. Each is named on the Enterprise roadmap;
   none blocks the sellable tier.

---
---

# Part II — Implementation guide

How each phase lands in *this* codebase: the files to create, the files to touch, the code
shapes, and the wiring points — anchored to the DI graph in `cli/serve.go:98-123`, the router
in `transport/http/router.go`, and the enforcement choke points identified in Part I. Code
below is a design sketch, not final source; names follow house conventions (constructor-
injected services over `domain` interfaces, sentinel errors, ports in `domain/repository.go`).

## II.0 — Phase 0: Foundations

### II.0.1 Roles in the token (`auth/oidc.go`)

Extend the claims struct (`oidc.go:48-53`) and thread roles onto the Principal:

```go
type tokenClaims struct {
    Email             string   `json:"email"`
    Name              string   `json:"name"`
    PreferredUsername string   `json:"preferred_username"`
    Azp               string   `json:"azp"`
    RealmAccess       struct { Roles []string `json:"roles"` } `json:"realm_access"`
}
// Verify(): p.PlatformRoles = claims.RealmAccess.Roles  (copy, never alias)
```

`domain/models.go` Principal gains:

```go
PlatformRoles []string // realm roles from the verified token; empty for everyone but staff
func (p *Principal) HasPlatformRole(r string) bool
```

The WS ticket copy (`auth/tickets.go`) copies the field automatically since it copies the
whole Principal — verify with a test, since a stale copy here would let a revoked staffer
keep an admin socket.

**Keycloak side:** add to `keycloak/realm-export.json` a `"roles": {"realm": [{"name":
"platform-admin"}]}` block *and* ship `deploy/migrate-realm.sh` (kcadm.sh via the keycloak
container, same pattern as `deploy/allow-origin.sh`) because the export only imports on first
boot. `realm_access` is included in tokens by default; no mapper work needed.

### II.0.2 The `authz` package

```
backend/internal/authz/
  roles.go      // OrgRole int: OrgNone < OrgGuest < OrgMember < OrgAdmin < OrgOwner (+lattice methods, mirrors service.Role)
  resolver.go   // OrgResolver{memberships domain.OrgMembershipRepository}
  context.go    // WithMembership / MembershipFrom — request-scoped cache, echo context key "qomra.orgrole"
```

`Membership()` returns `OrgNone` on not-found (fail closed) and on `status != active`.
Constructed in `serve.go` beside `access := service.NewAccessResolver(elements)` (`serve.go:98`).

### II.0.3 Audit (`domain` + `service` + `repository/mongo`)

- `domain/audit.go`: `AuditEvent` struct (§9 schema), `AuditRepository interface { Insert(ctx,
  *AuditEvent) error; List(ctx, AuditFilter) ([]*AuditEvent, string, error) }` — **no Update,
  no Delete in the interface**; append-only is enforced by the port having no other verbs.
- `repository/mongo/audit.go`: insert + cursor list (`_id`-cursor pagination); indexes added in
  `repository/mongo/indexes.go` next to the agent indexes (`indexes.go:52-62` style).
- `service/audit_service.go`: `Emit(ctx, ev)` — stamps `at`, `requestId` from the Principal
  (`models.go:177`), actor block, and *never returns the insert error to the caller's main
  path*: audit failure logs at Error and increments a metric, but does not fail the user's
  action (availability over completeness for v1; revisit for Enterprise hash-chain mode, which
  flips to fail-closed).
- First call sites (small diffs, each one line + injection): `ShareService.CreateLink/
  RevokeLink/InviteEditor/RemoveEditor/TransferOwnership` (`share_service.go:159-375`),
  `AccountService.DeleteAccount` (`account_service.go:291`), `ExportBoard`/`ExportMyData`
  handlers, agent apply/revert (`agent/service.go` where `StateAt` is stamped).
- Content-level fan-in later rides `TransactionSubscriber.OnCommitted`
  (`domain/transaction.go:123` + `service/commitbus.go`) — do **not** wire per-op audit in
  phase 0; the journal already covers content history.

### II.0.4 Metrics

`github.com/prometheus/client_golang` + echo middleware. Register in `server.go` beside the
rate limiter (`server.go:64`); expose `e.GET("/metrics", echoprometheus.NewHandler())` in
`router.go` next to `/healthz` (`router.go:11`) — network-gated in compose (bind the port to
loopback / scrape over the docker network only; never through nginx). Counters to start:
HTTP by route/status, txn ops applied, agent runs by state, audit insert failures.

### II.0.5 Mongo replica set

`docker-compose.yml` mongo service: `command: ["mongod","--replSet","rs0","--keyFile",...]`
for prod-like; **for the dev single-node case a one-node replica set is enough** (transactions
work on a 1-node RS). Add an init sidecar or extend `migrateCmd` (`cli/ops.go:26`) to call
`rs.initiate()` when `MONGO_REPLICA_SET=rs0` is set and the node is uninitialized. Update
`MONGO_URI` to carry `?replicaSet=rs0`. The existing comment block at `docker-compose.yml:14-24`
(loopback-only, no auth) stays honest — enabling Mongo auth is a separate deliberate step it
already documents. `cli/backup.go`'s header (`backup.go:18-58`) gets its answer: document the
oplog-based PITR procedure in `deploy/`.

## II.1 — Phase 1: Orgs & membership

### II.1.1 Domain layer

```
backend/internal/domain/org.go
  Organization, OrgSettings, OrgMembership, OrgInvitation   (§3.1 shapes)
  OrgRole constants live in authz; domain stores the string.
  Sentinels: ErrOrgSuspended, ErrLastOwner, ErrAlreadyMember, ErrInviteExpired
backend/internal/domain/repository.go
  + OrgRepository, OrgMembershipRepository, OrgInvitationRepository (same style as :77-87)
```

### II.1.2 Repositories & indexes

`repository/mongo/orgs.go` (+ `repository/memory/orgs.go` for tests — the house pattern, cf.
`repository/memory/attachments.go`). Indexes in `indexes.go`: `organizations.slug` unique;
`org_memberships (orgId,userSub)` unique + `userSub`; `org_invitations (orgId,email)`,
TTL on `expiresAt` partial `acceptedAt: null`.

### II.1.3 `OrgService`

`service/org_service.go`, constructed in `serve.go` after `userSvc` (needs it for email
lookup via the existing `identity.FindUserByEmail` path, `user_service.go:130`):

```go
func NewOrgService(orgs, memberships, invitations, elements domain.…, users *UserService,
    entitle *entitle.Service /*phase 2*/, audit *AuditService, notifier Notifier,
    newID func() string, log *zap.Logger) *OrgService
```

Methods: `Create` (org row + founding `owner` membership + **org root board**: an element of
`TypeBoard` with `ACL{OwnerID: founder, OrgID: org.ID}` created through `ElementService` so it
inherits validation), `Invite` (pending row + email token hash + in-app notification if the
address already resolves to a user), `Accept` (validates hash + expiry, creates membership,
audits `org.member_joined`), `SetRole`/`Remove` (guards: `ErrLastOwner`; removing a member
does **not** touch board ACLs they were individually named on — org access and named access
stay orthogonal, matching strongest-grant-wins), `Transfer`, `Delete` (staged like
`AccountService.DeleteAccount:291-378`, reusing the `TenantPurger` registry with an org-scoped
variant).

Invitation email: there is no SMTP subsystem today (the settings store email prefs "for the
SMTP extension point", README §Settings). Phase 1 ships **link-based invites** (admin copies
the invite URL; in-app notification when the invitee has an account); SMTP delivery is the
existing deferred extension point, not a new dependency here.

### II.1.4 The ACL resolution arm (`service/access.go`)

The one change to the authorization core, in `roleFromACL` (`access.go:216`) — inserted after
the owner/editor checks, before link checks, so provenance stays honest:

```go
if acl.OrgID != "" && p.Sub != "" {
    switch orgRoleOf(p, acl.OrgID) {          // resolved once per request, carried on Principal
    case authz.OrgOwner, authz.OrgAdmin:      // admin sees org boards; WRITE override is NOT
        return RoleEdit, ProvMember           //   granted here — see §II.5.3 (audited path)
    case authz.OrgMember:
        return roleFromString(orgDefaultRole(p, acl.OrgID)), ProvMember // edit|view
    }
}
```

Implementation detail: `AccessResolver` must not grow a Mongo dependency on memberships per
element walk. Instead the auth middleware (or the org-context middleware for org routes)
resolves the caller's memberships **once** and stamps `Principal.OrgContext
map[string]OrgRoleInfo`; `roleFromACL` reads the map. Personal-only users carry a nil map and
pay zero cost. `ACLFor` (`access.go:279`) passes `OrgID` through to viewers — it is not a
credential, and the UI needs it to show the org badge.

**Delegation interaction:** the agent's grant checks are unchanged, but admission
(`agent/service.go:421-433`) currently requires `ProvMember` — org-derived roles report
`ProvMember`, so members can run the agent on org boards; the org-level agent policy check
slots into the same admission chain **before** the board's `MayRun` (`element.go:154`).

### II.1.5 HTTP + middleware

`transport/http/handlers_orgs.go`; router additions per §13. Org-context middleware for the
`/orgs/:id` group: load org (404 if absent), refuse `status: suspended` with `ErrOrgSuspended`
→ mapped in the error mapper (`server.go:257-293`), resolve membership, stash. Handlers
assert fine-grained role via `authz.Require*`.

### II.1.6 Frontend

`frontend/src/store/orgStore.ts` (memberships, active org, hydrated with `/me` bootstrap);
context switcher in the top bar; org badge on org boards; `api/orgs.ts` client. Board list
(`GET /boards`, `MyBoards`) gains an `orgId` filter param — server-side, an indexed query on
`ACL.OrgID`.

## II.2 — Phase 2: Entitlements & metering

### II.2.1 Package layout

```
backend/internal/entitle/
  catalogue.go   // Plan, Dimension, Feature enums + the versioned Catalogue table (§7.1)
  subject.go     // Subject{Kind: user|org, ID} + period keys ("u:<sub>:2026-08")
  service.go     // Reserve / Release / Peek / Effective(personal, org) — max-per-dimension
  errors.go      // ErrQuotaExceeded{Dim, Used, Limit, Plan}
backend/internal/domain/repository.go
  + UsageCounterRepository { ReserveInc(ctx, key, dim, n, limit) error; Inc; Dec; Get }
repository/mongo/usage.go   // the findOneAndUpdate implementation:
  filter: {_id: key, $or:[{dims.<d>: {$exists:false}}, {dims.<d>: {$lte: limit-n}}]}
  update: {$inc: {dims.<d>: n}, $setOnInsert:…}, upsert — miss ⇒ ErrQuotaExceeded
```

`Effective()` resolves the caller's entitlements once per request (max of personal plan and
active-org plan per dimension, §6.2) and is the only place that logic lives.

### II.2.2 Enforcement diffs (the five call sites)

1. **Elements/boards** — `TransactionService.ApplyWithMeta` pre-validation loop
   (`transaction_service.go:157-254`, beside `verifyDelegation` at `:246`): count net-new
   creates in the op list; one `Reserve(subject, DimElements, n)` **before** commit; a
   top-level `TypeBoard` create additionally reserves `DimTopBoards`. Deletes → `Release` on
   commit success (and restore → re-Reserve). Because reservation precedes commit and the
   commit can still fail, over-reservation is reconciled by the nightly recount (II.2.4) —
   deliberate: the failure mode is a briefly *stricter* limit, never a breached one.
2. **Storage** — `UploadService.Presign` (`upload_service.go:49-82`): after the per-file size
   check, `Reserve(owner, DimStorageBytes, req.Size)`; `Complete` (`:135`) adjusts by
   `actual-declared` (R2 HEAD / local stat); the GC of stale presigns (`ops.go:89-97`) and
   all delete/purge paths `Release`. Backfill: one `$group: {_id:"$ownerId", sum:"$size"}`
   over attachments in `migrateCmd`.
3. **Editors per board** — `ShareService.InviteEditor` (`share_service.go:159`):
   `Reserve(boardOwner, DimEditors, 1)` keyed to the owner's plan.
4. **Agent credits** — admission `checkBudget` (`agent/service.go:748-774`) gains a credits
   pre-check: `credits = ceil(remainingRunBudgetUSD / 0.02)` against `Peek`; the **debit**
   happens at run completion where `StateAt`/usage finalize, writing `credit_ledger` +
   `Inc(DimAgentCredits, credits)`. The USD caps stay as the hard backstop beneath credits.
   Charge order (risk §18.5): board's org pool first, runner's personal credits second —
   resolved in the same place `ChargedTo` resolves today (`agent/service.go:499-505`).
5. **Seats** — `OrgService.AddMember`: `Reserve(org, DimSeats, 1)` for `seatKind: full`;
   feeds Stripe quantity in phase 3.

### II.2.3 Error surface

Error mapper (`server.go:257-293`) gains: `ErrQuotaExceeded` → HTTP **402**,
envelope `{"error":{"code":402,"message":…,"quota":{"dim","used","limit","plan"}}}`.
Frontend api layer converts it to the upgrade dialog; 80% warnings ride a new
`NotificationKind` (`NotifyQuotaWarning`, `models.go:93-121`) emitted by `Reserve` when the
post-inc value crosses the threshold (dedup by `(subject, dim, period)`).

### II.2.4 Reconciliation

`migrateCmd` (or a new `qomranote reconcile`) recounts each dimension from source collections
and overwrites counters — drift self-heals; also the seed for existing users. Runs nightly in
prod via the compose scheduler / cron.

## II.3 — Phase 3: Billing

- `backend/internal/billing/`: `stripe.go` (client, `stripe-go/v82`), `webhook.go`,
  `sync.go` (seat quantity reconcile). New `domain.BillingRepository` + `billing_accounts`
  mongo repo.
- **Config** (`config/config.go`, house pattern incl. `refuseUnsafeProduction:152` update):
  `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `STRIPE_PRICE_PRO`, `STRIPE_PRICE_BUSINESS_SEAT`,
  `STRIPE_PRICE_CREDIT_PACK`, `BILLING_ENABLED` (blank keys ⇒ billing hidden, exactly the
  agent's nil-harness pattern — `docker-compose.yml:117-121` precedent).
- Routes: checkout/portal in the authed group; **webhook registered beside `/healthz`
  outside both auth groups** (`router.go:11`), verifying `Stripe-Signature`, storing the raw
  event (`stripe_events` collection, unique on event id — idempotency), 200 immediately,
  processing async via a small worker goroutine started in `serve.go` (the reminder service
  precedent, `serve.go:121`).
- Event handling: `checkout.session.completed` → set plan + audit `billing.plan_changed`;
  `customer.subscription.updated/deleted` → entitlement sync; `invoice.payment_failed` →
  `delinquentAt` + banner notification; a sweep downgrades entitlements after 5 days
  (`maintenanceSvc` gains the job, `serve.go:123`).
- Seat sync: membership changes call `billing.SyncSeats(orgID)` debounced; nightly reconcile
  compares Stripe quantity vs. `DimSeats` counter and alerts on drift (§18.4).

## II.4 — Phase 4: Org-admin console (frontend-heavy)

- `frontend/src/org/` route area (`OrgAdminLayout`, panels per §10), code-split via dynamic
  import; guard = membership role from orgStore *and* every API call re-checked server-side.
- Reuse house UI: `components/ui/Modal.tsx`, `Toaster.tsx`, settings-dialog table styles
  (`styles/settings.css`) so the console inherits theme/density/RTL for free.
- New endpoints already defined in §13; the only new backend work in this phase is the
  read-model queries (usage panel = `Peek`; members = membership join with last-active maxima
  from `transactions`/`agent_runs`).
- i18n: `org.*` key namespace added to both languages in `i18n/index.ts` in the same PR as
  each panel — the repo's Arabic-parity convention.

## II.5 — Phase 5: Platform dashboard

### II.5.1 Gating
`adminMiddleware` in `server.go`: `!p.HasPlatformRole("platform-admin")` ⇒ **404 via
`domain.ErrNotFound`** (indistinguishable from no-route). Group registered in `router.go`
after the `api` group.

### II.5.2 Directory & overrides
`service/admin_service.go` with its own narrow ports (`AdminUserReader` etc.) so the
cross-tenant reads are greppable in one file. `UserRepository` finally gains `SetPlan`
(`repository.go:77-87` addition). Every mutation → audit with `actor.platformRole` set.

### II.5.3 The org-admin content override (deferred from II.1.4)
Org admins reading/writing arbitrary *org* boards beyond their ACL role goes through a minted
grant, not a silent role bump: `POST /orgs/:id/override` mints a short-lived
`Delegation`-shaped grant scoped to one board subtree (`RootBoardID`), audited
`org.content_override`, surfaced to the board owner via notification. Reuses
`verifyDelegation` (`transaction_service.go:861`) untouched — the grant vocabulary already
refuses sharing/ACL mutation by construction (`delegation.go:16-19`).

### II.5.4 Impersonation
`domain/supportgrant.go` (sibling of Delegation): `TicketRef` required, `ReadOnly` default
true, TTL ≤ 30 min. Flow: mint → response carries opaque grant id → staff client sends
`X-Support-Grant` → `authMiddleware` (after token verify) looks up the live grant, swaps the
effective principal to `OnBehalfOf`, attaches the attenuation, stamps
`actor.impersonatedBy` into every audit event and `Source: "support"` onto transactions
(`transaction.go:139-146` gains the value). Write mode additionally requires the grant's
consequence ceiling — destructive ops (`ActionDelete`) refuse. Start/end audited; user
notified via existing notifier.

## II.6 — Phase 6: Enterprise

- **KC Organizations:** enable feature flag on the keycloak container
  (`--features=organizations` in the compose command); `deploy/migrate-realm.sh` grows org
  provisioning (create KC org, set domains, link IdP) driven from our
  `POST /orgs/:id/sso` config endpoint through the admin client — `auth/keycloak.go` gains
  its first new capabilities since the original five (CreateOrg, LinkIdP, ListMembers,
  MFAStatus), each behind the existing `KeycloakAdminSecret != ""` wiring guard
  (`serve.go:65-68`).
- **Token org claim:** map KC org membership into the token; `oidc.go` parses it as a hint
  only — Mongo memberships stay authoritative; a reconcile job (SSO-provisioned user's first
  login) creates the app membership with the org's default role, audited `org.member_joined`
  with `meta.source: "sso"`.
- **SSO enforcement:** org setting; enforced at the KC authentication-flow level (org-scoped
  browser flow requiring the IdP), not in our middleware — password logins for enforced orgs
  never mint tokens at all.
- **SCIM:** decision gate per §5.4. If self-built: `transport/http/handlers_scim.go`
  implementing the SCIM 2.0 Users subset (create/patch/deactivate/list) with a per-org bearer
  token, writing `org_memberships` + KC user create via admin client; provisioning events →
  audit.
- **Audit API + hash chain:** cursor list endpoint already exists from phase 0's repo; add
  per-org chain: `chain = sha256(prevChain || canonicalJSON(event))` computed in `Emit` under
  a per-org serialization (small mutex map or Mongo `findOneAndUpdate` on a chain-head doc);
  verification endpoint recomputes a range. Enterprise orgs flip audit to fail-closed.
- **Analytics/sharing report:** read-only aggregation services over existing collections —
  `transactions` (`indexes.go:33-45`), `agent_runs`, elements-with-links (`ACL.ViewLink`/
  `PublicEditLink` non-empty, joined to org boards via `ACL.OrgID`). Materialize nightly into
  `org_analytics_daily` to keep dashboards O(1); the sweep lives with the other maintenance
  jobs.

## II.7 — Phase 7: Hardening

- Boundary matrix test generator: table-driven over every `/orgs/:id/*` route × role ∈
  {non-member, guest, member, admin, owner, platform-admin-without-membership} — the
  house style of pinning classes of bug as tests (cf. `clone_escalation_test.go` pinning DA1).
- Counter contention: parallel `Reserve` property test (exactly `limit` succeed) against
  real Mongo in CI (the repo already runs Mongo-backed tests: `repository/mongo/*_test.go`).
- Audit append-only proof: a test asserting the Mongo user's privileges exclude
  delete/update on `audit_events` (prod role doc in `deploy/`), plus grep-level CI check that
  no code path calls anything but Insert/List on the repository.
- Load: `k6` scripts in `deploy/load/` for txn apply + presign + agent admission with quotas
  hot.

## II.8 — Cross-cutting reference

**New env/config** (all following `config.go` + compose plumbing patterns):
`MONGO_REPLICA_SET`, `BILLING_ENABLED`, `STRIPE_*` (4), `SCIM_ENABLED`, `AUDIT_FAIL_CLOSED`
(enterprise), `METRICS_ADDR` (loopback default). Placeholder-secret refusal extends
`refuseUnsafeProduction` (`config.go:152-174`).

**Feature gating:** plan features resolve through `entitle.Effective().Features` — a single
server-side gate; the client mirrors it from the bootstrap payload (`GET /me` response gains
`entitlements`), never decides on its own.

**Rollout order inside every phase:** repo/domain → service + tests → routes → UI → i18n →
docs (README + this file's checkboxes). Each phase lands as one PR series on a feature
branch; `make verify` (`Makefile:89-94`) stays the gate; new packages join `go vet`/test
coverage automatically via `./...`.

**Doc upkeep:** README gains an "Organizations & plans" section at phase 1, pricing table at
phase 3 GA; `AI_AGENT_ARCHITECTURE.md` gains the org-policy layer at phase 2 (agent credits)
and phase 6 (governance).
