// Card renderers: board tiles with live content stats, links with rich
// embeds (YouTube/Vimeo/Spotify/SoundCloud/Google Maps), images, files
// (with inline audio/video players for media uploads), color swatches,
// sketches, and comment threads.
import { useEffect, useMemo, useRef, useState } from 'react';
import type { QComment, QElement } from '../../api/types';
import { api } from '../../api/client';
import { currentSub } from '../../auth/keycloak';
import { useT } from '../../i18n';
import { activateElement } from '../../canvas/activate';
import { dirAttr, elementDir } from '../../lib/direction';
import { iconByName, isLetterIcon } from '../../lib/iconCatalog';
import { updateOp, useBoard } from '../../store/boardStore';
import { useUserNames } from '../../store/userNames';
import type { ElementViewProps } from './ElementView';
import { AliasArrow, AudioIcon, BoardGlyph, CommentIcon, FileIcon, SyncIcon, VideoIcon } from '../Icons';
import { AuthedImage } from './AuthedImage';

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
  // Selectors, not the store. A board tile subscribed wholesale re-rendered on
  // every collaborator cursor frame; the only thing it reads out of `elements`
  // is the ONE board an alias points at.
  const isAlias = element.type === 'ALIAS';
  const target = useBoard((s) => (isAlias ? s.elements[element.content?.targetBoardId] : element));
  const boardStats = useBoard((s) => s.boardStats);
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const t = useT();
  const title = (isAlias ? element.content?.title || target?.content?.title : element.content?.title) ?? t('board.untitled');
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
    ? <AuthedImage className="tile-img" src={tileImg} alt="" draggable={false} />
    : LucideGlyph
      ? <LucideGlyph className="tile-glyph" strokeWidth={2.1} />
      : tileIcon
        ? <span className={`tile-icon${isLetterIcon(tileIcon) ? ' tile-letter' : ''}`}>{tileIcon}</span>
        : null;

  // Through the shared verb rather than its own copy, so the double-click, the
  // keyboard's Enter and the second tap on a selected tile all do the same
  // thing — the drift between them is what made OPEN a mouse-only idea.
  const open = () => { activateElement(element, navigate); };

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
      <div className="board-row" onDoubleClick={(e) => { e.stopPropagation(); open(); }} title={t('a11y.openHint')}>
        <div className="tile row-tile" style={{ background: tileBg }}>
          {tileGlyph ?? <BoardGlyph size={22} />}
          {isAlias && <div className="alias-badge" title={t('a11y.shortcutBadge')}><AliasArrow size={11} /></div>}
        </div>
        <div className="board-row-text">
          {titleNode}
          {statLine && <div className="board-stats">{statLine}</div>}
        </div>
      </div>
    );
  }

  return (
    <div className="board-card" onDoubleClick={(e) => { e.stopPropagation(); open(); }} title={t('a11y.openHint')}>
      <div className="tile" style={{ background: tileBg }}>
        {tileGlyph ?? <BoardGlyph size={30} />}
        {isAlias && <div className="alias-badge" title={t('a11y.shortcutBadge')}><AliasArrow size={12} /></div>}
      </div>
      {titleNode}
      {statLine && <div className="board-stats">{statLine}</div>}
    </div>
  );
}

// ---- LINK (§4.4–4.7): preview card or live embed ----

