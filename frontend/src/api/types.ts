// Mirror of the backend domain model (PLAN.md §2). Everything on a board is
// a typed element; every mutation is a transaction of ops.

export type ElementType =
  | 'BOARD' | 'ALIAS' | 'COLUMN' | 'CARD' | 'LINK' | 'LINE' | 'IMAGE' | 'FILE'
  | 'COMMENT_THREAD' | 'TASK_LIST' | 'TASK' | 'CLONE' | 'SKETCH' | 'ANNOTATION'
  | 'COLOR_SWATCH' | 'DOCUMENT' | 'TABLE' | 'SKELETON' | 'UNKNOWN';

export type Section = 'CANVAS' | 'UNSORTED';

export interface Point { x: number; y: number }

export interface ElementLocation {
  parentId: string;
  section: Section;
  position: Point;
  index: number;
  width: number;
  height: number;
}

export interface ViewLink {
  token: string;
  allowFeedback: boolean;
  requireAccount: boolean;
  welcomeMessage?: string;
}

export interface ACL {
  ownerId: string;
  editors: string[];
  publicEditLink?: string;
  viewLink?: ViewLink;
}

export interface QElement {
  id: string;
  type: ElementType;
  location: ElementLocation;
  content: Record<string, any>;
  acl?: ACL;
  labelIds?: string[];
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  deletedAt?: string | null;
  deletedBy?: string;
  /** JN18. Trashing a container soft-deletes its whole subtree under ONE batch
   *  id, and every member's Restore restores the whole batch anyway — so the
   *  batch, not the member, is the unit a person actually acts on. The server
   *  has always sent this field; the panel threw it away and rendered 400 rows
   *  for one deletion. */
  trashBatchId?: string;
}

export type OpAction = 'create' | 'update' | 'move' | 'delete' | 'restore';

export interface Op {
  elementId: string;
  action: OpAction;
  changes?: Record<string, any>;
  undoChanges?: Record<string, any>;
}

export interface Txn {
  id: string;
  boardId: string;
  userId: string;
  clientId: string;
  ops: Op[];
  createdAt: string;
  /** Who caused this write: absent for a person, "AGENT" for a run. */
  origin?: string;
  /** The run that produced it, when origin is AGENT. */
  agentRunId?: string;
}

// ---- user settings (mirrors domain.UserSettings) ----

export type Theme = 'light' | 'dark' | 'system';
export type Language = 'en' | 'ar';

export interface AppearanceSettings {
  theme: Theme;
  accentColor: string;
  dotGrid: boolean;
  cardShadows: boolean;
  uiDensity: 'comfortable' | 'compact';
}

export interface PreferenceSettings {
  doubleClickCreates: 'note' | 'board' | 'none';
  wheelMode: 'pan' | 'zoom';
  snapToGrid: boolean;
  spellCheck: boolean;
  openBoardsWith: 'doubleClick' | 'singleClick';
  showHints: boolean;
}

export interface LocalizationSettings {
  language: Language;
  firstDayOfWeek: 0 | 1 | 6;
  dateFormat: 'auto' | 'dmy' | 'mdy' | 'ymd';
  timeFormat: '12h' | '24h';
}

export interface ToolbarSettings {
  hiddenTools: string[];
}

export interface NotificationSettings {
  mentions: boolean;
  comments: boolean;
  shares: boolean;
  assignments: boolean;
  boardChanges: boolean;
  reminders: boolean;
  emailEnabled: boolean;
  emailDigest: 'off' | 'daily' | 'weekly';
}

export interface PrivacySettings {
  showPresence: boolean;
  showEmailToOthers: boolean;
}

