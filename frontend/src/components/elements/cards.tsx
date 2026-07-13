// Card renderers: board tiles with live content stats, links with rich
// embeds (YouTube/Vimeo/Spotify/SoundCloud/Google Maps), images, files
// (with inline audio/video players for media uploads), color swatches,
// sketches, and comment threads.
import { useEffect, useRef, useState } from 'react';
import type { QComment, QElement } from '../../api/types';
import { api } from '../../api/client';
import { currentSub } from '../../auth/keycloak';
import { dirAttr, elementDir } from '../../lib/direction';
import { iconByName, isLetterIcon } from '../../lib/iconCatalog';
import { updateOp, useBoard } from '../../store/boardStore';
import type { ElementViewProps } from './ElementView';
import { AliasArrow, AudioIcon, BoardGlyph, CommentIcon, FileIcon, SyncIcon, VideoIcon } from '../Icons';

// ---- BOARD / ALIAS: gradient tile + title + content stats (§3.2, §4.16) ----

const tileGradients = [
  'linear-gradient(135deg,#6e6cf0,#4a48c4)', 'linear-gradient(135deg,#ff8a65,#e8590c)',
  'linear-gradient(135deg,#4dd0a6,#0ca678)', 'linear-gradient(135deg,#5fb0f5,#1c7ed6)',
  'linear-gradient(135deg,#f78fb3,#e64980)', 'linear-gradient(135deg,#9775fa,#7048e8)',
  'linear-gradient(135deg,#ffc94d,#f08c00)', 'linear-gradient(135deg,#63e6be,#0c8599)',
];
function tileFor(id: string) {
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return tileGradients[h % tileGradients.length];
}

const statLabels: Array<[string, string]> = [
  ['BOARD', 'board'], ['CARD', 'card'], ['IMAGE', 'image'], ['FILE', 'file'],
  ['LINK', 'link'], ['TASK_LIST', 'list'], ['DOCUMENT', 'doc'], ['TABLE', 'table'],
];

// statLineFor renders "2 boards, 17 cards, 6 files" from a type-count map.
export function statLineFor(stats: Record<string, number> | undefined): string {
  if (!stats) return '';
  return statLabels
    .filter(([t]) => (stats[t] ?? 0) > 0)
    .slice(0, 3)
    .map(([t, label]) => `${stats[t]} ${label}${stats[t] === 1 ? '' : 's'}`)
    .join(', ');
}

export function BoardCard({ element, navigate, inColumn }: ElementViewProps) {
  const { elements, boardStats, commitTransaction } = useBoard();
  const isAlias = element.type === 'ALIAS';
  const target = isAlias ? elements[element.content?.targetBoardId] : element;
  const title = (isAlias ? element.content?.title || target?.content?.title : element.content?.title) ?? 'Untitled board';
  const [editTitle, setEditTitle] = useState<string | null>(null);
  const statsId = isAlias ? element.content?.targetBoardId : element.id;
  const stats = boardStats[statsId];
  const statLine = statLineFor(stats);

  // Customization (Color / Icon): aliases inherit the target board's look.
  const styleSource = (isAlias ? target?.content : element.content) ?? {};
  const tileBg = (styleSource.color as string) || tileFor(statsId ?? element.id);
  const tileIcon = (styleSource.icon as string) || '';
  const tileImg = (styleSource.iconUrl as string) || '';
  const LucideGlyph = tileIcon ? iconByName(tileIcon) : undefined;
  const tileGlyph = tileImg
    ? <img className="tile-img" src={tileImg} alt="" draggable={false} />
    : LucideGlyph
      ? <LucideGlyph className="tile-glyph" strokeWidth={2.1} />
      : tileIcon
        ? <span className={`tile-icon${isLetterIcon(tileIcon) ? ' tile-letter' : ''}`}>{tileIcon}</span>
        : null;

  const open = () => {
    const id = isAlias ? element.content?.targetBoardId : element.id;
    if (id) void navigate(id);
  };

  const commitTitle = () => {
    if (editTitle && editTitle.trim() && editTitle !== title) {
      void commitTransaction([updateOp(element, { content: { title: editTitle.trim() } })]);
    }
    setEditTitle(null);
  };

  const titleNode = editTitle === null ? (
    <div className="board-title" dir={dirAttr(elementDir(element))} onDoubleClick={(e) => { e.stopPropagation(); if (!isAlias) setEditTitle(title); }}>{title}</div>
  ) : (
    <input
      className="board-title-input"
      dir={dirAttr(elementDir(element))}
      autoFocus
      value={editTitle}
      onChange={(e) => setEditTitle(e.target.value)}
      onPointerDown={(e) => e.stopPropagation()}
      onBlur={commitTitle}
      onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
    />
  );

  // Inside a column: compact horizontal row (small tile · title · stats),
  // matching Milanote's board rows in columns.
  if (inColumn) {
    return (
      <div className="board-row" onDoubleClick={(e) => { e.stopPropagation(); open(); }} title="Double-click to open">
        <div className="tile row-tile" style={{ background: tileBg }}>
          {tileGlyph ?? <BoardGlyph size={22} />}
          {isAlias && <div className="alias-badge" title="Shortcut to a board"><AliasArrow size={11} /></div>}
        </div>
        <div className="board-row-text">
          {titleNode}
          {statLine && <div className="board-stats">{statLine}</div>}
        </div>
      </div>
    );
  }

  return (
    <div className="board-card" onDoubleClick={(e) => { e.stopPropagation(); open(); }} title="Double-click to open">
      <div className="tile" style={{ background: tileBg }}>
        {tileGlyph ?? <BoardGlyph size={30} />}
        {isAlias && <div className="alias-badge" title="Shortcut to a board"><AliasArrow size={12} /></div>}
      </div>
      {titleNode}
      {statLine && <div className="board-stats">{statLine}</div>}
    </div>
  );
}

