import { beforeEach, describe, expect, it } from 'vitest';
import { forgetSavedView, readSavedView, saveView } from './savedView';

// Opening a board or refreshing dropped you at the world origin: the view
// defaulted to (0,0) at 100%, nothing was ever persisted, and the only way to
// find your own content was to know that Z means "fit". On a board the agent
// had laid out, "open my board" answered with an empty grey field.
describe('where you were looking is remembered per board', () => {
  beforeEach(() => localStorage.clear());

  it('returns you to the position you left', () => {
    saveView('board-a', { panX: -1200, panY: 340, scale: 0.75 });
    expect(readSavedView('board-a')).toEqual({ panX: -1200, panY: 340, scale: 0.75 });
  });

  it('says nothing about a board you have never opened, so it gets framed instead', () => {
    expect(readSavedView('board-never-seen')).toBeNull();
  });

  it('keeps boards apart', () => {
    saveView('board-a', { panX: 10, panY: 10, scale: 1 });
    saveView('board-b', { panX: -900, panY: 50, scale: 0.4 });
    expect(readSavedView('board-a')?.panX).toBe(10);
    expect(readSavedView('board-b')?.panX).toBe(-900);
  });

  // A stored scale of zero divides the canvas into nothing and a NaN pan puts
  // every element at an unrenderable coordinate. Both are cheaper to reject
  // than to debug from a screenshot.
  it('refuses a stored value that would break the canvas', () => {
    localStorage.setItem('qomra.view', JSON.stringify({
      bad1: { panX: 0, panY: 0, scale: 0, at: 1 },
      bad2: { panX: NaN, panY: 0, scale: 1, at: 1 },
    }));
    expect(readSavedView('bad1')).toBeNull();
    expect(readSavedView('bad2')).toBeNull();
  });

  it('survives a corrupt store rather than stopping a board from opening', () => {
    localStorage.setItem('qomra.view', 'not json{');
    expect(() => readSavedView('board-a')).not.toThrow();
    expect(readSavedView('board-a')).toBeNull();
  });

  it('bounds what it keeps, so a year of use cannot grow without limit', () => {
    for (let i = 0; i < 60; i++) saveView(`b${i}`, { panX: i, panY: 0, scale: 1 });
    const stored = Object.keys(JSON.parse(localStorage.getItem('qomra.view')!));
    expect(stored.length).toBeLessThanOrEqual(40);
    // The ones it keeps are the ones you looked at most recently.
    expect(readSavedView('b59')).not.toBeNull();
  });

  it('forgets a board, so a recycled id cannot inherit a stranger position', () => {
    saveView('board-a', { panX: 5, panY: 5, scale: 1 });
    forgetSavedView('board-a');
    expect(readSavedView('board-a')).toBeNull();
  });
});
