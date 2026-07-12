// Table card (§4.10): an editable grid with a header row, Tab/Enter
// navigation, add/remove rows and columns, and auto-detected numeric
// alignment. Cell edits commit one transaction per blur so undo works
// per-cell. (The research notes Milanote embeds Handsontable+HyperFormula;
// this is a clean-room grid with the formula engine as a future extension.)
import { useState } from 'react';
import type { QElement } from '../../api/types';
import { updateOp, useBoard } from '../../store/boardStore';
import { MinusIcon, PlusIcon } from '../Icons';

type Cells = string[][];

export function TableCard({ element }: { element: QElement }) {
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const selection = useBoard((s) => s.selection);
  const cells: Cells = normalize(element.content?.cells);
  const [draft, setDraft] = useState<{ r: number; c: number; v: string } | null>(null);
  const selected = selection.has(element.id);

  const commitCells = (next: Cells) => {
    void commitTransaction([updateOp(element, { content: { cells: next } })]);
  };

  const commitDraft = () => {
    if (!draft) return;
    if (cells[draft.r]?.[draft.c] !== draft.v) {
      const next = cells.map((row) => [...row]);
      next[draft.r][draft.c] = draft.v;
      commitCells(next);
    }
    setDraft(null);
  };

  const addRow = () => commitCells([...cells.map((r) => [...r]), new Array(cells[0].length).fill('')]);
  const addCol = () => commitCells(cells.map((r) => [...r, '']));
  const delRow = () => cells.length > 1 && commitCells(cells.slice(0, -1).map((r) => [...r]));
  const delCol = () => cells[0].length > 1 && commitCells(cells.map((r) => r.slice(0, -1)));

  const isNumeric = (v: string) => v !== '' && !Number.isNaN(Number(v.replace(/[,%$€£]/g, '')));

  return (
    <div className="table-card">
      <table className="qn-table">
        <tbody>
          {cells.map((row, r) => (
            <tr key={r}>
              {row.map((val, c) => {
                const editing = draft && draft.r === r && draft.c === c;
                const value = editing ? draft.v : val;
                return (
                  <td key={c}>
                    <input
                      className={isNumeric(value) && r > 0 ? 'num' : undefined}
                      value={value}
                      onFocus={() => setDraft({ r, c, v: val })}
                      onChange={(e) => setDraft({ r, c, v: e.target.value })}
                      onBlur={commitDraft}
                      onPointerDown={(e) => e.stopPropagation()}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === 'Tab') {
                          e.preventDefault();
                          commitDraft();
                          // Move focus: Tab → next cell, Enter → cell below.
                          const inputs = [...(e.currentTarget.closest('table')?.querySelectorAll('input') ?? [])];
                          const idx = inputs.indexOf(e.currentTarget);
                          const next = e.key === 'Tab' ? inputs[idx + 1] : inputs[idx + row.length];
                          (next as HTMLInputElement | undefined)?.focus();
                        }
                        if (e.key === 'Escape') setDraft(null);
                      }}
                    />
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      {selected && (
        <div className="table-controls" onPointerDown={(e) => e.stopPropagation()}>
          <button className="table-ctl" onClick={addRow}><PlusIcon size={12} /> Row</button>
          <button className="table-ctl" onClick={addCol}><PlusIcon size={12} /> Column</button>
          <button className="table-ctl" onClick={delRow}><MinusIcon size={12} /> Row</button>
          <button className="table-ctl" onClick={delCol}><MinusIcon size={12} /> Column</button>
        </div>
      )}
    </div>
  );
}

function normalize(raw: unknown): Cells {
  if (Array.isArray(raw) && raw.length > 0) {
    const width = Math.max(...raw.map((r) => (Array.isArray(r) ? r.length : 0)), 1);
    return raw.map((r) => {
      const row = Array.isArray(r) ? r.map((v) => String(v ?? '')) : [];
      while (row.length < width) row.push('');
      return row;
    });
  }
  return [['', '', ''], ['', '', ''], ['', '', '']];
}
