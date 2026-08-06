import { describe, expect, it } from 'vitest';
import { simplifyStroke, type StrokePoint } from './stroke';

// SC23, write side. A thirty-second scribble stored several thousand raw
// coordinate pairs in one element, and that element then rode in full through
// board open, reconnect, duplicate, export, every position nudge and both
// halves of every transaction. The assertions here are about SIZE and SHAPE
// together: a reduction that loses the drawing is not a reduction.

/** Points along a straight line, sampled the way a slow drag samples. */
function straight(n: number): StrokePoint[] {
  return Array.from({ length: n }, (_, i) => [i * 2, i * 2] as StrokePoint);
}

/** A closed-ish loop, the shape of an actual scribble. */
function loop(n: number, radius = 120): StrokePoint[] {
  return Array.from({ length: n }, (_, i) => {
    const a = (i / n) * Math.PI * 2;
    return [radius + Math.cos(a) * radius, radius + Math.sin(a) * radius] as StrokePoint;
  });
}

/** Largest distance from any original point to the reduced polyline. */
function maxDeviation(original: StrokePoint[], reduced: StrokePoint[]): number {
  let worst = 0;
  for (const p of original) {
    let best = Infinity;
    for (let i = 0; i < reduced.length - 1; i++) {
      const [ax, ay] = reduced[i];
      const [bx, by] = reduced[i + 1];
      const dx = bx - ax, dy = by - ay;
      const len2 = dx * dx + dy * dy;
      const t = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((p[0] - ax) * dx + (p[1] - ay) * dy) / len2));
      best = Math.min(best, Math.hypot(p[0] - (ax + t * dx), p[1] - (ay + t * dy)));
    }
    worst = Math.max(worst, best);
  }
  return worst;
}

describe('a stored stroke is the drawing, not the sampling rate', () => {
  it('collapses a straight drag to its two ends', () => {
    // 900 samples down one line: every interior point is on the segment
    // through its neighbours and says nothing.
    expect(simplifyStroke(straight(900)).length).toBe(2);
  });

  it('keeps the shape of a curve it shrinks', () => {
    const raw = loop(2_000);
    const out = simplifyStroke(raw);
    expect(out.length, 'no reduction happened at all').toBeLessThan(raw.length / 4);
    expect(maxDeviation(raw, out), 'the reduced stroke is a different drawing').toBeLessThan(3);
  });

  it('caps a stroke no tolerance can shrink', () => {
    // Pure noise: RDP has nothing structural to remove, so the cap is the only
    // thing standing between one element and an unbounded document.
    let seed = 7;
    const noise: StrokePoint[] = Array.from({ length: 8_000 }, () => {
      seed = (seed * 1103515245 + 12345) % 2147483648;
      const a = (seed / 2147483648) * 400;
      seed = (seed * 1103515245 + 12345) % 2147483648;
      return [a, (seed / 2147483648) * 400];
    });
    expect(simplifyStroke(noise).length).toBeLessThanOrEqual(600);
  });

  it('keeps both endpoints, so the stroke starts and ends where it was drawn', () => {
    const raw = loop(500);
    const out = simplifyStroke(raw);
    expect(out[0]).toEqual([Math.round(raw[0][0] * 10) / 10, Math.round(raw[0][1] * 10) / 10]);
    const lastRaw = raw[raw.length - 1];
    expect(out[out.length - 1]).toEqual([
      Math.round(lastRaw[0] * 10) / 10, Math.round(lastRaw[1] * 10) / 10,
    ]);
  });

  it('rounds to a tenth of a pixel — nothing on a canvas can show more', () => {
    const out = simplifyStroke([[0.123456, 1.987654], [40, 40], [0.5, 80.44444]]);
    for (const [x, y] of out) {
      expect(Math.round(x * 10)).toBe(x * 10);
      expect(Math.round(y * 10)).toBe(y * 10);
    }
  });

  it('leaves a two-point stroke alone', () => {
    expect(simplifyStroke([[0, 0], [10, 10]])).toEqual([[0, 0], [10, 10]]);
  });
});