// ---- LINK (§4.4–4.7): preview card or live embed ----

export function LinkCard({ element }: { element: QElement }) {
  const c = element.content ?? {};
  const host = (() => { try { return new URL(c.url).host; } catch { return c.url; } })();
  const embed = embedSrc(c.url, c.embedType);

  if (embed) {
    const tall = c.embedType === 'spotify' || c.embedType === 'soundcloud' || c.embedType === 'audio';
    if (c.embedType === 'googlemaps') {
      return (
        <div className="link-card" style={{ width: element.location.width || 300 }}>
          <iframe className="map-embed" src={embed} title={c.title ?? 'map'} loading="lazy" referrerPolicy="no-referrer-when-downgrade" />
          <div className="link-body"><div className="link-title">{c.title}</div></div>
        </div>
      );
    }
    return (
      <div className="link-card" style={{ width: element.location.width || 320 }}>
        <iframe
          className={`link-embed${tall ? ' embed-tall' : ''}`}
          src={embed}
          title={c.title ?? 'embed'}
          allow="autoplay; encrypted-media; picture-in-picture"
          allowFullScreen
          loading="lazy"
        />
        <div className="link-body"><div className="link-host">{host}</div></div>
      </div>
    );
  }

  return (
    <div className="link-card" onDoubleClick={() => window.open(c.url, '_blank', 'noopener')} title="Double-click to open">
      {c.showPreview !== false && c.thumbnailUrl && <img className="link-thumb" src={c.thumbnailUrl} alt="" />}
      <div className="link-body">
        <div className="link-title">{c.title || c.url}</div>
        {c.showDescription !== false && c.description && <div className="link-desc">{c.description}</div>}
        <div className="link-host">{host}</div>
      </div>
    </div>
  );
}

