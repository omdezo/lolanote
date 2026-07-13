import { describe, expect, it } from 'vitest';
import { evaluateCell, type Cells } from './formula';

const grid: Cells = [
  ['Item', 'Qty', 'Price'],
  ['Apples', '4', '2.5'],
  ['Bread', '2', '1.25'],
  ['Milk', '٣', '4'], // Arabic-Indic quantity
];

const evalAt = (raw: string, cells: Cells = grid) => evaluateCell(raw, cells, 9, 9);

describe('table formulas', () => {
  it('passes plain text through untouched', () => {
    expect(evalAt('hello')).toEqual({ display: 'hello', isFormula: false, error: false });
  });

  it('does arithmetic with precedence and parens', () => {
    expect(evalAt('=2+3*4').display).toBe('14');
    expect(evalAt('=(2+3)*4').display).toBe('20');
    expect(evalAt('=-5+2').display).toBe('-3');
  });

  it('resolves cell references', () => {
    expect(evalAt('=B2*C2').display).toBe('10'); // 4 * 2.5
  });

  it('sums ranges, skipping text cells', () => {
    expect(evalAt('=SUM(B2:B4)').display).toBe('9'); // 4 + 2 + ٣
  });

  it('supports AVG, MIN, MAX, COUNT', () => {
    expect(evalAt('=AVG(B2:B4)').display).toBe('3');
    expect(evalAt('=MIN(C2:C4)').display).toBe('1.25');
    expect(evalAt('=MAX(C2:C4)').display).toBe('4');
    expect(evalAt('=COUNT(A1:C4)').display).toBe('6'); // numeric cells only
  });

  it('understands Arabic-Indic digits in literals and cells', () => {
    expect(evalAt('=٥+٥').display).toBe('10');
  });

  it('evaluates nested formulas in referenced cells', () => {
    const g: Cells = [['=2*3', '=A1+1']];
    expect(evaluateCell('=B1', g, 5, 5).display).toBe('7');
  });

  it('flags circular references as #REF', () => {
    const g: Cells = [['=B1', '=A1']];
    expect(evaluateCell(g[0][0], g, 0, 0).display).toBe('#REF');
  });

  it('flags garbage as #ERR', () => {
    expect(evalAt('=WHAT(').error).toBe(true);
    expect(evalAt('=1++').error).toBe(true);
  });
});
