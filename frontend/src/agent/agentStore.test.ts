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
});