function embedSrc(url: string, kind?: string): string | null {
  if (!url) return null;
  try {
    const u = new URL(/^https?:\/\//.test(url) ? url : `https://x.invalid/`);
    switch (kind) {
      case 'youtube': {
        const id = u.hostname === 'youtu.be' ? u.pathname.slice(1) : u.searchParams.get('v');
        return id ? `https://www.youtube-nocookie.com/embed/${id}` : null;
      }
      case 'vimeo': {
        const id = u.pathname.split('/').filter(Boolean)[0];
        return id ? `https://player.vimeo.com/video/${id}` : null;
      }
      case 'spotify':
        // open.spotify.com/track/x → open.spotify.com/embed/track/x
        return u.hostname.includes('spotify.com') && !u.pathname.startsWith('/embed')
          ? `https://open.spotify.com${'/embed' + u.pathname}`
          : url;
      case 'soundcloud':
        return `https://w.soundcloud.com/player/?url=${encodeURIComponent(url)}&visual=false&show_comments=false`;
      case 'googlemaps': {
        // Accept a maps URL or a plain address/place string.
        if (/^https?:\/\//.test(url)) {
          const place = decodeURIComponent(u.pathname.split('/place/')[1]?.split('/')[0] ?? '').replace(/\+/g, ' ');
          const q = place || u.searchParams.get('q') || '';
          return `https://maps.google.com/maps?q=${encodeURIComponent(q || url)}&output=embed`;
        }
        return `https://maps.google.com/maps?q=${encodeURIComponent(url)}&output=embed`;
      }
      case 'audio':
        return null; // unrecognized audio source: fall back to a link card
    }
  } catch { /* not a URL */ }
  if (kind === 'googlemaps') return `https://maps.google.com/maps?q=${encodeURIComponent(url)}&output=embed`;
  return null;
}

// ---- IMAGE (§4.3) ----

export function ImageCard({ element }: { element: QElement }) {
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [caption, setCaption] = useState<string | null>(null);
  const c = element.content ?? {};
  return (
    <div className="image-card">
      <img src={c.url} alt={c.caption ?? ''} draggable={false} />
      <input
        className="image-caption"
        dir={dirAttr(elementDir(element))}
        placeholder="Add a caption"
        value={caption ?? c.caption ?? ''}
        onChange={(e) => setCaption(e.target.value)}
        onPointerDown={(e) => e.stopPropagation()}
        onBlur={() => {
          if (caption !== null && caption !== c.caption) {
            void commitTransaction([updateOp(element, { content: { caption } })]);
          }
          setCaption(null);
        }}
      />
    </div>
  );
}

// ---- FILE (§4.8) — with inline players for uploaded audio/video ----

export function FileCard({ element }: { element: QElement }) {
  const c = element.content ?? {};
  const mime: string = c.mimeType ?? '';

  if (mime.startsWith('audio/')) {
    return (
      <div className="audio-card">
        <div className="file-name" style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
          <AudioIcon size={15} /> {c.filename}
        </div>
        <audio controls src={c.url} preload="metadata" onPointerDown={(e) => e.stopPropagation()} />
      </div>
    );
  }
  if (mime.startsWith('video/')) {
    return (
      <div className="video-card">
        <video controls src={c.url} preload="metadata" onPointerDown={(e) => e.stopPropagation()} />
        <div className="link-body">
          <div className="link-host" style={{ marginTop: 0 }}><VideoIcon size={13} /> {c.filename}</div>
        </div>
      </div>
    );
  }
  return (
    <div className="file-card" onDoubleClick={() => window.open(c.url, '_blank', 'noopener')} title="Double-click to download">
      <div className="file-badge"><FileIcon size={19} /></div>
      <div style={{ minWidth: 0 }}>
        <div className="file-name">{c.filename}</div>
        <div className="file-size">{formatBytes(c.size ?? 0)}</div>
      </div>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(2)} GB`;
}

// ---- COLOR_SWATCH (§4.14) ----

export function SwatchCard({ element }: { element: QElement }) {
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const inputRef = useRef<HTMLInputElement>(null);
  const c = element.content ?? {};
  const hex: string = c.hex ?? '#5e5ce6';
  const format: string = c.displayFormat ?? 'HEX';

  const display = () => {
    const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
    if (format === 'RGB') return `rgb(${r}, ${g}, ${b})`;
    if (format === 'HSL') {
      const [h, s, l] = rgbToHsl(r, g, b);
      return `hsl(${h}, ${s}%, ${l}%)`;
    }
    return hex.toUpperCase();
  };

  const cycleFormat = () => {
    const next = { HEX: 'RGB', RGB: 'HSL', HSL: 'HEX' }[format] ?? 'HEX';
    void commitTransaction([updateOp(element, { content: { displayFormat: next } })]);
  };

  return (
    <div className="swatch-card">
      <div className="swatch-color" style={{ background: hex }} onDoubleClick={() => inputRef.current?.click()} title="Double-click to pick a color" />
      <input
        ref={inputRef}
        type="color"
        value={hex}
        style={{ position: 'absolute', width: 0, height: 0, opacity: 0 }}
        onChange={(e) => void commitTransaction([updateOp(element, { content: { hex: e.target.value } })])}
      />
      <div className="swatch-value">
        <span>{display()}</span>
        <button onPointerDown={(e) => e.stopPropagation()} onClick={cycleFormat} title="Cycle HEX / RGB / HSL"><SyncIcon size={13} /></button>
      </div>
    </div>
  );
}

function rgbToHsl(r: number, g: number, b: number): [number, number, number] {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, Math.round(l * 100)];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  let h = 0;
  if (max === r) h = ((g - b) / d + (g < b ? 6 : 0)) / 6;
  else if (max === g) h = ((b - r) / d + 2) / 6;
  else h = ((r - g) / d + 4) / 6;
  return [Math.round(h * 360), Math.round(s * 100), Math.round(l * 100)];
}

// ---- SKETCH (§4.13) ----

interface Stroke { points: number[][]; color: string; width: number }

export function SketchCard({ element }: { element: QElement }) {
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [drawing, setDrawing] = useState<Stroke | null>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  const strokes: Stroke[] = element.content?.strokes ?? [];
  const w = element.content?.canvasW || element.location.width || 260;
  const h = element.content?.canvasH || 180;

  const localPoint = (e: React.PointerEvent) => {
    const rect = svgRef.current!.getBoundingClientRect();
    return [
      ((e.clientX - rect.left) / rect.width) * w,
      ((e.clientY - rect.top) / rect.height) * h,
    ];
  };

  return (
    <div className="sketch-card">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${w} ${h}`}
        style={{ width: '100%', height: 'auto', cursor: 'crosshair', touchAction: 'none' }}
        onPointerDown={(e) => {
          e.stopPropagation();
          svgRef.current?.setPointerCapture(e.pointerId);
          setDrawing({ points: [localPoint(e)], color: '#1d1d1f', width: 2.5 });
        }}
        onPointerMove={(e) => {
          if (drawing) setDrawing({ ...drawing, points: [...drawing.points, localPoint(e)] });
        }}
        onPointerUp={() => {
          if (drawing && drawing.points.length > 1) {
            void commitTransaction([updateOp(element, { content: { strokes: [...strokes, drawing] } })]);
          }
          setDrawing(null);
        }}
      >
        {[...strokes, ...(drawing ? [drawing] : [])].map((s, i) => (
          <polyline
            key={i}
            points={s.points.map((p) => p.join(',')).join(' ')}
            fill="none"
            stroke={s.color}
            strokeWidth={s.width}
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        ))}
      </svg>
    </div>
  );
}

