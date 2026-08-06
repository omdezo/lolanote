// FIRST: matchMedia and ResizeObserver do not exist in jsdom and this app reads
// both at module scope. Import order is the mechanism — see the file.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactElement } from 'react';

import { useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { useAgent } from '../agent/agentStore';
import { ElementShell } from './ElementShell';
import { TaskListView } from '../components/elements/TaskListView';
import { ColumnView } from '../components/elements/ColumnView';
import { ContextMenuHost, useContextMenu } from '../components/ui/ContextMenu';
import { NoteIcon, TrashIcon } from '../components/Icons';
import type { AgentPlan, AgentRun, Op, QElement } from '../api/types';

// Every assertion here is on RENDERED reality, because that is where these
// defects lived: the code compiled, typechecked and shipped, and the failures
// were "the button has no name", "Tab cannot leave this field", "the card does
// not dim". None of those are visible to a test that inspects state.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;
let committed: Op[][];

const el = (id: string, type: string, parentId: string, content: Record<string, unknown> = {}): QElement => ({
  id, type, content,
  location: { parentId, section: 'CANVAS', position: { x: 0, y: 0 }, index: 1, width: 260, height: 0 },
  createdBy: '', createdAt: '', updatedAt: '',
} as QElement);

const navigate = async () => { /* no navigation in these tests */ };
const viewportRef = { current: null } as React.RefObject<HTMLDivElement | null>;

function render(node: ReactElement) {
  act(() => { root.render(node); });
  return container;
}

beforeEach(() => {
  container = document.createElement('div');
  container.className = 'canvas-viewport';
  document.body.appendChild(container);
  root = createRoot(container);
  committed = [];

  useBoard.setState({
    boardId: 'b1',
    elements: { b1: el('b1', 'BOARD', '') },
    selection: new Set(),
    readOnly: false,
    remoteEditing: {},
    boardStats: {},
    commitTransaction: async (ops: Op[]) => { committed.push(ops); },
  } as never);
  useView.setState({ drag: null, labelFilter: new Set(), editingId: null, overlays: [], sizes: {}, focusedId: null });
  useAgent.setState({ run: null, adjustments: [], capabilities: { enabled: true, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 } } });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

// ---- AX13 -----------------------------------------------------------------
// The shared icon set omitted aria-hidden, so ~40 icon-only buttons computed an
// EMPTY accessible name and the product read as "button, button, button".
describe('icons are decoration, and the controls around them have names', () => {
  it('every glyph in the shared set is hidden from assistive technology', () => {
    render(<div><NoteIcon /><TrashIcon size={12} /></div>);
    const svgs = [...container.querySelectorAll('svg')];
    expect(svgs.length).toBeGreaterThan(0);
    for (const svg of svgs) {
      expect(svg.getAttribute('aria-hidden'), svg.outerHTML).toBe('true');
      expect(svg.getAttribute('focusable'), svg.outerHTML).toBe('false');
    }
  });

  it("a to-do row's controls all have an accessible name", () => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        tl: el('tl', 'TASK_LIST', 'b1'),
        t1: el('t1', 'TASK', 'tl', { text: 'Lock the location', done: false, indent: 0 }),
      },
    } as never);
    render(<TaskListView element={useBoard.getState().elements.tl} />);

    const unnamed = [...container.querySelectorAll('button, input')]
      .filter((c) => {
        const name = (c.getAttribute('aria-label')
          ?? c.getAttribute('aria-labelledby')
          ?? c.getAttribute('title')
          ?? c.textContent
          ?? '').trim();
        return name === '';
      })
      .map((c) => c.outerHTML);
    expect(unnamed.join('\n'), 'controls a screen reader cannot name').toBe('');
  });

  it('a done task announces as checked, not as a styled button', () => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        tl: el('tl', 'TASK_LIST', 'b1'),
        t1: el('t1', 'TASK', 'tl', { text: 'Wrap', done: true, indent: 0 }),
      },
    } as never);
    render(<TaskListView element={useBoard.getState().elements.tl} />);
    const box = container.querySelector('[role="checkbox"]');
    expect(box, 'the completion control is not a checkbox to anything').toBeTruthy();
    expect(box!.getAttribute('aria-checked')).toBe('true');
  });
});

