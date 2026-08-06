import { describe, expect, it } from 'vitest';
import { computeEffective } from './agentStore';
import type { AgentPlan } from '../api/types';

// A plan with no actions is a legitimate outcome — the agent answering a
// question, or asking one — and Go marshals an empty slice as `null`. Every
// consumer that walks plan.actions then throws:
//
//     can't access property "forEach", n.actions is null
//
// which takes down the whole agent bar, not just the empty plan. The empty case
// is the rare one, so it shipped untested and broke the first time somebody
// asked a question instead of requesting a change.
describe('computeEffective', () => {
  it('survives a plan whose actions came back null', () => {
    // Deliberately cast: this is exactly the shape the wire produced, and the
    // type says it cannot happen. That gap is the bug.
    const answered = { actions: null, summary: 'Nothing is missing.' } as unknown as AgentPlan;

    const effective = computeEffective(answered, []);
    expect(effective).not.toBeNull();
    expect(effective!.actions).toEqual([]);
  });

  it('survives an adjustment aimed at a plan with no actions', () => {
    const answered = { actions: null, summary: 'Answer.' } as unknown as AgentPlan;
    // A stale adjustment from a previous plan must not index into nothing.
    const effective = computeEffective(answered, [{ kind: 'drop', seq: 3 }]);
    expect(effective!.actions).toEqual([]);
  });

  it('leaves a populated plan alone', () => {
    const plan: AgentPlan = {
      summary: 'Two columns.',
      actions: [
        { seq: 0, kind: 'create_column', elementId: 'c1', title: 'Pricing', summary: 'Pricing' },
        { seq: 1, kind: 'create_column', elementId: 'c2', title: 'Launch', summary: 'Launch' },
      ],
    } as AgentPlan;

    const effective = computeEffective(plan, []);
    expect(effective!.actions).toHaveLength(2);

    // Dropping one still works — the guard must not have flattened the plan.
    const dropped = computeEffective(plan, [{ kind: 'drop', seq: 0 }]);
    expect(dropped!.actions).toHaveLength(1);
    expect(dropped!.actions[0].elementId).toBe('c2');
  });

  it('returns null only when there is no plan at all', () => {
    expect(computeEffective(undefined, [])).toBeNull();
  });

  // IL2. This projection is the mirror of the server's ApplyAdjustmentsDetailed,
  // and the two must agree about what one click removes: the preview is the
  // promise, the commit is the fact, and six content-key mismatches have shipped
  // in this repo past tests that only checked one side.
  it('reports how many rows a dropped container takes with it', () => {
    const plan: AgentPlan = {
      summary: 'A column and three cards.',
      actions: [
        { seq: 0, kind: 'create_column', elementId: 'c1', title: 'Pricing', summary: 'Pricing' },
        { seq: 1, kind: 'create_note', elementId: 'n1', parentId: 'c1', text: 'a', summary: 'a' },
        { seq: 2, kind: 'create_note', elementId: 'n2', parentId: 'c1', text: 'b', summary: 'b' },
        { seq: 3, kind: 'create_note', elementId: 'n3', parentId: 'b1', text: 'c', summary: 'c' },
      ],
    } as AgentPlan;

    const effective = computeEffective(plan, [{ kind: 'drop', seq: 0 }])!;
    expect(effective.actions.map((a) => a.seq)).toEqual([3]);
    // The click is attributed to the row the person pressed, not to the rows
    // that vanished — that is what lets the list say "+2 inside it" against the
    // thing they can still put back.
    expect(effective.explicitlyDropped).toEqual([0]);
    expect(effective.cascadedFrom.get(0)).toBe(2);
  });

  // A duplicate's own elementId is NOT among the elements it creates: the copies
  // are, one per entry in `copies`. Reading elementId alone left every copy
  // looking alive after the duplicate was dropped, so the review list showed
  // rows the commit was about to remove — the server's CreatedIDs has always
  // walked the copies.
  it('cascades through a dropped duplicate to its copies', () => {
    const plan: AgentPlan = {
      summary: 'Copy the board and file a card into the copy.',
      actions: [
        {
          seq: 0, kind: 'duplicate', elementId: 'src', summary: 'copy',
          copies: [{ newId: 'copy-board', sourceId: 'src', parentId: 'b1' }],
        },
        { seq: 1, kind: 'create_note', elementId: 'n1', parentId: 'copy-board', text: 'x', summary: 'x' },
      ],
    } as AgentPlan;

    const effective = computeEffective(plan, [{ kind: 'drop', seq: 0 }])!;
    expect(effective.actions).toHaveLength(0);
    expect(effective.cascadedFrom.get(0)).toBe(1);
  });
});