/**
 * AX30. Settings had five root-attribute channels — theme, density, dot grid,
 * card shadows, accent — and zero accessibility options. The product would let
 * a person turn off card shadows and could not let them turn off a
 * full-viewport camera tween the agent fires on every plan arrival.
 *
 * That is not a capability gap; the plumbing is identical. It is an intent gap,
 * and stating it as one surface with one owner is what turns a scattered list
 * of CSS fixes into something a person can actually reach.
 *
 * Every field defaults to `system`, meaning "ask the OS" — `prefers-reduced-
 * motion`, `prefers-contrast`, `prefers-reduced-transparency` — with an
 * explicit override, exactly as `theme` already works. Guessing wrong about
 * somebody's vestibular disorder is not a neutral default.
 */
export interface AccessibilitySettings {
  /** Camera tweens, panel slides, ghost fades. `system` reads prefers-reduced-motion. */
  motion: 'system' | 'full' | 'reduced';
  /** `more` thickens hairlines and drops glass. `system` reads prefers-contrast. */
  contrast: 'system' | 'normal' | 'more';
  /** A multiplier on the root font size, on top of the browser's own setting. */
  textScale: 100 | 115 | 130 | 150;
  /** Backdrop blur on the glass surfaces. `system` reads prefers-reduced-transparency. */
  transparency: 'system' | 'full' | 'reduced';
  /**
   * How much of a run the live regions speak.
   *
   *   outcome — only the four decision points (plan ready, applied, reverted, failed)
   *   normal  — those plus throttled progress, which is today's behaviour
   *   verbose — every staged action as it is staged
   *
   * No default can guess this: for one person forty staged actions is the
   * review, and for another it is forty interruptions.
   */
  announceVerbosity: 'outcome' | 'normal' | 'verbose';
  /**
   * AX20, WCAG 2.1.4. Bare single-character shortcuts — `Z` to fit the board,
   * Space to pan — off in one switch, keeping every modifier combo. Speech
   * input emits stray characters constantly and each stray `Z` moved the whole
   * canvas.
   */
  singleKeyShortcuts: boolean;
}

export interface UserSettings {
  appearance: AppearanceSettings;
  preferences: PreferenceSettings;
  localization: LocalizationSettings;
  toolbar: ToolbarSettings;
  notifications: NotificationSettings;
  privacy: PrivacySettings;
  accessibility: AccessibilitySettings;
  agent: AgentSettings;
}

/** Standing notes for the agent that apply to every board this person owns.
 *  A board's own rules layer on top and win where they disagree. */
export interface AgentSettings {
  instructions: string;
  /**
   * When this person was told, and agreed, that board content leaves this
   * deployment for a third-party model provider.
   *
   * Empty until they have been asked. The disclosure line on the composer is
   * permanent and is the thing they can re-read; this is the record that they
   * were asked BEFORE the first run rather than left to discover it in a
   * settings tab — which, for a filmmaker whose boards hold unreleased scripts
   * and client budgets, is the whole question.
   */
  processingAcknowledgedAt?: string;
}

export const DEFAULT_SETTINGS: UserSettings = {
  appearance: { theme: 'system', accentColor: '#5e5ce6', dotGrid: true, cardShadows: true, uiDensity: 'comfortable' },
  preferences: { doubleClickCreates: 'note', wheelMode: 'pan', snapToGrid: false, spellCheck: true, openBoardsWith: 'doubleClick', showHints: true },
  localization: { language: 'en', firstDayOfWeek: 1, dateFormat: 'auto', timeFormat: '12h' },
  toolbar: { hiddenTools: [] },
  notifications: { mentions: true, comments: true, shares: true, assignments: true, boardChanges: false, reminders: true, emailEnabled: false, emailDigest: 'off' },
  privacy: { showPresence: true, showEmailToOthers: true },
  accessibility: {
    motion: 'system', contrast: 'system', textScale: 100, transparency: 'system',
    announceVerbosity: 'normal', singleKeyShortcuts: true,
  },
  agent: { instructions: '', processingAcknowledgedAt: '' },
};

