// Board customization popover (right-click a board → Customize): tile color
// swatches and an icon (emoji) grid, exactly the Color / Icon controls
// Milanote hangs off a board card. Writes content.color / content.icon via
// the transaction pipeline (undoable, realtime-synced).
import { useEffect, useRef } from 'react';
import { create } from 'zustand';
import { updateOp, useBoard } from '../../store/boardStore';

const COLORS = [
  '', // auto (hash gradient)
  'linear-gradient(135deg,#6e6cf0,#4a48c4)',
  'linear-gradient(135deg,#5fb0f5,#1c7ed6)',
  'linear-gradient(135deg,#63e6be,#0c8599)',
  'linear-gradient(135deg,#4dd0a6,#0ca678)',
  'linear-gradient(135deg,#ffc94d,#f08c00)',
  'linear-gradient(135deg,#ff8a65,#e8590c)',
  'linear-gradient(135deg,#f78fb3,#e64980)',
  'linear-gradient(135deg,#9775fa,#7048e8)',
  'linear-gradient(135deg,#a8b2bd,#5f6b76)',
  'linear-gradient(135deg,#495057,#212529)',
  '#a3c7f0', '#f0b6c5', '#f6d9a0', '#b8e6c9', '#d6c9f0', '#e8e2d5',
];

const ICONS = [
  '', // none (default glyph)
  '📝', '💡', '🎨', '🎬', '📷', '🎵', '📚', '🔬', '💼', '🏠', '✈️',
  '🍽️', '🛒', '💪', '🌱', '⭐', '❤️', '🔥', '🎯', '🧭', '📅', '🗂️',
  '🎉', '🕌', '📿', '🖋️', '🧠', '⚙️', '💰', '🎓', '🏗️', '🌍',
];

interface BoardStyleState {
  pos: { x: number; y: number } | null;
  elementId: string;
  open(x: number, y: number, elementId: string): void;
  close(): void;
}

export const useBoardStyle = create<BoardStyleState>((set) => ({
  pos: null,
  elementId: '',
  open: (x, y, elementId) => set({ pos: { x, y }, elementId }),
  close: () => set({ pos: null, elementId: '' }),
}));

export function BoardStylePopoverHost() {
  const { pos, elementId, close } = useBoardStyle();
  const element = useBoard((s) => s.elements[elementId]);
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!pos) return;
    const onDown = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) close();
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && close();
    window.addEventListener('pointerdown', onDown, true);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('pointerdown', onDown, true);
      window.removeEventListener('keydown', onKey);
    };
  }, [pos, close]);

  if (!pos || !element) return null;
  const current = element.content ?? {};
  const set = (patch: Record<string, unknown>) =>
    void commitTransaction([updateOp(element, { content: patch })]);

  const x = Math.min(pos.x, window.innerWidth - 292);
  const y = Math.min(pos.y, window.innerHeight - 330);

  return (
    <div ref={ref} className="board-style-pop" style={{ left: x, top: y }} onPointerDown={(e) => e.stopPropagation()}>
      <div className="bsp-label">Color</div>
      <div className="bsp-grid bsp-colors">
        {COLORS.map((color) => (
          <button
            key={color || 'auto'}
            className={`bsp-swatch${(current.color ?? '') === color ? ' on' : ''}${color === '' ? ' auto' : ''}`}
            style={color ? { background: color } : undefined}
            title={color === '' ? 'Automatic' : undefined}
            onClick={() => set({ color: color || null })}
          >
            {color === '' && 'A'}
          </button>
        ))}
      </div>
      <div className="bsp-label">Icon</div>
      <div className="bsp-grid bsp-icons">
        {ICONS.map((icon) => (
          <button
            key={icon || 'none'}
            className={`bsp-icon${(current.icon ?? '') === icon ? ' on' : ''}`}
            title={icon === '' ? 'Default' : undefined}
            onClick={() => set({ icon: icon || null })}
          >
            {icon || '—'}
          </button>
        ))}
      </div>
    </div>
  );
}
