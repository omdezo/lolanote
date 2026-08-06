// One verb: OPEN.
//
// Nothing in the product opened or edited on a single activation. Editing an
// existing note was `onDoubleClick` and nothing else; so was opening a board,
// a link, a file and a colour swatch — each carrying `title="Double-click to
// open"`. Two consequences the codebase found independently:
//
//   * keyboard — there was no expressible OPEN at all. A note created this
//     session auto-enters editing and is writable; the same note tomorrow is
//     not reachable by any key.
//   * touch — the synthesized `dblclick` races the drag that arms at 3px of
//     finger drift, so the commonest mobile interaction is "try to open
//     Pre-Production, move it 40px instead".
//
// This is the single handler all three paths call — the mouse's double-click,
// Enter on a focused shell, and the second tap on an already-selected element
// — so they cannot drift apart. Mouse semantics are unchanged.
import type { QElement } from '../api/types';
import { useView } from '../store/viewStore';

/** Types whose activation is "start writing in it". */
const EDITABLE = new Set(['CARD', 'CLONE', 'DOCUMENT']);

/**
 * Focus the first thing inside a shell that takes typing.
 *
 * Read off the DOM rather than plumbed through props: these components own
 * their own inputs and there is no shared editing store for them, so the shell
 * cannot hold a ref to something it does not render.
 */
function focusInside(elementId: string, ...selectors: string[]): boolean {
  const shell = document.querySelector(`[data-element-id="${elementId}"]`);
  if (!shell) return false;
  for (const sel of selectors) {
    const node = shell.querySelector<HTMLElement>(sel);
    if (node) { node.focus(); return true; }
  }
  return false;
}

/**
 * Do whatever OPEN means for this element. Returns false when the type has no
 * activation, so a caller can leave the key unhandled rather than swallow it.
 */
export function activateElement(
  element: QElement,
  navigate?: (boardId: string) => Promise<void>,
): boolean {
  const content = element.content ?? {};

  if (EDITABLE.has(element.type)) {
    useView.getState().setEditing(element.id);
    return true;
  }

  switch (element.type) {
    case 'BOARD':
    case 'ALIAS': {
      const id = element.type === 'ALIAS' ? content.targetBoardId : element.id;
      if (!id || !navigate) return false;
      void navigate(id);
      return true;
    }
    case 'LINK':
    case 'FILE':
      if (!content.url) return false;
      window.open(content.url, '_blank', 'noopener');
      return true;
    case 'COLOR_SWATCH': {
      const shell = document.querySelector(`[data-element-id="${element.id}"]`);
      const picker = shell?.querySelector<HTMLInputElement>('input[type="color"]');
      if (!picker) return false;
      picker.click();
      return true;
    }
    case 'TASK_LIST':
      return focusInside(element.id, '.task-text', '.task-add');
    case 'COLUMN':
      return focusInside(element.id, '.column-title');
    case 'IMAGE':
      return focusInside(element.id, '.image-caption');
    case 'COMMENT_THREAD':
      return focusInside(element.id, '.comment-input');
    case 'TABLE':
      return focusInside(element.id, 'input, textarea, [contenteditable="true"]');
    default:
      return false;
  }
}

/** Types whose name is edited by F2 rather than by opening them. */
export function isRenameable(element: QElement): boolean {
  return element.type === 'BOARD' || element.type === 'COLUMN';
}
