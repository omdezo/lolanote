// Table formula engine (§4.10): cells starting with '=' evaluate arithmetic
// over numbers, cell references (A1), and ranges (A1:B3) with SUM / AVG /
// MIN / MAX / COUNT. A recursive-descent parser — no dependencies, no eval.
// Errors surface as '#ERR', circular references as '#REF'.
import { normalizeDigits } from './direction';

export type Cells = string[][];

// colIndex: 'A' → 0, 'Z' → 25, 'AA' → 26 …
function colIndex(letters: string): number {
  let n = 0;
  for (const ch of letters) n = n * 26 + (ch.charCodeAt(0) - 64);
  return n - 1;
}

interface Ref { row: number; col: number }

function parseRef(token: string): Ref | null {
  const m = /^([A-Z]+)([0-9]+)$/.exec(token);
  if (!m) return null;
  return { row: parseInt(m[2], 10) - 1, col: colIndex(m[1]) };
}

// numeric parses a plain cell value, tolerating Arabic-Indic digits,
// thousands separators, and currency/percent decorations.
function numeric(raw: string): number | null {
  const cleaned = normalizeDigits(raw).replace(/[,%$€£\s]/g, '');
  if (cleaned === '') return null;
  const n = Number(cleaned);
  return Number.isNaN(n) ? null : n;
}

const FUNCTIONS = new Set(['SUM', 'AVG', 'AVERAGE', 'MIN', 'MAX', 'COUNT']);

class Parser {
  private pos = 0;
  constructor(
    private src: string,
    private cells: Cells,
    private evaluating: Set<string>,
  ) {}

  parse(): number {
    const v = this.expr();
    this.skipWs();
    if (this.pos < this.src.length) throw new Error('trailing input');
    return v;
  }

  private skipWs() {
    while (this.pos < this.src.length && this.src[this.pos] === ' ') this.pos++;
  }

  private peek(): string {
    this.skipWs();
    return this.src[this.pos] ?? '';
  }

  private expr(): number {
    let v = this.term();
    for (;;) {
      const op = this.peek();
      if (op === '+' || op === '-') {
        this.pos++;
        const rhs = this.term();
        v = op === '+' ? v + rhs : v - rhs;
      } else return v;
    }
  }

  private term(): number {
    let v = this.factor();
    for (;;) {
      const op = this.peek();
      if (op === '*' || op === '/') {
        this.pos++;
        const rhs = this.factor();
        v = op === '*' ? v * rhs : v / rhs;
      } else return v;
    }
  }

  private factor(): number {
    const ch = this.peek();
    if (ch === '-') { this.pos++; return -this.factor(); }
    if (ch === '+') { this.pos++; return this.factor(); }
    if (ch === '(') {
      this.pos++;
      const v = this.expr();
      if (this.peek() !== ')') throw new Error('missing )');
      this.pos++;
      return v;
    }
    // Number literal (Western or Arabic-Indic digits).
    const numMatch = /^[0-9٠-٩۰-۹]+(\.[0-9٠-٩۰-۹]+)?/.exec(this.src.slice(this.pos));
    if (numMatch) {
      this.pos += numMatch[0].length;
      return Number(normalizeDigits(numMatch[0]));
    }
    // Function call or cell reference.
    const identMatch = /^[A-Za-z]+[A-Za-z0-9]*/.exec(this.src.slice(this.pos));
    if (identMatch) {
      const ident = identMatch[0].toUpperCase();
      this.pos += identMatch[0].length;
      if (FUNCTIONS.has(ident)) return this.callFunction(ident);
      const ref = parseRef(ident);
      if (ref) return this.cellValue(ref);
      throw new Error(`unknown: ${ident}`);
    }
    throw new Error('unexpected input');
  }

  private callFunction(name: string): number {
    if (this.peek() !== '(') throw new Error('missing (');
    this.pos++;
    const values: number[] = [];
    for (;;) {
      // Range (A1:B3) or expression argument.
      const save = this.pos;
      const rangeMatch = /^\s*([A-Za-z]+[0-9]+)\s*:\s*([A-Za-z]+[0-9]+)/.exec(this.src.slice(this.pos));
      if (rangeMatch) {
        this.pos += rangeMatch[0].length;
        const a = parseRef(rangeMatch[1].toUpperCase())!;
        const b = parseRef(rangeMatch[2].toUpperCase())!;
        for (let r = Math.min(a.row, b.row); r <= Math.max(a.row, b.row); r++) {
          for (let c = Math.min(a.col, b.col); c <= Math.max(a.col, b.col); c++) {
            const raw = this.cells[r]?.[c];
            if (raw === undefined) continue;
            const v = this.rawValue({ row: r, col: c });
            if (v !== null) values.push(v);
          }
        }
      } else {
        this.pos = save;
        values.push(this.expr());
      }
      const ch = this.peek();
      if (ch === ',') { this.pos++; continue; }
      if (ch === ')') { this.pos++; break; }
      throw new Error('bad arguments');
    }
    switch (name) {
      case 'SUM': return values.reduce((a, b) => a + b, 0);
      case 'AVG':
      case 'AVERAGE': return values.length ? values.reduce((a, b) => a + b, 0) / values.length : 0;
      case 'MIN': return values.length ? Math.min(...values) : 0;
      case 'MAX': return values.length ? Math.max(...values) : 0;
      case 'COUNT': return values.length;
      default: throw new Error('unknown function');
    }
  }

  // cellValue: a referenced cell must resolve to a number (formulas recurse).
  private cellValue(ref: Ref): number {
    const v = this.rawValue(ref);
    if (v === null) throw new Error('non-numeric ref');
    return v;
  }

  // rawValue returns the numeric value of a cell, or null for text/empty.
  private rawValue(ref: Ref): number | null {
    const raw = this.cells[ref.row]?.[ref.col] ?? '';
    if (raw.startsWith('=')) {
      const key = `${ref.row}:${ref.col}`;
      if (this.evaluating.has(key)) throw new Error('circular');
      this.evaluating.add(key);
      try {
        return new Parser(raw.slice(1), this.cells, this.evaluating).parse();
      } finally {
        this.evaluating.delete(key);
      }
    }
    return numeric(raw);
  }
}

// evaluateCell renders a cell for display: formulas compute, everything else
// passes through. Returns { display, isFormula, error }.
export function evaluateCell(raw: string, cells: Cells, row: number, col: number): { display: string; isFormula: boolean; error: boolean } {
  if (!raw.startsWith('=')) return { display: raw, isFormula: false, error: false };
  try {
    const evaluating = new Set<string>([`${row}:${col}`]);
    const v = new Parser(raw.slice(1), cells, evaluating).parse();
    const rounded = Math.round(v * 1e6) / 1e6;
    return { display: String(rounded), isFormula: true, error: false };
  } catch (err) {
    return {
      display: (err as Error).message === 'circular' ? '#REF' : '#ERR',
      isFormula: true,
      error: true,
    };
  }
}