// ---- COMMENT_THREAD (§4.17) ----

export function CommentCard({ element }: { element: QElement }) {
  const [comments, setComments] = useState<QComment[]>([]);
  const [body, setBody] = useState('');

  useEffect(() => {
    api.comments(element.id).then(setComments).catch(() => setComments([]));
  }, [element.id]);

  const post = async () => {
    const text = body.trim();
    if (!text) return;
    const created = await api.addComment(element.id, text);
    setComments((cs) => [...cs, created]);
    setBody('');
  };

  const dir = dirAttr(elementDir(element));

  return (
    <div className="comment-card">
      <div className="thread-title"><CommentIcon size={13} /> COMMENTS</div>
      {comments.map((c) => (
        <div key={c.id} className="comment-msg" dir={dir}>
          <div className="author">{c.authorId === currentSub() ? 'You' : c.authorId.slice(0, 8)}</div>
          {c.body}
          <ReactionBar comment={c} onUpdate={(u) => setComments((cs) => cs.map((x) => (x.id === u.id ? u : x)))} />
        </div>
      ))}
      <input
        className="comment-input"
        dir={dir}
        placeholder="Reply… (Enter to send)"
        value={body}
        onChange={(e) => setBody(e.target.value)}
        onPointerDown={(e) => e.stopPropagation()}
        onKeyDown={(e) => { if (e.key === 'Enter') void post(); }}
      />
    </div>
  );
}

function ReactionBar({ comment, onUpdate }: { comment: QComment; onUpdate: (c: QComment) => void }) {
  const emojis = ['👍', '❤️', '🎉'];
  return (
    <div style={{ display: 'flex', gap: 4, marginTop: 5 }}>
      {emojis.map((e) => {
        const subs = comment.reactions?.[e] ?? [];
        const mine = subs.includes(currentSub());
        return (
          <button
            key={e}
            className={`reaction-chip${mine ? ' on' : ''}`}
            onPointerDown={(ev) => ev.stopPropagation()}
            onClick={async () => onUpdate(await api.react(comment.id, e))}
          >
            {e} {subs.length > 0 && subs.length}
          </button>
        );
      })}
    </div>
  );
}