export interface User {
  id: string;
  keycloakSub: string;
  email: string;
  displayName: string;
  avatarUrl?: string;
  homeBoardId: string;
  plan: string;
  settings?: UserSettings;
}

export interface BreadcrumbEntry { id: string; title: string }

export interface BoardView {
  board: QElement;
  breadcrumb: BreadcrumbEntry[];
  role: 'owner' | 'edit' | 'feedback' | 'view' | 'none';
}

export interface TrashItem {
  element: QElement;
  deletedByMe: boolean;
  /** DL17's hard clause. The countdown a person reads has to be the server's
   *  own arithmetic, or a change to `domain.TrashRetention` silently makes
   *  every number in this UI wrong — and the number is the one thing standing
   *  between someone and a permanently gone sequence. Optional because the
   *  field is not served yet; `purgeDeadline()` says what it does when absent
   *  rather than pretending to know. */
  purgeAt?: string;
}

export interface QComment {
  id: string;
  threadId: string;
  authorId: string;
  body: string;
  reactions?: Record<string, string[]>;
  createdAt: string;
  editedAt?: string;
}

export interface Label {
  id: string; ownerId: string; name: string; color: string; usageCount: number;
}

export interface LinkMetadata {
  url: string; title: string; description: string;
  thumbnailUrl: string; siteName: string; embedType: string;
}

export interface PresignResult {
  attachmentId: string; uploadUrl: string; publicUrl: string;
}

export interface ShareState {
  ownerId: string;
  editors: string[];
  publicEditLink?: string;
  viewLink?: ViewLink;
}

export interface PresenceUser {
  clientId: string; sub: string; name: string;
  cursor?: Point; editing?: string;
}

export interface QNotification {
  id: string; kind: string; actorId: string; boardId?: string;
  elementId?: string; message: string; read: boolean; createdAt: string;
}

// ---- AI agent ---------------------------------------------------------------
// Mirrors backend/internal/agent. The client renders and adjusts a plan but
// never authors ops: adjustments are typed, and the server recompiles from its
// own stored plan.

export type AgentRunState =
  | 'CREATED' | 'PLANNING' | 'RUNNING' | 'PROPOSED' | 'APPLYING' | 'VERIFYING'
  | 'COMPLETED' | 'PARTIAL' | 'DISCARDED' | 'CANCELLED' | 'FAILED' | 'DENIED'
  | 'BUDGET_EXHAUSTED' | 'SECURITY_QUARANTINED' | 'REVERTED';

/** A free, model-less observation that a board wants attention. */
/**
 * Everything the composer needs on board open, in one read.
 *
 * `visible` and `elided` are the agent's real reach. The chip used to show the
 * board's own element count, which promised a reach the digest does not have —
 * on a busy board the agent sees a fraction and looks like it ignored you.
 */
export interface AgentBoardState {
  visible: number;
  elided: number;
  drift?: AgentDrift;
  active?: AgentActiveRun;
}

/**
 * A run already under way here. Yours carries an id and can be re-adopted;
 * a colleague's carries only the fact of it and who started it — their request
 * is their own words, and their plan is theirs to decide on.
 */
export interface AgentActiveRun {
  id?: string;
  state: AgentRunState;
  mine: boolean;
  actor?: string;
  startedAt: string;
}

export interface AgentDrift {
  kind: string;
  message: string;
  intent: string;
}

/** One change the agent actually made on a board. */
export interface AgentAuditEntry {
  runId: string;
  intent: string;
  at: string;
  ops: number;
  reverted: boolean;
  costUsd: number;
  state: AgentRunState;
  /**
   * What the run READ, whatever its outcome.
   *
   * The audit answers "what did the agent change" and never "what did it
   * read", so a run that walked 340 elements across four boards, transmitted
   * two PDFs and was then discarded because the plan was wrong is — from the
   * audit's point of view — something that never happened. The person cannot
   * answer "was the client's contract ever sent to a model", and neither can
   * the operator.
   *
   * Optional because the server writes it from the scope compiler and older
   * deployments have nothing to send; the read surface simply stays empty
   * rather than inventing a number.
   */
  reads?: {
    elementCount?: number;
    boardIds?: string[];
    attachmentIds?: string[];
    urlsFetched?: string[];
    digestBytes?: number;
  };
}