export function LinkCard({ element }: { element: QElement }) {
  const t = useT();
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
    <div className="link-card" onDoubleClick={() => activateElement(element)} title={t('a11y.openHint')}>
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
  const t = useT();
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [caption, setCaption] = useState<string | null>(null);
  const c = element.content ?? {};
  return (
    <div className="image-card">
      {/* AX27. This was `alt={c.caption ?? ''}`, immediately followed by the
          caption <input> whose VALUE is the same string — so a captioned image
          announced its caption twice, once as the image and once as the field.
          And an uncaptioned image announced as `alt=""`, which is a positive
          assertion to assistive technology that the image carries nothing. On a
          filmmaker's reference board the images ARE the content: forty
          uncaptioned stills was the board being empty.
          With a caption the field speaks and the image stays quiet; without
          one the image says that it is an image nobody has described, which is
          true, useful, and the thing IN15's uncaptioned-image digest flag is
          counting from the other side. */}
      <AuthedImage src={c.url} alt={c.caption ? '' : t('image.undescribed')} draggable={false} />
      <input
        className="image-caption"
        dir={dirAttr(elementDir(element))}
        aria-label={t('image.caption')}
        placeholder={t('image.caption')}
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
  const t = useT();
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
    <div className="file-card" onDoubleClick={() => activateElement(element)} title={`${t('a11y.download')} — ${t('a11y.openHint')}`}>
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
  const t = useT();
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
      <div className="swatch-color" style={{ background: hex }} onDoubleClick={() => inputRef.current?.click()} title={`${t('a11y.pickColour')} — ${t('a11y.openHint')}`} />
      <input
        ref={inputRef}
        type="color"
        aria-label={t('a11y.pickColour')}
        value={hex}
        style={{ position: 'absolute', width: 0, height: 0, opacity: 0 }}
        onChange={(e) => void commitTransaction([updateOp(element, { content: { hex: e.target.value } })])}
      />
      <div className="swatch-value">
        <span>{display()}</span>
        <button onPointerDown={(e) => e.stopPropagation()} onClick={cycleFormat} title={t('a11y.cycleFormat')} aria-label={t('a11y.cycleFormat')}><SyncIcon size={13} /></button>
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
  const t = useT();
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
      {/* A bare <svg> with no role and no title announced as nothing at all,
          which is the same fact the digest found from the other side: a sketch
          carries no title, no textPreview, no filename and no url, so it was an
          element that exists, can be moved, and has no name. Naming it by its
          stroke count is the minimum that makes it referenceable. */}
      <svg
        ref={svgRef}
        role="img"
        aria-label={`${t('tool.sketch')} — ${strokes.length}`}
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
// Real author names (users/resolve cache), live updates over the socket,
// and @mentions: typing @ opens a collaborator picker; mentioned users get
// a notification.

export function CommentCard({ element }: { element: QElement }) {
  const t = useT();
  const [comments, setComments] = useState<QComment[]>([]);
  const [body, setBody] = useState('');
  const [mentionQuery, setMentionQuery] = useState<string | null>(null);
  const [mentions, setMentions] = useState<Record<string, string>>({}); // name → sub
  const users = useUserNames((s) => s.users);
  // Only the board's own ACL, not the element map: a comment thread that
  // subscribed to `elements` re-rendered its whole editor on every remote
  // cursor frame, losing nothing but costing everything.
  const boardAcl = useBoard((s) => s.elements[s.boardId]?.acl);

  useEffect(() => {
    api.comments(element.id).then(setComments).catch(() => setComments([]));
  }, [element.id]);

  // Live: peers' comments on this thread arrive over the socket.
  useEffect(() => {
    const onNew = (e: Event) => {
      const c = (e as CustomEvent).detail as QComment;
      if (c.threadId !== element.id) return;
      setComments((cs) => (cs.some((x) => x.id === c.id) ? cs : [...cs, c]));
    };
    window.addEventListener('qomra:comment', onNew);
    return () => window.removeEventListener('qomra:comment', onNew);
  }, [element.id]);

  // Resolve author names for everything on screen.
  useEffect(() => {
    const subs = comments.map((c) => c.authorId);
    if (subs.length) void useUserNames.getState().resolve(subs);
  }, [comments]);

  // Collaborators on this board = mention candidates.
  const collaborators = useMemo(
    () => [boardAcl?.ownerId, ...(boardAcl?.editors ?? [])].filter(Boolean) as string[],
    [boardAcl],
  );
  useEffect(() => {
    if (collaborators.length) void useUserNames.getState().resolve(collaborators);
  }, [collaborators]);

  const mentionMatches = mentionQuery === null ? [] : collaborators
    .map((sub) => ({ sub, name: users[sub]?.name ?? sub.slice(0, 8) }))
    .filter((u) => u.sub !== currentSub() && u.name.toLowerCase().includes(mentionQuery.toLowerCase()))
    .slice(0, 5);

  const onBodyChange = (v: string) => {
    setBody(v);
    // An @word being typed at the end opens the picker.
    const m = /@([^\s@]*)$/.exec(v);
    setMentionQuery(m ? m[1] : null);
  };

  const pickMention = (sub: string, name: string) => {
    setBody((v) => v.replace(/@([^\s@]*)$/, `@${name} `));
    setMentions((prev) => ({ ...prev, [name]: sub }));
    setMentionQuery(null);
  };

  const post = async () => {
    const text = body.trim();
    if (!text) return;
    // Only names still present in the text mention their users.
    const mentioned = Object.entries(mentions)
      .filter(([name]) => text.includes(`@${name}`))
      .map(([, sub]) => sub);
    const created = await api.addComment(element.id, text, mentioned);
    setComments((cs) => (cs.some((x) => x.id === created.id) ? cs : [...cs, created]));
    setBody('');
    setMentions({});
    setMentionQuery(null);
  };

  const dir = dirAttr(elementDir(element));

  return (
    <div className="comment-card">
      <div className="thread-title"><CommentIcon size={13} /> {t('comment.head')}</div>
      {comments.map((c) => (
        <div key={c.id} className="comment-msg" dir={dir}>
          <div className="author">{c.authorId === currentSub() ? 'You' : (users[c.authorId]?.name ?? c.authorId.slice(0, 8))}</div>
          {c.body}
          <ReactionBar comment={c} onUpdate={(u) => setComments((cs) => cs.map((x) => (x.id === u.id ? u : x)))} />
        </div>
      ))}
      <div style={{ position: 'relative' }}>
        {mentionQuery !== null && mentionMatches.length > 0 && (
          <div className="mention-pop" onPointerDown={(e) => e.stopPropagation()}>
            {mentionMatches.map((u) => (
              <button key={u.sub} onClick={() => pickMention(u.sub, u.name)}>@{u.name}</button>
            ))}
          </div>
        )}
        <input
          className="comment-input"
          dir={dir}
          aria-label={t('comment.reply')}
          placeholder={t('comment.reply')}
          value={body}
          onChange={(e) => onBodyChange(e.target.value)}
          onPointerDown={(e) => e.stopPropagation()}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && mentionQuery !== null && mentionMatches.length > 0) {
              e.preventDefault();
              pickMention(mentionMatches[0].sub, mentionMatches[0].name);
            } else if (e.key === 'Enter') {
              void post();
            } else if (e.key === 'Escape') {
              setMentionQuery(null);
            }
          }}
        />
      </div>
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
