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

export interface UserSettings {
  appearance: AppearanceSettings;
  preferences: PreferenceSettings;
  localization: LocalizationSettings;
  toolbar: ToolbarSettings;
  notifications: NotificationSettings;
  privacy: PrivacySettings;
}

export const DEFAULT_SETTINGS: UserSettings = {
  appearance: { theme: 'system', accentColor: '#5e5ce6', dotGrid: true, cardShadows: true, uiDensity: 'comfortable' },
  preferences: { doubleClickCreates: 'note', wheelMode: 'pan', snapToGrid: false, spellCheck: true, openBoardsWith: 'doubleClick', showHints: true },
  localization: { language: 'en', firstDayOfWeek: 1, dateFormat: 'auto', timeFormat: '12h' },
  toolbar: { hiddenTools: [] },
  notifications: { mentions: true, comments: true, shares: true, assignments: true, boardChanges: false, reminders: true, emailEnabled: false, emailDigest: 'off' },
  privacy: { showPresence: true, showEmailToOthers: true },
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

export interface TrashItem { element: QElement; deletedByMe: boolean }

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
