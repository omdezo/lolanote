// Freehand stroke reduction.
//
// A thirty-second scribble is several thousand raw pointer samples, and the
// element holding them then rides in full through every hot path the product
// has: the board-children read on every open and every reconnect, Descendants
// in duplicate/export/delete-account, the read-modify-replace behind every
// position nudge, and the transaction ops twice per edit (once as the change,
// once as the undo copy). One element a person creates in half a minute became
// a permanent multiplier on five of them.
//
// Ramer–Douglas–Peucker is the standard treatment because it removes the points
// that carry no shape: a slow straight drag emits a sample every few
// milliseconds and every one of them is on the same line. What survives is
// visually the same stroke an order of magnitude smaller.

export type StrokePoint = [number, number];

/** Below this perpendicular distance (in canvas px) a point says nothing the
 *  line either side of it does not already say. */
const EPSILON = 1.1;

/** A ceiling no single stroke may pass, whatever its shape. Reached only by a
 *  genuinely intricate scribble; a signature lands around 150. */
const MAX_POINTS = 600;

/** Perpendicular distance from `p` to the segment `a`–`b`. */
function perpendicular(p: StrokePoint, a: StrokePoint, b: StrokePoint): number {
  const dx = b[0] - a[0];
  const dy = b[1] - a[1];
  const len = Math.hypot(dx, dy);
  if (len === 0) return Math.hypot(p[0] - a[0], p[1] - a[1]);
  return Math.abs(dy * p[0] - dx * p[1] + b[0] * a[1] - b[1] * a[0]) / len;
}

/**
 * Ramer–Douglas–Peucker, iterative.
 *
 * Iterative rather than recursive on purpose: the input is user-controlled and
 * unbounded, and a straight drag across a wide board is exactly the shape that
 * recurses deepest.
 */
function rdp(points: StrokePoint[], epsilon: number): StrokePoint[] {
  if (points.length < 3) return points.slice();
  const keep = new Uint8Array(points.length);
  keep[0] = 1;
  keep[points.length - 1] = 1;
  const stack: Array<[number, number]> = [[0, points.length - 1]];
  while (stack.length) {
    const [first, last] = stack.pop()!;
    let worst = 0;
    let index = -1;
    for (let i = first + 1; i < last; i++) {
      const d = perpendicular(points[i], points[first], points[last]);
      if (d > worst) { worst = d; index = i; }
    }
    if (index >= 0 && worst > epsilon) {
      keep[index] = 1;
      stack.push([first, index], [index, last]);
    }
  }
  const out: StrokePoint[] = [];
  for (let i = 0; i < points.length; i++) if (keep[i]) out.push(points[i]);
  return out;
}

/**
 * The stroke as it should be STORED: shape-preserving, bounded, and rounded to
 * a tenth of a pixel because nothing on a canvas can show more than that and
 * the extra digits are pure payload.
 */
export function simplifyStroke(
  points: StrokePoint[],
  epsilon = EPSILON,
  maxPoints = MAX_POINTS,
): StrokePoint[] {
  let out = rdp(points, epsilon);
  // A shape so intricate that RDP kept too much still has to fit. Doubling the
  // tolerance until it does keeps the reduction uniform along the stroke —
  // dropping every Nth point instead would put the loss wherever the person
  // happened to draw fastest.
  for (let eps = epsilon * 2; out.length > maxPoints && eps < 64; eps *= 2) {
    out = rdp(points, eps);
  }
  if (out.length > maxPoints) {
    const step = out.length / maxPoints;
    const thinned: StrokePoint[] = [];
    for (let i = 0; i < maxPoints - 1; i++) thinned.push(out[Math.floor(i * step)]);
    thinned.push(out[out.length - 1]);
    out = thinned;
  }
  return out.map(([x, y]) => [Math.round(x * 10) / 10, Math.round(y * 10) / 10]);
}
