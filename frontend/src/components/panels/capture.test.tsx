// FIRST: matchMedia and ResizeObserver do not exist in jsdom and this app reads
// both at module scope. Import order is the mechanism — see the file.
import './../../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import { PromptHost } from '../ui/Prompt';
import { captureText, captureToUnsorted } from './UnsortedTray';
import type { Op } from '../../api/types';

// CV24 — quick capture was a global gesture that only worked while a panel was
// open.
//
// The Ctrl/⌘+Enter listener was registered INSIDE UnsortedTray, so it unmounted
// with the tray: the one gesture documented as working "anywhere" fired only
// while the panel it exists to replace was already on screen. And it called
// `window.prompt` — an unstyled, unthemed, left-to-right OS box on a product
// whose whole point for this user is that Arabic renders correctly.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;
let committed: Op[][];

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  committed = [];
  useView.setState({ overlays: [] });
  useBoard.setState({
    boardId: 'b1', readOnly: false, elements: {}, selection: new Set(),
    commitTransaction: async (ops: Op[]) => { committed.push(ops); },
  } as never);
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

describe('capture does not need the tray to be open', () => {
  it('the listener does not live in the component it replaces', () => {
    const src = new URL('./UnsortedTray.tsx', import.meta.url);
    void src;
    // Asserted as text because the defect IS the registration site: a listener
    // inside a component is a listener that unmounts with it.
    return import('node:fs').then(async ({ readFileSync }) => {
      const { resolve } = await import('node:path');
      // Comments are allowed to name the thing they replaced; code is not.
      const tray = readFileSync(resolve(__dirname, './UnsortedTray.tsx'), 'utf8')
        .replace(/\/\*[\s\S]*?\*\//g, ' ')
        .replace(/^[ \t]*\/\/.*$/gm, ' ');
      expect(tray, 'the gesture is back inside the panel it is meant to replace')
        .not.toMatch(/addEventListener\('keydown'/);
      expect(tray, 'an unstyled LTR OS dialog on an Arabic board')
        .not.toContain('window.prompt');
      const app = readFileSync(resolve(__dirname, '../../App.tsx'), 'utf8');
      expect(app, 'the global key map does not know about capture').toContain('captureToUnsorted');
    });
  });

  it('lands one card in Unsorted, through the ordinary transaction path', () => {
    expect(captureText('  a thought  ')).toBe(true);
    expect(committed.length, 'capture wrote nothing').toBe(1);
    const op = committed[0][0];
    expect(op.action).toBe('create');
    expect(op.changes?.location?.section, 'the capture inbox is the whole point').toBe('UNSORTED');
    expect(op.changes?.content?.textPreview).toBe('a thought');
  });

  it('refuses empty text and a read-only board rather than writing a blank card', () => {
    expect(captureText('   ')).toBe(false);
    useBoard.setState({ readOnly: true } as never);
    expect(captureText('anything')).toBe(false);
    expect(committed).toEqual([]);
  });

  it('asks through the in-app host, which is themed and direction-aware', async () => {
    act(() => { root.render(<PromptHost />); });
    act(() => { void captureToUnsorted(); });
    const field = container.querySelector('.modal input.search-input') as HTMLInputElement;
    expect(field, 'the gesture still reaches for the OS dialog').not.toBeNull();
    expect(field.getAttribute('dir'), 'an Arabic capture rendered left-to-right').toBe('auto');
  });
});