export type AgentActionKind =
  | 'create_board' | 'create_column' | 'create_note' | 'create_todo'
  | 'create_link' | 'move_element' | 'rename' | 'set_note_text' | 'delete_element'
  // Attribute edits: they change how something is filed without moving it.
  | 'apply_label' | 'set_color' | 'set_task_done'
  // Relationships, grids and cross-board references.
  | 'connect' | 'create_table' | 'clone_here'
  // Composition and repair.
  | 'place' | 'comment' | 'create_heading' | 'resize'
  | 'set_assignee' | 'set_reminder' | 'add_tasks' | 'place_file'
  // Authoring the types the toolbar has always had.
  | 'write_document' | 'add_color' | 'link_board'
  // Revising what already exists, rather than building a second one beside it.
  | 'edit_table' | 'set_url' | 'set_caption' | 'collapse'
  | 'duplicate' | 'convert';

/** Server-computed geometry, so preview and commit cannot disagree. */
export interface AgentBox { x: number; y: number; width: number }

export interface AgentAction {
  seq: number;
  kind: AgentActionKind;
  elementId: string;
  because?: string;
  labelId?: string;
  color?: string;
  done?: boolean;
  parentId?: string;
  title?: string;
  text?: string;
  url?: string;
  tasks?: string[];
  section?: string;
  position?: AgentBox;
  summary: string;
  /** A connector's endpoints. Either id may name an element already on the
   *  board or one this same plan creates — the preview has to resolve both, or
   *  the diagram capability reviews as a set of loose cards with no arrows. */
  fromId?: string;
  toId?: string;
  /** What a connector MEANS — decides how the line is drawn. */
  relation?: 'leads_to' | 'depends_on' | 'blocks' | 'related';
  /** Where this lands, by name — "Pricing", "Unsorted". Empty when it goes
   *  straight onto the board, where the ghost already shows exactly where. */
  destination?: string;
  /** What a `duplicate` will write: one entry per element in the copied
   *  subtree, the first being the copy of the element itself.
   *
   *  A duplicate is ONE action that creates many elements, so the ghost cannot
   *  count its contents the way it counts every other container — nothing is
   *  parented to it in the plan. Without this the preview shows "Copy of
   *  Development" as an empty box, and the person approves three cards they
   *  were never shown. */
  copies?: { newId: string; sourceId: string; parentId: string }[];
  /**
   * How much of a reviewer's attention this row deserves, decided server-side.
   *
   * The client's own derivation cannot see a nested board it has not loaded and
   * conservatively bands everything there as existing material — safe, and it
   * meant a run that filed into four sub-boards collapsed nothing at all.
   */
  risk?: 'touches-existing' | 'structural' | 'additive';
}

export interface AgentPlan {
  /** Board content repeatedly tried to steer this run; it never auto-applies. */
  quarantined?: boolean;
  /** Set when the run stopped to ask rather than guess. No actions accompany it. */
  question?: { text: string; options?: string[] };
  actions: AgentAction[];
  summary?: string;
  /**
   * One sentence naming what this plan proposes, for the assertive live region.
   *
   * AX38: the announcement is part of the plan contract, not something React
   * composes. A frontend that builds the sentence from counts can only ever say
   * "12 changes"; the planner knows it is "12 changes, 4 new, 2 to trash, in
   * Pre-Production". Optional because the server does not populate it yet — see
   * AgentLive, which prefers it, falls back to `summary`, and composes counts
   * only as a last resort.
   */
  announce?: string;
  /** What the harness dropped or cut short — budgets, step limits, ignored references. */
  notes?: string[];
  /** Parts of the request this plan does not carry out, in the agent's own words. */
  unmet?: Array<{ request: string; why?: string }>;
  /** One sentence the agent suggests adding to this board's rules, offered only
   *  when it had to be corrected. Proposed — never written without approval. */
  proposedRule?: string;
}

