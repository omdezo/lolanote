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

export interface User {
  id: string;
  keycloakSub: string;
  email: string;
  displayName: string;
  homeBoardId: string;
  plan: string;
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
