import { useCallback, useEffect, useState } from 'react';
import { api, setSharePassword, setShareToken } from './api/client';
import { initAuth, isAuthenticated } from './auth/keycloak';
import { connectBoard, disconnect } from './realtime/socket';
import { useBoard } from './store/boardStore';
import { useView } from './store/viewStore';
import { BoardCanvas } from './canvas/BoardCanvas';
import { Topbar } from './components/Topbar';
import { Toolbar } from './components/Toolbar';
import { UnsortedTray } from './components/panels/UnsortedTray';
import { TrashPanel } from './components/panels/TrashPanel';
import { SearchOverlay } from './components/panels/SearchOverlay';
import { ShareDialog } from './components/panels/ShareDialog';
import { PasswordGate } from './components/panels/PasswordGate';
import { ErrorBoundary, Toaster } from './components/ui/Toaster';
import { PromptHost } from './components/ui/Prompt';
import { BoardStylePopoverHost } from './components/ui/BoardStylePopover';
import { ContextMenuHost } from './components/ui/ContextMenu';
import { LabelPopoverHost } from './components/ui/LabelPopover';
import { BoardsDrawer } from './components/panels/BoardsDrawer';
import { TemplatePicker } from './components/panels/TemplatePicker';
import { SettingsDialog } from './components/panels/SettingsDialog';
import { copySelection, cutSelection, pasteFromClipboardData } from './store/clipboard';
import { useLabels } from './store/labels';
import { useSettings } from './store/settingsStore';

export type PanelKind = 'none' | 'unsorted' | 'trash' | 'search' | 'share' | 'boards' | 'templates' | 'settings';

