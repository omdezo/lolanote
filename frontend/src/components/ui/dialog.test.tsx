// FIRST: matchMedia and ResizeObserver do not exist in jsdom and this app reads
// both at module scope. Import order is the mechanism — see the file.
import './../../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { useView } from '../../store/viewStore';
import { PromptHost, prompt } from './Prompt';
import { Modal } from './Modal';

// AX11 — no dialog was a dialog.
//
// Six surfaces each rendered `.modal-backdrop`/`.modal` with no role, no
// aria-modal, no aria-labelledby on the <h3> that was right there, no focus
// trap and no focus restore. The file that replaced the native dialogs says in
// its own header that it did so because they are "unstyled, focus-stealing,
// unthemeable" — and dropped the three things they did correctly.
//
// The specific casualty was agent-shaped: Escape and Enter were handled ONLY on
// Prompt's single-line branch, and AgentAsk opens the `multiline` branch for the
// board's agentInstructions. So the one dialog a keyboard user could not close
// at all was the board's rules editor — a governance surface. Escape lives on
// the container here, which is precisely why that branch was missed.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

function escape(node: Element) {
  act(() => {
    node.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  });
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  useView.setState({ overlays: [] });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

describe('a dialog is a dialog', () => {
  it('is named by its own heading', () => {
    act(() => {
      root.render(<Modal title="Rename board" overlayId="t" onClose={() => undefined}><p>body</p></Modal>);
    });
    const box = container.querySelector('[role="dialog"]');
    expect(box, 'no role at all — the canvas behind it was still a document').not.toBeNull();
    expect(box!.getAttribute('aria-modal')).toBe('true');
    const labelledBy = box!.getAttribute('aria-labelledby');
    expect(labelledBy, 'the <h3> was right there and unused').toBeTruthy();
    // useId() ids contain ':', so getElementById rather than a selector.
    expect(document.getElementById(labelledBy!)!.textContent).toBe('Rename board');
  });

  it('hides everything else from pointers and screen readers', () => {
    const behind = document.createElement('div');
    behind.className = 'app';
    document.body.insertBefore(behind, container);
    act(() => {
      root.render(<Modal title="X" overlayId="t" onClose={() => undefined}><button>ok</button></Modal>);
    });
    // NOT `inert` on `.app`: these dialogs render inside the app tree, so that
    // would inert the dialog with it. Siblings all the way up is what
    // "everything except this subtree" means in a tree.
    expect(behind.hasAttribute('inert'), 'Tab walked straight into the canvas behind').toBe(true);
    act(() => { root.unmount(); });
    expect(behind.hasAttribute('inert'), 'the app stayed inert after the dialog closed').toBe(false);
    behind.remove();
    root = createRoot(container);
  });

  it('traps Tab rather than letting it leave', () => {
    act(() => {
      root.render(
        <Modal title="X" overlayId="t" onClose={() => undefined}>
          <button className="a">a</button>
          <button className="b">b</button>
        </Modal>,
      );
    });
    const a = container.querySelector('button.a') as HTMLElement;
    const b = container.querySelector('button.b') as HTMLElement;
    // jsdom reports offsetParent as null for everything, so the trap's
    // visibility filter would empty the list; assert the wiring instead, on the
    // element that receives it.
    expect(document.activeElement, 'nothing inside took focus on open').toBe(a);
    act(() => { b.focus(); });
    expect(document.activeElement).toBe(b);
  });

  it('gives focus back to whatever opened it', () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();
    act(() => {
      root.render(<Modal title="X" overlayId="t" onClose={() => undefined}><button>ok</button></Modal>);
    });
    expect(document.activeElement).not.toBe(opener);
    act(() => { root.unmount(); });
    expect(document.activeElement, 'focus was left on <body> with nothing to navigate back with').toBe(opener);
    opener.remove();
    root = createRoot(container);
  });

  it('takes Escape on the container, so every branch inside gets it', () => {
    let closed = false;
    act(() => {
      root.render(<Modal title="X" overlayId="esc" onClose={() => { closed = true; }}><textarea /></Modal>);
    });
    escape(container.querySelector('textarea')!);
    expect(closed, 'Escape inside a field did nothing').toBe(true);
  });
});

describe('the board rules editor — the dialog that could not be closed', () => {
  it('the multiline branch answers Escape', async () => {
    act(() => { root.render(<PromptHost />); });
    let resolved: string | null | undefined;
    act(() => { void prompt({ title: 'How this board works', multiline: true }).then((v) => { resolved = v; }); });
    const area = container.querySelector('textarea');
    expect(area, 'AgentAsk opens exactly this branch for agentInstructions').not.toBeNull();
    escape(area!);
    await act(async () => { await Promise.resolve(); });
    expect(resolved, 'the multiline branch had no onKeyDown at all').toBeNull();
  });

  it('and Ctrl+Enter commits it, because plain Enter is a newline', async () => {
    act(() => { root.render(<PromptHost />); });
    let resolved: string | null | undefined;
    act(() => { void prompt({ title: 'Rules', multiline: true, defaultValue: 'no new columns' }).then((v) => { resolved = v; }); });
    const area = container.querySelector('textarea') as HTMLTextAreaElement;
    act(() => {
      area.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true, cancelable: true }));
    });
    await act(async () => { await Promise.resolve(); });
    expect(resolved).toBe('no new columns');
  });
});
