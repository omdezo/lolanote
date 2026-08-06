// One dialog primitive, for the six surfaces that each reimplemented half of
// one.
//
// AX11. `Prompt`, `SearchOverlay`, `ShareDialog`, `SettingsDialog`,
// `TemplatePicker` and the document editor all rendered
// `.modal-backdrop`/`.modal` with no `role="dialog"`, no `aria-modal`, no
// `aria-labelledby` on the `<h3>` that was right there, no focus trap and no
// focus restore. Tab from inside any of them walked into the canvas behind,
// which the screen reader still read because nothing was inert.
//
// The file that replaced the native dialogs says in its own header that it did
// so because they are "unstyled, focus-stealing, unthemeable" — and in doing so
// it dropped the three things they did correctly.
//
// The specific casualty was agent-shaped. Escape and Enter were handled only on
// Prompt's single-line branch; the `multiline` branch had no `onKeyDown` at
// all, and `AgentAsk` opens exactly that branch for the board's
// `agentInstructions` — which two other items treat as a governance surface. So
// the one dialog a keyboard user could not close was the board's rules editor.
// Escape lives on the CONTAINER here, which is why that branch was missed.
import { useEffect, useId, useLayoutEffect, useRef } from 'react';
import { ownsEscape, useView } from '../../store/viewStore';

interface Props {
  /** Heading text. Becomes the dialog's accessible name via aria-labelledby. */
  title: React.ReactNode;
  /** Called on Escape, on backdrop click, and by the close button. */
  onClose(): void;
  /** Distinct id on the shared Escape stack, so nested overlays resolve. */
  overlayId: string;
  /** Extra classes on the `.modal` box (e.g. a width variant). */
  className?: string;
  style?: React.CSSProperties;
  backdropStyle?: React.CSSProperties;
  /** Rendered to the right of the title in the head row — usually a close button. */
  headExtra?: React.ReactNode;
  children: React.ReactNode;
}

/** Everything inside the dialog that can take focus, in DOM order. */
const FOCUSABLE =
  'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), '
  + 'textarea:not(:disabled), [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';

/**
 * Hide everything that is not this dialog from pointers and screen readers.
 *
 * NOT `inert` on `.app`: these dialogs render INSIDE the app tree (Search,
 * Share, Templates and Settings are all children of `.workspace`), so inerting
 * the app would inert the dialog with it — an accessibility fix that disables
 * the thing it is protecting. Instead, walk up from the dialog to `<body>` and
 * inert every SIBLING along the way, which is what "everything except this
 * subtree" actually means in a tree.
 *
 * Only nodes this call marked are unmarked on cleanup, so a prompt opened from
 * inside Settings composes: the prompt inerts Settings, and closing the prompt
 * gives Settings back without disturbing whatever Settings itself inerted.
 */
function makeBackgroundInert(box: HTMLElement | null): () => void {
  if (!box) return () => { /* nothing to hide from */ };
  const marked: HTMLElement[] = [];
  let node: HTMLElement | null = box;
  while (node && node !== document.body) {
    const parent: HTMLElement | null = node.parentElement;
    if (!parent) break;
    for (const sibling of Array.from(parent.children)) {
      if (sibling === node || !(sibling instanceof HTMLElement)) continue;
      if (sibling.hasAttribute('inert')) continue; // already hidden by an outer dialog
      sibling.setAttribute('inert', '');
      marked.push(sibling);
    }
    node = parent;
  }
  return () => { for (const el of marked) el.removeAttribute('inert'); };
}

/**
 * The dialog behaviour without the dialog chrome.
 *
 * Two surfaces cannot use `<Modal>` as written: Settings is a two-column
 * layout with its own nav instead of a `.modal-head`, and the document editor
 * is a full-bleed writing view. They still need every one of these guarantees,
 * so the behaviour lives in a hook and `Modal` is its most common caller —
 * rather than a primitive that fits four of six and gets copy-pasted twice.
 */
export function useDialog(overlayId: string, onClose: () => void) {
  const boxRef = useRef<HTMLDivElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);

  useLayoutEffect(() => {
    returnFocus.current = document.activeElement as HTMLElement | null;
    const box = boxRef.current;
    const undoInert = makeBackgroundInert(box);
    useView.getState().pushOverlay(overlayId);
    return () => {
      undoInert();
      useView.getState().popOverlay(overlayId);
      // Restore only when focus is about to be orphaned — it is still inside
      // this dialog (React runs cleanups before it detaches the nodes) or it
      // has already fallen to <body>. A dialog that closes because ANOTHER one
      // opened must not snatch focus back out of the new one.
      const active = document.activeElement;
      const orphaned = !active || active === document.body || !!box?.contains(active);
      if (orphaned) returnFocus.current?.focus?.();
    };
  }, [overlayId]);

  // Focus the first thing inside, or the box itself when there is nothing.
  useEffect(() => {
    const box = boxRef.current;
    if (!box) return;
    if (box.contains(document.activeElement)) return; // an autoFocus already won
    const first = box.querySelector<HTMLElement>(FOCUSABLE);
    (first ?? box).focus();
  }, []);

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      if (!ownsEscape(overlayId)) return;
      e.stopPropagation();
      e.preventDefault();
      onClose();
      return;
    }
    if (e.key !== 'Tab') return;
    // The trap. Without it Tab from the last control walks into the canvas
    // behind the dialog and keeps going, and there is no way back in.
    const box = boxRef.current;
    if (!box) return;
    const items = [...box.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((n) => n.offsetParent !== null);
    if (items.length === 0) { e.preventDefault(); return; }
    const first = items[0];
    const last = items[items.length - 1];
    if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    else if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
  };

  return { boxRef, onKeyDown };
}

export function Modal({ title, onClose, overlayId, className, style, backdropStyle, headExtra, children }: Props) {
  const { boxRef, onKeyDown } = useDialog(overlayId, onClose);
  const titleId = useId();

  return (
    <div className="modal-backdrop" style={backdropStyle} onClick={onClose}>
      <div
        ref={boxRef}
        className={`modal${className ? ` ${className}` : ''}`}
        style={style}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onPointerDown={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div className="modal-head">
          <h3 id={titleId}>{title}</h3>
          {headExtra}
        </div>
        {children}
      </div>
    </div>
  );
}
