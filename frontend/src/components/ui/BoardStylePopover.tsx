// Board customization, Milanote-style: clicking a selected board's Color or
// Icon action opens the matching panel. Color = tile swatches. Icon = a
// tabbed picker (Recommended / Letters & numbers / Upload an image) with a
// working keyword search ("timer" → ⏱ ⏲ ⏰ ⌛ …). Custom image icons ride
// the normal presigned-upload pipeline. All writes go through the
// transaction pipeline (undoable, realtime-synced).
import { useEffect, useRef, useState } from 'react';
import { create } from 'zustand';
import { uploadFile } from '../../api/client';
import { ICON_CATALOG, LETTER_ICONS, searchIcons } from '../../lib/iconCatalog';
import { updateOp, useBoard } from '../../store/boardStore';
import { toast } from './Toaster';
import { SearchIcon } from '../Icons';

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

export type BoardStyleMode = 'color' | 'icon';

interface BoardStyleState {
  pos: { x: number; y: number } | null;
  elementId: string;
  mode: BoardStyleMode;
  open(x: number, y: number, elementId: string, mode?: BoardStyleMode): void;
  close(): void;
}

export const useBoardStyle = create<BoardStyleState>((set) => ({
  pos: null,
  elementId: '',
  mode: 'color',
  open: (x, y, elementId, mode = 'color') => set({ pos: { x, y }, elementId, mode }),
  close: () => set({ pos: null, elementId: '' }),
}));

export function BoardStylePopoverHost() {
  const { pos, elementId, mode, close } = useBoardStyle();
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

  const width = mode === 'icon' ? 460 : 292;
  const x = Math.min(pos.x, window.innerWidth - width - 16);
  const y = Math.min(pos.y, window.innerHeight - (mode === 'icon' ? 340 : 200));

  return (
    <div ref={ref} className="board-style-pop" style={{ left: x, top: y, width }} onPointerDown={(e) => e.stopPropagation()}>
      {mode === 'color' ? (
        <>
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
        </>
      ) : (
        <IconPicker
          currentIcon={(current.icon as string) ?? ''}
          hasImage={!!current.iconUrl}
          onPick={(icon) => set({ icon: icon || null, iconUrl: null })}
          onImage={(url) => set({ iconUrl: url, icon: null })}
        />
      )}
    </div>
  );
}

// ---- the tabbed icon picker ----

type IconTab = 'recommended' | 'letters' | 'upload';

function IconPicker({ currentIcon, hasImage, onPick, onImage }: {
  currentIcon: string;
  hasImage: boolean;
  onPick: (icon: string) => void;
  onImage: (url: string) => void;
}) {
  const [tab, setTab] = useState<IconTab>('recommended');
  const [query, setQuery] = useState('');
  const [uploading, setUploading] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  // A live search overrides the tab — matches Milanote's picker.
  const searching = query.trim().length > 0;
  const results = searching ? searchIcons(query) : ICON_CATALOG;

  const upload = async (file: File | undefined | null) => {
    if (!file || !file.type.startsWith('image/')) return;
    setUploading(true);
    try {
      const { url } = await uploadFile(file);
      onImage(url);
    } catch {
      toast.error('Icon upload failed');
    } finally {
      setUploading(false);
    }
  };

  return (
    <div className="icon-picker">
      <div className="ip-side">
        <button className={`ip-tab${tab === 'recommended' && !searching ? ' on' : ''}`} onClick={() => { setTab('recommended'); setQuery(''); }}>
          Recommended
        </button>
        <button className={`ip-tab${tab === 'letters' && !searching ? ' on' : ''}`} onClick={() => { setTab('letters'); setQuery(''); }}>
          Letters &amp; numbers
        </button>
        <button className={`ip-tab${tab === 'upload' && !searching ? ' on' : ''}`} onClick={() => { setTab('upload'); setQuery(''); }}>
          Upload an image
        </button>
        <div className="ip-search">
          <input
            dir="auto"
            placeholder="Search icons…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <SearchIcon size={13} />
        </div>
        {(currentIcon || hasImage) && (
          <button className="ip-clear" onClick={() => onPick('')}>Remove icon</button>
        )}
      </div>

      <div className="ip-grid-wrap">
        {searching || tab === 'recommended' ? (
          results.length ? (
            <div className="ip-grid">
              {results.map((entry) => (
                <button
                  key={entry.e}
                  className={`ip-cell${currentIcon === entry.e ? ' on' : ''}`}
                  title={entry.k.split(' ')[0]}
                  onClick={() => onPick(entry.e)}
                >
                  {entry.e}
                </button>
              ))}
            </div>
          ) : (
            <div className="ip-empty">No icons match “{query}”</div>
          )
        ) : tab === 'letters' ? (
          <div className="ip-grid">
            {LETTER_ICONS.map((ch) => (
              <button
                key={ch}
                className={`ip-cell ip-letter${currentIcon === ch ? ' on' : ''}`}
                onClick={() => onPick(ch)}
              >
                {ch}
              </button>
            ))}
          </div>
        ) : (
          <div className="ip-upload">
            <p>Use any image as this board's icon — it's cropped to the tile.</p>
            <button className="btn-primary" disabled={uploading} onClick={() => fileRef.current?.click()}>
              {uploading ? 'Uploading…' : 'Choose an image'}
            </button>
            <input
              ref={fileRef}
              type="file"
              accept="image/*"
              hidden
              onChange={(e) => void upload(e.target.files?.[0])}
            />
          </div>
        )}
      </div>
    </div>
  );
}
