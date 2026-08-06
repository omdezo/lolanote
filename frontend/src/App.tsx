import { useCallback, useEffect, useRef, useState } from 'react';
import { api, setSharePassword, setShareToken } from './api/client';
import { useT } from './i18n';
import { currentSub, initAuth, isAuthenticated } from './auth/keycloak';
import { setCacheOwner } from './lib/boardCache';
import { connectBoard, disconnect } from './realtime/socket';
import { useBoard } from './store/boardStore';
import { navigateToBoard } from './store/navigation';
import { getLastPointer, ownsEscape, useView } from './store/viewStore';
import { BoardCanvas } from './canvas/BoardCanvas';
import { Topbar } from './components/Topbar';
import { Toolbar } from './components/Toolbar';
import { UnsortedTray, captureToUnsorted } from './components/panels/UnsortedTray';
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
import { AgentBar, useAgentShell } from './agent/AgentBar';
import { useAgent } from './agent/agentStore';

export type PanelKind = 'none' | 'unsorted' | 'trash' | 'search' | 'share' | 'boards' | 'templates' | 'settings';

/** This app's entry on the shared Escape stack (viewStore.overlays). */
const PANEL_OVERLAY = 'panel';

export default function App() {
  const t = useT();
  const [booted, setBooted] = useState(false);
  const [bootError, setBootError] = useState('');
  const [panel, setPanel] = useState<PanelKind>('none');
  const [publicView, setPublicView] = useState(false);
  const [needPassword, setNeedPassword] = useState<{ token: string; board: string } | null>(null);
  const [welcome, setWelcome] = useState('');
  const { setUser, openBoard, readOnly, undo, redo } = useBoard();
  const boardId = useBoard((s) => s.boardId);
  const boardTitle = useBoard((s) => s.boardTitle);
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
        setCacheOwner(currentSub()); // empty for an anonymous visitor, which is right
        await openShared(share, sharedBoard);
        return;
      }

      await initAuth('required');
      // Before the first board is opened, so the local mirror is keyed to this
      // account from the very first read: switching users on a shared machine
      // must not paint the previous person's boards, even for the instant
      // before reconcile.
      setCacheOwner(currentSub());
      const me = await api.me();
      if (cancelled) return;
      setUser(me);
      useSettings.getState().hydrate(me);
      void useLabels.getState().load();
      await openBoard(me.homeBoardId);
      await connectBoard(me.homeBoardId);
      setBooted(true);
    })().catch((err) => {
      // A failed boot used to leave the splash spinning forever with the cause
      // only in the console. Surface it, so the state is legible and there is
      // a way out.
      console.error('boot failed', err);
      if (!cancelled) setBootError(err?.message || t('boot.error'));
    });
    return () => { cancelled = true; disconnect(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Board navigation: swap store contents and realtime room together.
  //
  // AX18. Opening another board replaces the entire canvas, so this is a page
  // navigation in every sense except the URL — and it produced no title change,
  // no focus change, no announcement, and landed in a document with no headings
  // to orient by. Focus moves to the board heading once the swap resolves,
  // which is what makes the announcement of the new board's name happen at all
  // and what gives a keyboard user a defined place to be. `document.title` is
  // set in the store, next to `boardTitle`, so the two cannot disagree.
  const headingRef = useRef<HTMLHeadingElement>(null);
  const navigate = useCallback(async (id: string) => {
    await navigateToBoard(id);
    headingRef.current?.focus();
  }, []);

  // Global keyboard map (§5): undo/redo, search, duplicate, mode escapes.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const inEditor = (e.target as HTMLElement)?.closest?.('input, textarea, [contenteditable="true"]');
      const mod = e.ctrlKey || e.metaKey;
      // Quick capture. It lived inside UnsortedTray, so the gesture documented
      // as working "anywhere" unmounted with the panel: it fired only while the
      // tray you were trying not to open was already open. The capture inbox is
      // the thing a phone is for, and its gesture was the least reliable
      // surface in the app.
      if (mod && e.key === 'Enter' && !inEditor) {
        e.preventDefault();
        void captureToUnsorted();
        return;
      }
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
        // Only when this panel is the innermost thing open. This listener and
        // the agent shell's both sat on `window` with no propagation guard, so
        // one Escape ran both: closing Search with a proposal pending also
        // discarded the plan — paid for, unrecoverable, unannounced.
        if (!ownsEscape(PANEL_OVERLAY)) return;
        setPanel('none');
        useView.getState().setDrawMode(false);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [undo, redo]);

  // The side panels take their turn on the overlay stack like every other
  // dismissable surface, so whichever opened last is the one Escape closes.
  useEffect(() => {
    const v = useView.getState();
    if (panel === 'none') v.popOverlay(PANEL_OVERLAY);
    else v.pushOverlay(PANEL_OVERLAY);
  }, [panel]);

  // Native paste (Ctrl/⌘+V outside editors): the ClipboardEvent hands us
  // files and text without permission prompts — screenshots and copied
  // photos become IMAGE cards at the last pointer position.
  useEffect(() => {
    const onPaste = (e: ClipboardEvent) => {
      const inEditor = (e.target as HTMLElement)?.closest?.('input, textarea, [contenteditable="true"]');
      if (inEditor || !e.clipboardData) return;
      e.preventDefault();
      const pt = getLastPointer();
      void pasteFromClipboardData(e.clipboardData, pt.x, pt.y);
    };
    window.addEventListener('paste', onPaste);
    return () => window.removeEventListener('paste', onPaste);
  }, []);

  // Agent keyboard map and canvas auto-framing live together.
  useAgentShell();

  // Ask the server what the agent can do here. A deployment without a model
  // provider reports enabled:false and every entry point stays hidden, so
  // there are no dead buttons rather than buttons that fail.
  useEffect(() => {
    if (isAuthenticated()) void useAgent.getState().loadCapabilities();
  }, [booted]);

  // A free, model-less check of whether this board wants tidying. Re-run per
  // board, never on a timer: a hint that reappears while you work is a nag.
  useEffect(() => {
    if (booted && boardId) void useAgent.getState().loadBoardState();
  }, [booted, boardId]);

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
        {bootError ? (
          <>
            <div className="boot-error" role="alert">{bootError}</div>
            <button className="boot-retry" onClick={() => window.location.reload()}>{t('boot.retry')}</button>
          </>
        ) : (
          <>
            <div className="spinner" />
            <div className="boot-sub">{t('boot.tagline')}</div>
          </>
        )}
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
            <div className="public-tag">{t('boot.readOnly')}</div>
          </div>
          <div className="workspace">
            <main className="canvas-region" aria-labelledby="board-heading">
              <h1 className="sr-only" id="board-heading">{boardTitle}</h1>
              <BoardCanvas navigate={navigate} />
            </main>
          </div>
        </div>
        {hosts}
      </ErrorBoundary>
    );
  }

  return (
    <ErrorBoundary>
      {/* AX18. The document had no landmarks at all: zero <main>, zero
          <header>, zero <h1>, and one <nav> (buried in Settings). "Go to the
          top and start over" — the recovery every screen-reader user falls back
          on — was not a usable recovery, because the top of this document had
          nothing in it. Skip links first, so the first Tab from a fresh load
          offers the canvas and the agent rather than eleven topbar buttons. */}
      <a className="skip-link" href="#board-heading">{t('a11y.skipToBoard')}</a>
      <a className="skip-link" href="#agent-shell-anchor">{t('a11y.skipToAgent')}</a>
      <div className="app" spellCheck={spellCheck}>
        <Topbar navigate={navigate} panel={panel} setPanel={setPanel} />
        <div className="workspace">
          {!readOnly && <Toolbar />}
          <main className="canvas-region" aria-labelledby="board-heading">
            {/* The board's name as a real heading. It is the only h1 in the
                document and it is where navigate() puts focus, so "what am I
                looking at" has an answer that is spoken rather than painted.
                tabIndex -1 because it is a focus target, not a tab stop. */}
            <h1 className="sr-only" id="board-heading" ref={headingRef} tabIndex={-1}>{boardTitle}</h1>
            <BoardCanvas navigate={navigate} />
            {panel === 'unsorted' && <UnsortedTray onClose={() => setPanel('none')} />}
            {panel === 'trash' && <TrashPanel onClose={() => setPanel('none')} navigate={navigate} />}
          </main>
          {panel === 'boards' && <BoardsDrawer onClose={() => setPanel('none')} navigate={navigate} />}
          {panel === 'search' && <SearchOverlay onClose={() => setPanel('none')} navigate={navigate} />}
          {panel === 'share' && boardId && <ShareDialog boardId={boardId} onClose={() => setPanel('none')} />}
          {panel === 'templates' && <TemplatePicker onClose={() => setPanel('none')} />}
          {panel === 'settings' && <SettingsDialog onClose={() => setPanel('none')} />}
        </div>
      </div>
      {/* Agent surfaces sit outside the workspace so the composer, the run
          panel, and the decision bar float above the canvas without competing
          with the side panels for layout. */}
      {!readOnly && <AgentBar />}
      {hosts}
    </ErrorBoundary>
  );
}