export interface AgentVerdict {
  passed: boolean;
  criteria: Array<{ name: string; passed: boolean; detail?: string; fatal: boolean }>;
}

export interface AgentUsage {
  inputTokens: number; outputTokens: number; cachedTokens: number;
  costUsd: number; calls: number;
}

export interface AgentRun {
  id: string;
  state: AgentRunState;
  reason?: string;
  rev: number;
  task: {
    intent: string;
    rootBoardId: string;
    scope: AgentScope;
    selectionIds?: string[];
    autonomy: AgentAutonomy;
  };
  plan?: AgentPlan;
  verdict?: AgentVerdict;
  transactionIds?: string[];
  /** Elements from this run that have since been undone one at a time. */
  revertedElementIds?: string[];
  /** The run this one picks up from, when it was started with Continue. */
  continuesRunId?: string;
  usage: AgentUsage;
  createdAt: string;
  updatedAt: string;
  completedAt?: string;
  /** JN21. When an unanswered PROPOSED run stops holding its delegation. The
   *  server has computed and stored this since proposal expiry shipped; no
   *  surface has ever shown it, so a person reviewing forty staged actions had
   *  no way to know the review itself had a clock on it. */
  proposalExpiresAt?: string;
  /** JN8's honesty clause. Set when the inverse of this run can no longer be
   *  replayed — a target was permanently deleted, usually by Empty Trash
   *  (JN20). The ledger renders the REASON instead of a button that 500s. */
  revertBlockedReason?: string;
}

export interface AgentEvent {
  id: string;
  runId: string;
  sequence: number;
  type: string;
  message?: string;
  data?: Record<string, any>;
  at: string;
}

export type AgentScope = 'board' | 'unsorted' | 'selection';
export type AgentAutonomy = 'preview' | 'auto';

/** The closed set of edits a human may make to a plan. */
export type AgentAdjustment =
  | { kind: 'drop'; seq: number }
  | { kind: 'retitle'; seq: number; value: string }
  | { kind: 'retext'; seq: number; value: string }
  /** Send this change somewhere else. `value` is a container id, or the board. */
  | { kind: 'reparent'; seq: number; value: string };

export interface AgentCapabilities {
  enabled: boolean;
  /** Configured is not the same as able to answer. A rotated or rate-limited
   *  provider key used to leave the composer accepting intents it could not
   *  serve, one failed run at a time. */
  providerHealthy?: boolean;
  can: string[];
  cannot: string[];
  /** maxCostUsd is the ceiling on one run — stated so cost is knowable before, not only after. */
  /** maxCostUsd is per PASS; a run may have maxPasses of them, each with a
   *  fresh budget. The honest ceiling for a run is the product. */
  limits: { maxActions: number; maxSteps: number; maxCostUsd?: number; maxPasses?: number };
  /** Today's allowance, so the ceiling is plannable rather than discovered by refusal. */
  budget?: { spentTodayUsd: number; dailyCapUsd: number };
  /**
   * Who actually processes the board.
   *
   * Nowhere in the product did it say that board content is transmitted to a
   * third-party model provider, and the personification works against the
   * person noticing: the model is called "Qomra", a name in the user's own
   * language presented as part of the app, and a reasonable person reads that
   * as a feature of the software they installed. It is a courier.
   *
   * Served, never a client constant: a self-hosted deployment must state its
   * own truth, and a hard-coded name becomes a false claim on the first
   * deployment that switches keys.
   */
  processing?: {
    provider?: string;
    model?: string;
    region?: string;
    retentionPolicyUrl?: string;
  };
}