// ---- AX3 ------------------------------------------------------------------
// WCAG 2.1.2, Level A. preventDefault() ran BEFORE the 0..4 clamp, so at both
// ends of the indent range Tab was swallowed and focus could not leave the
// field by keyboard at all.
describe('the to-do field is not a keyboard trap', () => {
  const renderTaskAtIndent = (indent: number) => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        tl: el('tl', 'TASK_LIST', 'b1'),
        t1: el('t1', 'TASK', 'tl', { text: 'Scout the wadi', done: false, indent }),
      },
    } as never);
    render(<TaskListView element={useBoard.getState().elements.tl} />);
    return container.querySelector('.task-text') as HTMLInputElement;
  };

  const press = (node: HTMLElement, init: KeyboardEventInit) => {
    const ev = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init });
    act(() => { node.dispatchEvent(ev); });
    return ev;
  };

  for (const [name, indent, shiftKey] of [
    ['Shift+Tab at the shallowest indent', 0, true],
    ['Tab at the deepest indent', 4, false],
    ['Tab in the middle of the range', 2, false],
  ] as const) {
    it(`${name} moves focus instead of being swallowed`, () => {
      const field = renderTaskAtIndent(indent);
      const ev = press(field, { key: 'Tab', shiftKey });
      expect(ev.defaultPrevented, 'Tab was consumed — focus cannot leave').toBe(false);
      expect(committed, 'Tab wrote to the board').toEqual([]);
    });
  }

  it('indenting moved to Alt+arrow, which no browser reserves', () => {
    const field = renderTaskAtIndent(1);
    press(field, { key: 'ArrowRight', altKey: true });
    expect(committed.length).toBe(1);
    expect(committed[0][0].changes?.content?.indent).toBe(2);
  });

  it('Alt+arrow at the boundary changes nothing and stays unhandled', () => {
    const field = renderTaskAtIndent(0);
    const ev = press(field, { key: 'ArrowLeft', altKey: true });
    expect(ev.defaultPrevented).toBe(false);
    expect(committed).toEqual([]);
  });
});

// ---- AX2 ------------------------------------------------------------------
// Nothing in the product opened or edited on a single activation: the core
// verbs were onDoubleClick and nothing else, so there was no keyboard-
// expressible and no touch-expressible OPEN at all.
describe('OPEN is expressible without a mouse', () => {
  it('a focused card opens on Enter', () => {
    const card = el('c1', 'CARD', 'b1', { textPreview: 'Second act' });
    useBoard.setState({ elements: { b1: el('b1', 'BOARD', ''), c1: card } } as never);
    render(<ElementShell element={card} navigate={navigate} viewportRef={viewportRef} />);

    const shell = container.querySelector('[data-element-id="c1"]') as HTMLElement;
    // -1 rather than 0 because the board's tab order is now a roving index
    // (AX1): every card stays focusable, but only the current one is a tab
    // stop. What matters here is that focus can arrive at all.
    expect(shell.getAttribute('tabindex'), 'the shell cannot receive focus').toBe('-1');
    act(() => { useView.getState().setFocused('c1'); });
    expect(shell.getAttribute('tabindex'), 'the roving card is not the tab stop').toBe('0');

    act(() => {
      shell.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    });
    expect(useView.getState().editingId, 'Enter on a focused card did not open it').toBe('c1');
  });

  it('a card carries a name, so focus lands somewhere announceable', () => {
    const card = el('c1', 'CARD', 'b1', { textPreview: 'Call sheet' });
    useBoard.setState({ elements: { b1: el('b1', 'BOARD', ''), c1: card } } as never);
    render(<ElementShell element={card} navigate={navigate} viewportRef={viewportRef} />);
    const shell = container.querySelector('[data-element-id="c1"]')!;
    expect(shell.getAttribute('aria-label')).toContain('Call sheet');
  });
});

