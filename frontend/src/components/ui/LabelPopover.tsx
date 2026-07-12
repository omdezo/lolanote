// Label popover: create a new label or toggle existing ones on the selected
// element(s). Positioned at the pointer like the context menu.
import { useEffect, useRef, useState } from 'react';
import { create } from 'zustand';
import { useLabels } from '../../store/labels';
import { useBoard } from '../../store/boardStore';
import { CheckIcon, PlusIcon } from '../Icons';

interface PopState {
  target: { x: number; y: number; elementIds: string[] } | null;
  open(x: number, y: number, elementIds: string[]): void;
  close(): void;
}
export const useLabelPopover = create<PopState>((set) => ({
  target: null,
  open: (x, y, elementIds) => set({ target: { x, y, elementIds } }),
  close: () => set({ target: null }),
}));

const palette = ['#e8590c', '#5e5ce6', '#2eb85c', '#1c7ed6', '#f2cc0d', '#212529', '#e64980', '#0ca678'];

// LabelChips renders an element's labels as colored pills. Dimmed when a label
// filter is active and this element doesn't match.
export function LabelChips({ labelIds }: { labelIds?: string[] }) {
  const labels = useLabels((s) => s.labels);
  if (!labelIds || labelIds.length === 0) return null;
  const mine = labelIds.map((id) => labels.find((l) => l.id === id)).filter(Boolean);
  if (mine.length === 0) return null;
  return (
    <div className="label-chips">
      {mine.map((l) => (
        <span key={l!.id} className="label-chip" style={{ background: l!.color }}>{l!.name}</span>
      ))}
    </div>
  );
}

export function LabelPopoverHost() {
  const { target, close } = useLabelPopover();
  const { labels, create: createLabel, attach, detach, load } = useLabels();
  const [name, setName] = useState('');
  const ref = useRef<HTMLDivElement>(null);
  const elements = useBoard((s) => s.elements);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    if (!target) return;
    const onDown = (e: PointerEvent) => { if (!ref.current?.contains(e.target as Node)) close(); };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && close();
    window.addEventListener('pointerdown', onDown, true);
    window.addEventListener('keydown', onKey);
    return () => { window.removeEventListener('pointerdown', onDown, true); window.removeEventListener('keydown', onKey); };
  }, [target, close]);

  if (!target) return null;
  const x = Math.min(target.x, window.innerWidth - 260);
  const y = Math.min(target.y, window.innerHeight - 320);

  // A label is "on" if every targeted element carries it.
  const isOn = (labelId: string) =>
    target.elementIds.every((id) => elements[id]?.labelIds?.includes(labelId));

  const toggle = async (labelId: string) => {
    const on = isOn(labelId);
    await Promise.all(target.elementIds.map((id) => (on ? detach(id, labelId) : attach(id, labelId))));
  };

  const submitNew = async () => {
    const trimmed = name.trim();
    if (!trimmed) return;
    const color = palette[labels.length % palette.length];
    const label = await createLabel(trimmed, color);
    await Promise.all(target.elementIds.map((id) => attach(id, label.id)));
    setName('');
  };

  return (
    <div ref={ref} className="label-popover" style={{ left: x, top: y }}>
      <input
        className="label-new-input"
        placeholder="Create or search labels…"
        value={name}
        autoFocus
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === 'Enter' && void submitNew()}
      />
      <div className="label-list">
        {labels
          .filter((l) => l.name.toLowerCase().includes(name.toLowerCase()))
          .map((l) => (
            <button key={l.id} className="label-row" onClick={() => void toggle(l.id)}>
              <span className="label-dot" style={{ background: l.color }} />
              <span className="label-name">{l.name}</span>
              {isOn(l.id) && <CheckIcon size={14} />}
            </button>
          ))}
        {name.trim() && !labels.some((l) => l.name.toLowerCase() === name.trim().toLowerCase()) && (
          <button className="label-row create" onClick={() => void submitNew()}>
            <PlusIcon size={14} /> Create “{name.trim()}”
          </button>
        )}
      </div>
    </div>
  );
}