export default function App() {
  const [booted, setBooted] = useState(false);
  const [panel, setPanel] = useState<PanelKind>('none');
  const [publicView, setPublicView] = useState(false);
  const [needPassword, setNeedPassword] = useState<{ token: string; board: string } | null>(null);
  const [welcome, setWelcome] = useState('');
  const { setUser, openBoard, readOnly, undo, redo } = useBoard();
  const boardId = useBoard((s) => s.boardId);
  const spellCheck = useSettings((s) => s.settings.preferences.spellCheck);

  // Open a shared board via its token; handle password-gated links.
  const openShared = useCallback(async (token: string, board: string, password?: string) => {
    setShareToken(token);
    if (password) setSharePassword(password);
    try {
      const resolved = await api.resolveSharedLink(token);
      setWelcome(resolved.welcomeMessage || '');
      const target = resolved.boardId || board;
      // Logged-in editors get the full editor; everyone else, read-only.
      if (isAuthenticated()) {
        try {
          const me = await api.me();
          setUser(me);
          useSettings.getState().hydrate(me);
        } catch { /* editor bootstrap optional */ }
      }
      setPublicView(!isAuthenticated() || resolved.kind === 'view');
      await openBoard(target);
      if (isAuthenticated()) await connectBoard(target);
      setNeedPassword(null);
      setBooted(true);
    } catch (err: any) {
      if (err?.status === 401) {
        setNeedPassword({ token, board });
        setBooted(true);
      } else {
        throw err;
      }
    }
  }, [openBoard, setUser]);

  // Boot: a share link opens the shared board (optional auth); otherwise the
  // normal authenticated flow bootstraps the user and opens Home.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const params = new URLSearchParams(window.location.search);
      const share = params.get('share');
      const sharedBoard = params.get('board');

      if (share && sharedBoard) {
        await initAuth('optional'); // don't force login for public links
        if (cancelled) return;
        await openShared(share, sharedBoard);
        return;
      }

      await initAuth('required');
      const me = await api.me();
      if (cancelled) return;
      setUser(me);
      useSettings.getState().hydrate(me);
      void useLabels.getState().load();
      await openBoard(me.homeBoardId);
      await connectBoard(me.homeBoardId);
      setBooted(true);
    })().catch((err) => console.error('boot failed', err));
    return () => { cancelled = true; disconnect(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Board navigation: swap store contents and realtime room together.
  const navigate = useCallback(async (id: string) => {
    await openBoard(id);
    await connectBoard(id);
  }, [openBoard]);

  // Global keyboard map (§5): undo/redo, search, duplicate, mode escapes.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const inEditor = (e.target as HTMLElement)?.closest?.('input, textarea, [contenteditable="true"]');
      const mod = e.ctrlKey || e.metaKey;
      if (mod && e.key.toLowerCase() === 'z' && !inEditor) {
        e.preventDefault();
        if (e.shiftKey) redo(); else undo();
      } else if (mod && e.key.toLowerCase() === 'f') {
        e.preventDefault();
        setPanel((p) => (p === 'search' ? 'none' : 'search'));
      } else if (mod && e.key.toLowerCase() === 'd' && !inEditor) {
        e.preventDefault();
        const state = useBoard.getState();
        const ids = Array.from(state.selection);
        if (ids.length) {
          // Merge the server-created copies straight into the store (no board
          // refetch) and move the selection onto them.
          void Promise.all(ids.map((id) => api.duplicate(id))).then((results) => {
            const created = results.flat();
            state.upsertElements(created);
            state.select(results.map((r) => r[0]?.id).filter(Boolean) as string[]);
          });
        }
      } else if (mod && e.key.toLowerCase() === 'c' && !inEditor) {
        copySelection();
      } else if (mod && e.key.toLowerCase() === 'x' && !inEditor) {
        cutSelection();
      } else if (mod && e.key.toLowerCase() === 'a' && !inEditor) {
        e.preventDefault();
        const state = useBoard.getState();
        state.select(
          Object.values(state.elements)
            .filter((el) => el.location.parentId === state.boardId && !el.deletedAt && el.type !== 'LINE')
            .map((el) => el.id),
        );
      } else if (e.key === 'Escape') {
        setPanel('none');
        useView.getState().setDrawMode(false);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [undo, redo]);

  // Native paste (Ctrl/⌘+V outside editors): the ClipboardEvent hands us
  // files and text without permission prompts — screenshots and copied
  // photos become IMAGE cards at the last pointer position.
  useEffect(() => {
    const onPaste = (e: ClipboardEvent) => {
      const inEditor = (e.target as HTMLElement)?.closest?.('input, textarea, [contenteditable="true"]');
      if (inEditor || !e.clipboardData) return;
      e.preventDefault();
      const pt = useView.getState().lastPointer;
      void pasteFromClipboardData(e.clipboardData, pt.x, pt.y);
    };
    window.addEventListener('paste', onPaste);
    return () => window.removeEventListener('paste', onPaste);
  }, []);

  // Global hosts render regardless of view mode.
  const hosts = (
    <>
      <Toaster />
      <PromptHost />
      <ContextMenuHost />
      <LabelPopoverHost />
      <BoardStylePopoverHost />
    </>
  );

  if (needPassword) {
    return (
      <>
        <PasswordGate onSubmit={(pw) => openShared(needPassword.token, needPassword.board, pw)} />
        {hosts}
      </>
    );
  }

  if (!booted) {
    return (
      <div className="boot-screen">
        <div className="boot-mark">Q</div>
        <div className="boot-title">Qomra<em>Note</em></div>
        <div className="spinner" />
        <div className="boot-sub">Get organized. Stay creative.</div>
      </div>
    );
  }

  // Public read-only view: no rail, no editing chrome — just the board.
  if (publicView && readOnly) {
    return (
      <ErrorBoundary>
        <div className="app">
          <div className="public-bar">
            <div className="brand">
              <div className="brand-mark">Q</div>
              <div className="brand-name">Qomra<em>Note</em></div>
            </div>
            {welcome && <div className="public-welcome">{welcome}</div>}
            <div className="public-tag">Read-only</div>
          </div>
          <div className="workspace">
            <div className="canvas-region">
              <BoardCanvas navigate={navigate} />
            </div>
          </div>
        </div>
        {hosts}
      </ErrorBoundary>
    );
  }

  return (
    <ErrorBoundary>
      <div className="app" spellCheck={spellCheck}>
        <Topbar navigate={navigate} panel={panel} setPanel={setPanel} />
        <div className="workspace">
          {!readOnly && <Toolbar />}
          <div className="canvas-region">
            <BoardCanvas navigate={navigate} />
            {panel === 'unsorted' && <UnsortedTray onClose={() => setPanel('none')} />}
            {panel === 'trash' && <TrashPanel onClose={() => setPanel('none')} navigate={navigate} />}
          </div>
          {panel === 'boards' && <BoardsDrawer onClose={() => setPanel('none')} navigate={navigate} />}
          {panel === 'search' && <SearchOverlay onClose={() => setPanel('none')} navigate={navigate} />}
          {panel === 'share' && boardId && <ShareDialog boardId={boardId} onClose={() => setPanel('none')} />}
          {panel === 'templates' && <TemplatePicker onClose={() => setPanel('none')} />}
          {panel === 'settings' && <SettingsDialog onClose={() => setPanel('none')} />}
        </div>
      </div>
      {hosts}
    </ErrorBoundary>
  );
}