// ---- DL16 -----------------------------------------------------------------
// There was no way at any layer to tell the agent not to read something.
describe('content a person has marked private', () => {
  it('says so on the card and in its accessible name', () => {
    const card = el('c1', 'CARD', 'b1', { textPreview: 'Cast medical notes', agentExclude: true });
    useBoard.setState({ elements: { b1: el('b1', 'BOARD', ''), c1: card } } as never);
    render(<ElementShell element={card} navigate={navigate} viewportRef={viewportRef} />);

    const shell = container.querySelector('[data-element-id="c1"]')!;
    expect(shell.className).toContain('agent-excluded');
    expect(shell.textContent).toContain('Qomra does not read this');
    expect(shell.getAttribute('aria-label')).toContain('Qomra does not read this');
  });
});

// ---- CV4 ------------------------------------------------------------------
// Cards inside columns never dimmed under a proposal, so a plan moving six
// cards OUT of a column previewed as nothing at all: the ghosts are suppressed
// (the destination is another canvas), the tile badge only counts what goes IN,
// and the source cards sat undimmed exactly where they were.
describe('the preview is not blind inside columns', () => {
  it('a card being moved out of a column dims in place', () => {
    const column = el('col', 'COLUMN', 'b1', { title: 'Pre-Production' });
    const child = el('c1', 'CARD', 'col', { textPreview: 'Location scout' });
    useBoard.setState({ elements: { b1: el('b1', 'BOARD', ''), col: column, c1: child } } as never);
    useAgent.setState({
      run: {
        id: 'r1', state: 'PROPOSED',
        plan: {
          actions: [{ seq: 0, kind: 'move_element', elementId: 'c1', parentId: 'b1', summary: 'move out' }],
        } as AgentPlan,
      } as AgentRun,
      adjustments: [],
    });

    render(<ColumnView element={column} navigate={navigate} viewportRef={viewportRef} />);
    const shell = container.querySelector('[data-element-id="c1"]');
    expect(shell, 'the column rendered no child shell').toBeTruthy();
    expect(shell!.className, 'a card the plan moves out of a column shows nothing')
      .toContain('agent-proposed');
  });
});

// ---- AX12 -----------------------------------------------------------------
// The context menu is the only home for Lock, Add label, Text direction, Group
// into column, Rename, Delete and both "Ask Qomra" entry points, and it had no
// menu semantics, no focus on open and no arrow keys.
describe('the context menu is a menu', () => {
  beforeEach(() => {
    act(() => {
      useContextMenu.getState().open(20, 20, [
        { label: 'Duplicate', onClick: () => undefined },
        { label: 'Lock', onClick: () => undefined },
        {
          label: 'Text direction',
          sub: [
            { label: 'Auto', checked: true, onClick: () => undefined },
            { label: 'Right to left', checked: false, onClick: () => undefined },
          ],
        },
      ]);
    });
    render(<ContextMenuHost />);
  });

  it('declares itself as a menu of menu items', () => {
    expect(container.querySelector('[role="menu"]')).toBeTruthy();
    expect(container.querySelectorAll('[role="menuitem"]').length).toBeGreaterThan(0);
  });

  it('focuses its first item, so it is operable the moment it opens', () => {
    expect(document.activeElement?.textContent).toContain('Duplicate');
  });

  it('walks with the arrow keys', () => {
    const first = document.activeElement as HTMLElement;
    act(() => {
      first.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }));
    });
    expect(document.activeElement?.textContent).toContain('Lock');
  });

  it('a parent row opens its group on click, not on hover alone', () => {
    const parent = [...container.querySelectorAll<HTMLButtonElement>('button.ctx-item')]
      .find((b) => b.textContent?.includes('Text direction'))!;
    expect(parent.getAttribute('aria-haspopup')).toBe('menu');
    act(() => { parent.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(container.textContent).toContain('Right to left');
  });

  it('a value row announces which value is current', () => {
    const parent = [...container.querySelectorAll<HTMLButtonElement>('button.ctx-item')]
      .find((b) => b.textContent?.includes('Text direction'))!;
    act(() => { parent.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    const auto = [...container.querySelectorAll('[role="menuitemcheckbox"]')]
      .find((b) => b.textContent?.includes('Auto'));
    expect(auto, 'the direction options are not checkable to anything').toBeTruthy();
    expect(auto!.getAttribute('aria-checked')).toBe('true');
  });
});
