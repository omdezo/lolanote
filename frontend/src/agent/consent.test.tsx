// FIRST: the composer reads the settings store, which resolves the OS theme
// with matchMedia at module scope, and jsdom has none.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactElement } from 'react';

import { useAgent } from './agentStore';
import { useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import { Ask } from './AgentAsk';
import { api } from '../api/client';
import { localAcknowledgement } from './processingConsent';
import { DEFAULT_SETTINGS } from '../api/types';

// DL8. Nowhere in the product did it say that board content is transmitted to a
// third-party model provider, and the personification worked against a person
// noticing: the model is called "Qomra", a name in the user's own language,
// presented as part of the app they installed. It is a courier. Every other
// trust mechanism here governs what the agent WRITES; not one governed or
// disclosed what it SENDS.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

function render(node: ReactElement) {
  act(() => { root.render(node); });
  return container;
}

const setAcknowledged = (at: string) => {
  useSettings.setState({
    sub: 'person-1',
    settings: { ...DEFAULT_SETTINGS, agent: { instructions: '', processingAcknowledgedAt: at } },
  });
};

beforeEach(() => {
  container = document.createElement('div');
  container.className = 'canvas-viewport';
  document.body.appendChild(container);
  root = createRoot(container);
  vi.spyOn(api, 'updateSettings').mockResolvedValue(DEFAULT_SETTINGS);
  window.localStorage.clear();

  useBoard.setState({
    boardId: 'b1', elements: {}, selection: new Set(), readOnly: false,
  } as never);
  useAgent.setState({
    run: null, events: [], adjustments: [], recent: [], audit: [], reach: null,
    busy: false, open: true,
    capabilities: {
      enabled: true, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 },
      processing: { provider: 'Anthropic', model: 'claude-opus-4', region: 'us-east-1' },
    },
  });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
  vi.restoreAllMocks();
});

describe('the person is told where the board goes before it goes there', () => {
  it('an account that has never been asked gets the question, not the field', () => {
    setAcknowledged('');
    render(<Ask />);
    expect(container.textContent).toContain('Before Qomra reads this board');
    // No way to transmit anything until it is answered. A disclosure a request
    // can be sent past without reading is not a disclosure.
    expect(container.querySelector('.ac-intent'), 'the composer accepts a request unasked').toBeNull();
    expect(container.querySelector('.ac-send')).toBeNull();
  });

  it('names the deployment\'s real processor rather than a constant in this bundle', () => {
    setAcknowledged('');
    render(<Ask />);
    expect(container.textContent, 'the provider is not named at all').toContain('Anthropic');
    expect(container.textContent).toContain('claude-opus-4');
    expect(container.textContent).toContain('us-east-1');
  });

  it('says a third-party provider even when the deployment reports none', () => {
    useAgent.setState({
      capabilities: { enabled: true, can: [], cannot: [], limits: { maxActions: 1, maxSteps: 1 } },
    });
    setAcknowledged('');
    render(<Ask />);
    // Silence would read as "nothing leaves this machine", which is the
    // opposite of the truth.
    expect(container.textContent).toContain('third-party model provider');
  });

  it('records the answer on the account, so it is asked once and not once a session', () => {
    setAcknowledged('');
    render(<Ask />);
    const agree = [...container.querySelectorAll('button')]
      .find((b) => b.textContent?.includes('turn Qomra on'))!;
    expect(agree).toBeTruthy();
    act(() => { agree.dispatchEvent(new MouseEvent('click', { bubbles: true })); });

    const stamp = useSettings.getState().settings.agent.processingAcknowledgedAt;
    expect(stamp, 'agreeing recorded nothing').toBeTruthy();
    expect(Number.isNaN(Date.parse(stamp!))).toBe(false);
    // And the composer is there now.
    expect(container.querySelector('.ac-intent')).toBeTruthy();
  });

  it('survives a server that drops the field on the way back', () => {
    // PATCH /me/settings decodes into the typed struct and returns its own
    // normalized copy, so until the backend carries the field the account stamp
    // is thrown away — and the card would have come back half a second after
    // the person dismissed it, forever.
    setAcknowledged('');
    render(<Ask />);
    const agree = [...container.querySelectorAll('button')]
      .find((b) => b.textContent?.includes('turn Qomra on'))!;
    act(() => { agree.dispatchEvent(new MouseEvent('click', { bubbles: true })); });

    act(() => {
      useSettings.setState({
        settings: { ...DEFAULT_SETTINGS, agent: { instructions: '', processingAcknowledgedAt: '' } },
      });
    });
    expect(container.textContent, 'the question came back after being answered')
      .not.toContain('Before Qomra reads this board');
  });

  it('does not answer on behalf of the next person to sign in', () => {
    setAcknowledged('');
    render(<Ask />);
    act(() => {
      [...container.querySelectorAll('button')]
        .find((b) => b.textContent?.includes('turn Qomra on'))!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(localAcknowledgement('person-1')).toBeTruthy();
    expect(localAcknowledgement('someone-else'), 'a shared browser consented for a stranger').toBe('');
  });

  it('an account that has already answered is not asked again', () => {
    setAcknowledged('2026-01-01T00:00:00Z');
    render(<Ask />);
    expect(container.textContent).not.toContain('Before Qomra reads this board');
    expect(container.querySelector('.ac-intent')).toBeTruthy();
  });

  it('and the standing disclosure survives the question being answered', () => {
    setAcknowledged('2026-01-01T00:00:00Z');
    render(<Ask />);
    // The permanent line is the thing a person can re-read later; a modal
    // dismissed once and never seen again is not a disclosure either.
    expect(container.querySelector('.ac-processing-line')?.textContent).toContain('Anthropic');
  });
});
