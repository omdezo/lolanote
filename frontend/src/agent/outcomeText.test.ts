import { describe, expect, it } from 'vitest';
import { outcomeText } from './agentStore';
import type { AgentRun, AgentRunState } from '../api/types';

const runIn = (state: AgentRunState, reason = ''): AgentRun =>
  ({ id: 'r1', state, reason } as unknown as AgentRun);

// FAILED and DENIED are different things that happened, and for a long time
// they rendered as the same sentence because both fell through to the same
// `default:` arm. "Could not finish — something went wrong" in front of a
// permission refusal teaches people the agent is flaky, when in fact it did
// exactly what it was told and the server said no.
describe('outcomeText', () => {
  it('distinguishes a refusal from a malfunction', () => {
    const denied = outcomeText(runIn('DENIED'));
    const failed = outcomeText(runIn('FAILED'));

    expect(denied.title).not.toBe(failed.title);
    expect(denied.detail).not.toBe(failed.detail);
    // A refusal is not a fault: it must not be dressed in the error tone.
    expect(denied.tone).toBe('warn');
    expect(failed.tone).toBe('bad');
  });

  it('prefers the server reason over the generic line, in both cases', () => {
    expect(outcomeText(runIn('DENIED', 'you can only comment on this board')).detail)
      .toBe('you can only comment on this board');
    expect(outcomeText(runIn('FAILED', 'the proposed changes did not pass validation')).detail)
      .toBe('the proposed changes did not pass validation');
  });

  it('gives every terminal state its own copy, not a shared fallback', () => {
    const states: AgentRunState[] = [
      'COMPLETED', 'REVERTED', 'PARTIAL', 'DISCARDED', 'CANCELLED',
      'BUDGET_EXHAUSTED', 'SECURITY_QUARANTINED', 'DENIED', 'FAILED',
    ];
    const titles = states.map((s) => outcomeText(runIn(s)).title);
    expect(new Set(titles).size).toBe(states.length);
  });

  it('counts a completed run through the translator, never a bare -s', () => {
    const one = { id: 'r', state: 'COMPLETED', plan: { actions: [{}] } } as unknown as AgentRun;
    const many = { id: 'r', state: 'COMPLETED', plan: { actions: [{}, {}] } } as unknown as AgentRun;
    // A stub translator returns the key, which is enough to see that the
    // singular and the plural came from two different dictionary entries.
    const key = (k: string) => k;
    expect(outcomeText(one, key as never).detail).toContain('agent.change');
    expect(outcomeText(many, key as never).detail).toContain('agent.changes');
  });

  it('survives a completed run whose plan came back with null actions', () => {
    const nulled = { id: 'r', state: 'COMPLETED', plan: { actions: null } } as unknown as AgentRun;
    expect(() => outcomeText(nulled)).not.toThrow();
  });
});
