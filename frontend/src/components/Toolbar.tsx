// The left tool rail — Milanote's exact tool inventory: Note, Link, To-do,
// Line, Board, Column, Comment, Table, a ••• flyout (Sketch, Color,
// Document, Audio, Map, Video, Heading), Add image, Upload, Draw, and the
// Trash drop-target pinned at the bottom.
import { useEffect, useRef, useState } from 'react';
import gsap from 'gsap';
import { api, uploadFile } from '../api/client';
import { createOp, useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { prompt } from './ui/Prompt';
import {
  AudioIcon, BoardIcon, ColorIcon, ColumnIcon, CommentIcon, DocumentIcon,
  DrawIcon, HeadingIcon, ImageIcon, LineIcon, LinkIcon, MapIcon, MoreIcon,
  NoteIcon, SketchIcon, TableIcon, TodoIcon, TrashIcon, UploadIcon, VideoIcon,
} from './Icons';

export function Toolbar() {
  const { boardId, commitTransaction } = useBoard();
  const { lineMode, setLineMode, drawMode, setDrawMode } = useView();
  const fileInput = useRef<HTMLInputElement>(null);
  const anyFileInput = useRef<HTMLInputElement>(null);
  const [moreOpen, setMoreOpen] = useState(false);
  const [moreTop, setMoreTop] = useState(300);
  const flyoutRef = useRef<HTMLDivElement>(null);
  const moreBtnRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (moreOpen && flyoutRef.current) {
      gsap.fromTo(flyoutRef.current, { opacity: 0, x: -8, scale: 0.97 }, { opacity: 1, x: 0, scale: 1, duration: 0.18, ease: 'power3.out' });
    }
  }, [moreOpen]);

  // Click-away closes the flyout.
  useEffect(() => {
    if (!moreOpen) return;
    const onDown = (e: PointerEvent) => {
      if (!flyoutRef.current?.contains(e.target as Node) && !moreBtnRef.current?.contains(e.target as Node)) {
        setMoreOpen(false);
      }
    };
    window.addEventListener('pointerdown', onDown);
    return () => window.removeEventListener('pointerdown', onDown);
  }, [moreOpen]);

  const dropPoint = () => {
    const v = useView.getState();
    const viewport = document.querySelector('.canvas-viewport') as HTMLElement | null;
    const w = viewport?.clientWidth ?? window.innerWidth;
    const h = viewport?.clientHeight ?? window.innerHeight;
    return {
      x: (w / 2 - v.panX) / v.scale - 130 + (Math.random() - 0.5) * 60,
      y: (h / 2 - v.panY) / v.scale - 80 + (Math.random() - 0.5) * 60,
    };
  };

  const add = (type: string, content: Record<string, any>, width?: number, editAfter = false) => {
    const op = createOp(type, boardId, { position: dropPoint(), width, content });
    void commitTransaction([op]);
    if (editAfter) useView.getState().setEditing(op.elementId);
    setMoreOpen(false);
  };

  const addLink = async () => {
    const url = await prompt({ title: 'Add a link', placeholder: 'https://…', confirmLabel: 'Add link' });
    if (!url || !/^https?:\/\//.test(url.trim())) return;
    const meta = await api.resolveLink(url.trim()).catch(() => null);
    add('LINK', meta
      ? { url: meta.url, title: meta.title, description: meta.description, thumbnailUrl: meta.thumbnailUrl, embedType: meta.embedType, showPreview: true, showDescription: true }
      : { url: url.trim(), title: url.trim(), showPreview: false, showDescription: false }, 260);
  };

  const addAudio = async () => {
    const url = await prompt({ title: 'Add audio', placeholder: 'Spotify, SoundCloud, YouTube…', confirmLabel: 'Add' });
    if (!url || !/^https?:\/\//.test(url.trim())) return;
    const meta = await api.resolveLink(url.trim()).catch(() => null);
    add('LINK', {
      url: url.trim(),
      title: meta?.title || url.trim(),
      thumbnailUrl: meta?.thumbnailUrl || '',
      embedType: meta?.embedType || 'audio',
      showPreview: true, showDescription: false,
    }, 300);
  };

  const addMap = async () => {
    const q = await prompt({ title: 'Add a map', placeholder: 'Google Maps link, or a place / address', confirmLabel: 'Add map' });
    if (!q?.trim()) return;
    add('LINK', { url: q.trim(), title: q.trim(), embedType: 'googlemaps', showPreview: true, showDescription: false }, 300);
  };

  const addVideo = async () => {
    const url = await prompt({ title: 'Add a video', placeholder: 'YouTube or Vimeo link', confirmLabel: 'Add video' });
    if (!url || !/^https?:\/\//.test(url.trim())) return;
    const meta = await api.resolveLink(url.trim()).catch(() => null);
    add('LINK', {
      url: url.trim(), title: meta?.title || url.trim(),
      thumbnailUrl: meta?.thumbnailUrl || '',
      embedType: meta?.embedType || 'youtube',
      showPreview: true, showDescription: false,
    }, 320);
  };

  const onFiles = async (files: FileList | null, imagesOnly: boolean) => {
    if (!files) return;
    const pt = dropPoint();
    let offset = 0;
    for (const file of Array.from(files)) {
      const { url, attachmentId } = await uploadFile(file);
      const isImage = file.type.startsWith('image/');
      const op = createOp(isImage ? 'IMAGE' : 'FILE', boardId, {
        position: { x: pt.x + offset, y: pt.y + offset },
        width: isImage ? 280 : 0,
        content: isImage
          ? { url, attachmentId, caption: '' }
          : { url, attachmentId, filename: file.name, mimeType: file.type, size: file.size },
      });
      await commitTransaction([op]);
      offset += 28;
    }
    if (fileInput.current) fileInput.current.value = '';
    if (anyFileInput.current) anyFileInput.current.value = '';
  };

  const emptyDoc = (text = '') =>
    text
      ? { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text }] }] }
      : null;

  const mainTools = [
    { icon: <NoteIcon />, label: 'Note', onClick: () => add('CARD', { doc: null, textPreview: '' }, 300, true) },
    { icon: <LinkIcon />, label: 'Link', onClick: () => void addLink() },
    { icon: <TodoIcon />, label: 'To-do', onClick: () => add('TASK_LIST', { title: '' }) },
    { icon: <LineIcon />, label: 'Line', onClick: () => setLineMode(!lineMode), active: lineMode },
    { icon: <BoardIcon />, label: 'Board', onClick: () => add('BOARD', { title: 'New board' }) },
    { icon: <ColumnIcon />, label: 'Column', onClick: () => add('COLUMN', { title: '', collapsed: false }) },
    { icon: <CommentIcon />, label: 'Comment', onClick: () => add('COMMENT_THREAD', {}) },
    { icon: <TableIcon />, label: 'Table', onClick: () => add('TABLE', { cells: [['', '', ''], ['', '', ''], ['', '', '']] }, 300) },
  ];

  const moreTools = [
    { icon: <SketchIcon size={17} />, bg: 'linear-gradient(135deg,#8e8ef5,#5e5ce6)', label: 'Sketch', onClick: () => add('SKETCH', { strokes: [], canvasW: 260, canvasH: 180 }, 260) },
    { icon: <ColorIcon size={17} />, bg: 'linear-gradient(135deg,#ff6482,#e64980)', label: 'Color', onClick: () => add('COLOR_SWATCH', { hex: nextSwatch(), displayFormat: 'HEX' }) },
    { icon: <DocumentIcon size={17} />, bg: 'linear-gradient(135deg,#34c1e0,#2193b0)', label: 'Document', onClick: () => add('DOCUMENT', { title: 'Untitled document', doc: null, textPreview: '' }) },
    { icon: <AudioIcon size={17} />, bg: 'linear-gradient(135deg,#4cd471,#2eb85c)', label: 'Audio', onClick: () => void addAudio() },
    { icon: <MapIcon size={17} />, bg: 'linear-gradient(135deg,#ffa94d,#f76707)', label: 'Map', onClick: () => addMap() },
    { icon: <VideoIcon size={17} />, bg: 'linear-gradient(135deg,#748ffc,#4263eb)', label: 'Video', onClick: () => void addVideo() },
    { icon: <HeadingIcon size={17} />, bg: 'linear-gradient(135deg,#495057,#212529)', label: 'Heading', onClick: () => add('CARD', { doc: emptyDoc(), textPreview: '', variant: 'heading' }, 340, true) },
  ];

  return (
    <div className="rail" onPointerDown={(e) => e.stopPropagation()}>
      {mainTools.map((t) => (
        <button key={t.label} className={`rail-btn${t.active ? ' active' : ''}`} onClick={t.onClick}>
          {t.icon}
          <span>{t.label}</span>
        </button>
      ))}

      <button
        ref={moreBtnRef}
        className={`rail-btn${moreOpen ? ' active' : ''}`}
        onClick={(e) => {
          setMoreTop(Math.min((e.currentTarget as HTMLElement).offsetTop - 40, window.innerHeight - 260));
          setMoreOpen(!moreOpen);
        }}
      >
        <MoreIcon />
        <span>More</span>
      </button>

      <button className="rail-btn" onClick={() => fileInput.current?.click()}>
        <ImageIcon />
        <span>Add image</span>
      </button>
      <button className="rail-btn" onClick={() => anyFileInput.current?.click()}>
        <UploadIcon />
        <span>Upload</span>
      </button>
      <button className={`rail-btn${drawMode ? ' active' : ''}`} onClick={() => setDrawMode(!drawMode)}>
        <DrawIcon />
        <span>Draw</span>
      </button>

      <div className="rail-spacer" />

      {/* Drop a dragged card here to delete it (ElementShell checks this). */}
      <div className="rail-btn danger-target" data-trash-drop="1" title="Drag cards here to delete">
        <TrashIcon />
        <span>Trash</span>
      </div>

      {moreOpen && (
        <div ref={flyoutRef} className="flyout" style={{ top: moreTop }}>
          {moreTools.map((t) => (
            <button key={t.label} className="flyout-btn" onClick={t.onClick}>
              <div className="fly-ico" style={{ background: t.bg }}>{t.icon}</div>
              {t.label}
            </button>
          ))}
        </div>
      )}

      <input ref={fileInput} type="file" accept="image/*" multiple hidden onChange={(e) => void onFiles(e.target.files, true)} />
      <input ref={anyFileInput} type="file" multiple hidden onChange={(e) => void onFiles(e.target.files, false)} />
    </div>
  );
}

const swatchPalette = ['#e8590c', '#5e5ce6', '#2eb85c', '#1c7ed6', '#f2cc0d', '#212529', '#e64980', '#0ca678'];
let swatchIndex = 0;
function nextSwatch() {
  return swatchPalette[swatchIndex++ % swatchPalette.length];
}
