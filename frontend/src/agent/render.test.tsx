import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactElement } from 'react';

import { useAgent } from './agentStore';
import { useBoard } from '../store/boardStore';
import { Pill } from './AgentPill';
import { Observing } from './AgentObserving';
import { Working } from './AgentWorking';
import { Decide } from './AgentDecide';
import { Done } from './AgentDone';
import { PlanNotes } from './PlanNotes';
import { GhostLayer } from './GhostLayer';
import type { AgentAction, AgentPlan, AgentRun } from '../api/types';

// Every one of these surfaces was written, typechecked, bundled — and never
// rendered. Typechecking proves the shapes line up; it does not prove a
// component survives being asked to draw itself, which is where a missing
// export, a bad destructure or an undefined lookup actually bites.
//
// These render for REAL, in a DOM, the way the browser does. renderToString
// cannot stand in for it: zustand 5 hands React getInitialState as the server
// snapshot, so a server render sees the store as it was at boot and every
// selector-driven component draws itself empty — a false pass on exactly the
// components most worth checking.

// React needs telling it is in a test environment, or every act() warns.
(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

function html(node: ReactElement): string {
  act(() => { root.render(node); });
  return container.innerHTML;
}

const action = (over: Partial<AgentAction> = {}): AgentAction => ({
  seq: 0, kind: 'create_column', elementId: 'c1',
  title: 'Pricing', summary: 'Pricing', ...over,
});

/** One board element, as the canvas store holds it. */
const el = (
  id: string, type: string, parentId: string,
  position = { x: 0, y: 0 }, width = 0, height = 0,
) => ({
  id, type, content: {},
  location: { parentId, section: 'CANVAS', position, index: 0, width, height },
});

const plan = (over: Partial<AgentPlan> = {}): AgentPlan => ({
  summary: 'Two changes.',
  actions: [
    action(),
    action({ seq: 1, kind: 'move_element', elementId: 'card-1', title: undefined, text: 'A card', destination: 'Pricing', parentId: 'c1' }),
  ],
  ...over,
});

const run = (over: Partial<AgentRun> = {}): AgentRun => ({
  id: 'r1', state: 'PROPOSED',
  task: { intent: 'Organize', rootBoardId: 'b1', scope: 'board', autonomy: 'preview' },
  usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0.012, calls: 1 },
  createdAt: '', updatedAt: '',
  plan: plan(),
  ...over,
} as unknown as AgentRun);

