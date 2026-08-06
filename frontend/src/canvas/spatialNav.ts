// Moving between cards on a 2D canvas without a pointer.
//
// The last wave put `tabIndex={0}` on every shell, which made cards focusable
// and was an admitted floor rather than an answer: a 200-card board became 200
// tab stops, and reaching the card beside the one you are looking at meant
// pressing Tab until the browser happened to arrive there in DOM order — which
// on a canvas bears no relation to where anything is. A roving index is one tab
// stop into the board and arrow keys inside it, which is how every other 2D
// grid in a browser behaves.
//
// Pure geometry, no DOM: the caller measures, this decides. That is what makes
// "the card to the right" something a test can hold to.

export interface NavBox {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

export type Direction = 'left' | 'right' | 'up' | 'down';

interface Centre { id: string; cx: number; cy: number; box: NavBox }

function centre(b: NavBox): Centre {
  return { id: b.id, cx: b.x + b.w / 2, cy: b.y + b.h / 2, box: b };
}

/**
 * The card a person means when they press an arrow.
 *
 * Scored rather than nearest-by-distance: pure distance sends Right to a card
 * that is barely to the right and mostly below, which reads as the focus
 * jumping sideways off the row you were reading. Weighting the off-axis offset
 * keeps movement along the axis the person asked for, and the fallback below
 * keeps the key from doing nothing when the row simply ends.
 */
export function nextInDirection(boxes: NavBox[], fromId: string, dir: Direction): string | null {
  const from = boxes.find((b) => b.id === fromId);
  if (!from) return boxes.length > 0 ? readingOrder(boxes)[0].id : null;
  const here = centre(from);

  const axis = (c: Centre) => (dir === 'left' || dir === 'right' ? c.cx - here.cx : c.cy - here.cy);
  const cross = (c: Centre) => (dir === 'left' || dir === 'right' ? c.cy - here.cy : c.cx - here.cx);
  const forward = (v: number) => (dir === 'right' || dir === 'down' ? v : -v);

  let best: { id: string; score: number } | null = null;
  for (const b of boxes) {
    if (b.id === fromId) continue;
    const c = centre(b);
    const along = forward(axis(c));
    if (along <= 1) continue; // behind, or on the same line
    const off = Math.abs(cross(c));
    // Off-axis costs double: "the next one along this row" beats "the nearest
    // thing that happens to be that way".
    const score = along + off * 2;
    if (!best || score < best.score) best = { id: b.id, score };
  }
  if (best) return best.id;

  // Nothing that way. Wrapping to the far side would teleport the person across
  // the board; staying put and letting the boundary be felt is the honest
  // answer, and it is what a grid does.
  return null;
}

/**
 * Everything on the canvas in the order a person would read it — top band
 * first, then left to right within the band.
 *
 * Not DOM order: shells are rendered in whatever order the element map
 * enumerates, which is insertion order on the server and has nothing to do with
 * where anything sits. Home and End are the only way to get from one end of a
 * wide board to the other without pressing an arrow forty times.
 */
export function readingOrder(boxes: NavBox[]): NavBox[] {
  // Rows are banded so two cards side by side, one twenty pixels lower than the
  // other, still read as one row.
  const BAND = 60;
  return [...boxes].sort((a, b) => {
    const rowA = Math.round(a.y / BAND);
    const rowB = Math.round(b.y / BAND);
    if (rowA !== rowB) return rowA - rowB;
    return a.x - b.x;
  });
}