beforeEach(() => {
  container = document.createElement('div');
  container.className = 'canvas-viewport';
  document.body.appendChild(container);
  root = createRoot(container);

  useBoard.setState({
    boardId: 'b1',
    elements: { b1: { id: 'b1', type: 'BOARD', content: {}, location: { parentId: '', section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 } } } as never,
    selection: new Set(),
    readOnly: false,
  });
  useAgent.setState({
    run: null, events: [], adjustments: [], hoverSeq: null,
    recent: [], audit: [], drift: null, reach: null, active: null,
    observedStaged: 0, busy: false, open: false,
    capabilities: { enabled: true, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 } },
  });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

describe('agent surfaces render', () => {
  it('the resting pill', () => {
    expect(html(<Pill />)).toContain('Qomra');
  });

  it('the drift pill', () => {
    useAgent.setState({ drift: { kind: 'scatter', message: 'This board has drifted', intent: 'Tidy it' } });
    expect(html(<Pill />)).toContain('This board has drifted');
  });

  // Never rendered before this test existed.
  it("a colleague's run", () => {
    useAgent.setState({
      active: { state: 'RUNNING', mine: false, actor: 'Rana', startedAt: '' },
      observedStaged: 3,
    });
    const out = html(<Observing />);
    expect(out).toContain('Rana');
    expect(out).toContain('3');
  });

  it('nothing, when the run is your own', () => {
    useAgent.setState({ active: { id: 'r1', state: 'RUNNING', mine: true, startedAt: '' } });
    expect(html(<Observing />)).toBe('');
  });

  it('the working card, with a steer box', () => {
    useAgent.setState({
      run: run({ state: 'RUNNING' }),
      events: [
        { id: '', runId: 'r1', sequence: 1, type: 'action.staged', message: 'Column: Pricing', data: { kind: 'create_column' }, at: '' },
      ] as never,
    });
    expect(html(<Working />)).toContain('Pricing');
  });

  it('the decision card, collapsed', () => {
    const r = run();
    useAgent.setState({ run: r });
    const out = html(<Decide run={r} />);
    expect(out).toContain('Two changes.'); // the summary, not the rows
    expect(out).toContain('change');
  });

  // The rows are one click away by design. This is the click, and the thing
  // being checked is the fact the review used to omit entirely: WHERE each
  // change lands.
  it('the decision card, expanded, says where every change goes', () => {
    const r = run();
    useAgent.setState({ run: r });
    html(<Decide run={r} />);

    const review = [...container.querySelectorAll('button')]
      .find((b) => b.textContent?.includes('Review'));
    expect(review, 'no Review control on the decision card').toBeTruthy();
    act(() => { review!.dispatchEvent(new MouseEvent('click', { bubbles: true })); });

    const out = container.innerHTML;
    expect(out).toContain('A card');   // the thing being moved
    expect(out).toContain('Pricing');  // and where it is going
  });

  it('the decision card when the plan only asks a question', () => {
    const r = run({ plan: { actions: [], question: { text: 'Which board?', options: ['A', 'B'] } } as AgentPlan });
    useAgent.setState({ run: r });
    expect(html(<Decide run={r} />)).toContain('Which board?');
  });

  // A run that declined for a good reason had that reason in plan.summary and
  // nowhere a person would ever look: the outcome line is generated from the
  // run's state and says nothing about the actual work.
  it('the outcome card shows what the agent actually said', () => {
    const r = run({ state: 'PARTIAL', plan: plan({ actions: [], summary: 'The figures are not on this board.' }) });
    useAgent.setState({ run: r });
    expect(html(<Done run={r} />)).toContain('The figures are not on this board.');
  });

  it('the outcome card', () => {
    const r = run({ state: 'COMPLETED' });
    useAgent.setState({ run: r });
    expect(html(<Done run={r} />)).toContain('Done');
  });

  it('the outcome card for a refusal, which must not read like a fault', () => {
    const r = run({ state: 'DENIED', reason: 'you can only comment here' });
    useAgent.setState({ run: r });
    const out = html(<Done run={r} />);
    expect(out).toContain('you can only comment here');
    expect(out).not.toContain('Could not finish');
  });

  // A job bigger than one run's budget could only be continued by retyping the
  // request from memory, and the retyped one knew nothing about the run before
  // it — which is how a truncated build got a second copy built beside it.
  it('the outcome card offering to continue what a run left undone', () => {
    const r = run({
      state: 'COMPLETED',
      plan: plan({ unmet: [{ request: 'filling Editing, Sound', why: 'the run ran out of room' }] }),
    });
    useAgent.setState({ run: r });
    html(<Done run={r} />);
    const button = [...container.querySelectorAll('button')]
      .find((b) => b.textContent?.includes('Continue'));
    expect(button, 'a run with unmet work offers no way to keep going').toBeTruthy();
  });

  it('no Continue when the run finished everything', () => {
    const r = run({ state: 'COMPLETED' });
    useAgent.setState({ run: r });
    html(<Done run={r} />);
    const button = [...container.querySelectorAll('button')]
      .find((b) => b.textContent?.includes('Continue'));
    expect(button, 'a finished run offers to continue nothing').toBeFalsy();
  });

  // A run that changed nothing records its whole request as unmet, with the
  // reason it declined. Offering Continue there asks the same question again
  // and gets the same answer.
  it('no Continue when the run changed nothing', () => {
    const r = run({
      state: 'PARTIAL',
      plan: plan({
        actions: [],
        summary: 'The figures are not on this board.',
        unmet: [{ request: 'fill in the budget figures', why: 'The figures are not on this board.' }],
      }),
    });
    useAgent.setState({ run: r });
    html(<Done run={r} />);
    const button = [...container.querySelectorAll('button')]
      .find((b) => b.textContent?.includes('Continue'));
    expect(button, 'a run that declined offers to continue itself').toBeFalsy();
  });

  it('the outcome card offering a rule it learned', () => {
    const r = run({ state: 'COMPLETED', plan: plan({ proposedRule: 'Never add a column.' }) });
    useAgent.setState({ run: r });
    expect(html(<Done run={r} />)).toContain('Never add a column.');
  });

  it('what a plan did not do', () => {
    const p = plan({
      unmet: [{ request: 'file the invoices', why: 'none on this board' }],
      notes: ['This plan may be incomplete.'],
    });
    const out = html(<PlanNotes plan={p} />);
    expect(out).toContain('file the invoices');
    expect(out).toContain('none on this board');
    expect(out).toContain('This plan may be incomplete.');
  });

  it('nothing, when a plan did everything', () => {
    expect(html(<PlanNotes plan={plan()} />)).toBe('');
  });

  it('the ghost layer, naming what goes inside a container', () => {
    useAgent.setState({
      run: run({
        plan: plan({
          actions: [
            action({ position: { x: 0, y: 0, width: 320 } }),
            action({ seq: 1, kind: 'move_element', elementId: 'card-1', title: undefined, text: 'Check pricing', parentId: 'c1' }),
          ],
        }),
      }),
    });
    const out = html(<GhostLayer />);
    // A count was the entire review surface for filing. The names are the fix.
    expect(out).toContain('Check pricing');
  });

  // The server lays out every canvas a plan writes to, so an action filed into
  // a NESTED board now arrives with a position — in that board's coordinate
  // space, not this one's. Drawn on the root canvas it is a phantom card at a
  // meaningless place, promising something that will not happen there.
  it('the ghost layer, not drawing a card destined for another canvas', () => {
    useAgent.setState({
      run: run({
        plan: plan({
          actions: [
            action({ title: 'On this board', position: { x: 0, y: 0, width: 320 } }),
            action({
              seq: 1, elementId: 'c2', title: 'Inside the sub-board',
              parentId: 'sub-board', position: { x: 40, y: 40, width: 320 },
            }),
          ],
        }),
      }),
    });
    const out = html(<GhostLayer />);
    expect(out).toContain('On this board');
    expect(out).not.toContain('Inside the sub-board');
  });

  // The other half of the same coin. Suppressing the phantom card was correct
  // and left the review with NOTHING to say about a plan that files thirty
  // elements into sub-boards: five untouched tiles and a text list, and the
  // person approving what they cannot see. The badge is what the tile says
  // instead, and the count is rolled up through the column the cards actually
  // go into — a badge that only counted direct children would read "+1" on the
  // tile everything was going into.
  it('the ghost layer, badging a nested board tile by what is going inside it', () => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        'sub-board': el('sub-board', 'BOARD', 'b1', { x: 120, y: 60 }, 260, 180),
        'col-1': el('col-1', 'COLUMN', 'sub-board'),
      } as never,
    });
    useAgent.setState({
      run: run({
        plan: plan({
          actions: [
            action({ seq: 0, kind: 'create_column', elementId: 'c9', title: 'Sound', parentId: 'sub-board' }),
            action({ seq: 1, kind: 'create_note', elementId: 'n1', title: undefined, text: 'Assembly cut', parentId: 'col-1' }),
            action({ seq: 2, kind: 'create_note', elementId: 'n2', title: undefined, text: 'Colour grade', parentId: 'col-1' }),
            // Somewhere else entirely: it must not be counted on this tile.
            action({ seq: 3, kind: 'create_note', elementId: 'n3', title: undefined, text: 'On the root', parentId: 'b1' }),
          ],
        }),
      }),
    });
    const out = html(<GhostLayer />);
    expect(out).toContain('+3');
    expect(out).toContain('inside');
    // And still no phantom: nothing destined for the other canvas is drawn here.
    expect(out).not.toContain('Assembly cut');
  });

  // A plan whose actions came back null took the whole bar down once.
  it('survives a plan with null actions', () => {
    const r = run({ state: 'COMPLETED', plan: { actions: null } as unknown as AgentPlan });
    useAgent.setState({ run: r });
    expect(() => html(<Done run={r} />)).not.toThrow();
    expect(() => html(<Decide run={r} />)).not.toThrow();
    expect(() => html(<GhostLayer />)).not.toThrow();
  });
});
