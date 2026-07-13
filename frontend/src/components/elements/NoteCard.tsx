// Rich-text note (§4.1) on Tiptap. Deliberate formatting with a dark
// floating format bar while editing — bold/italic/underline/strike,
// headings, both list kinds, quote/code, inline links, text color,
// highlight, note background color, and text direction. The 'heading'
// variant renders as large chromeless title text on the canvas. Edits
// commit one transaction on blur (Tiptap JSON + plain-text preview).
import { useCallback, useEffect, useState } from 'react';
import { EditorContent, useEditor } from '@tiptap/react';
import StarterKit from '@tiptap/starter-kit';
import Placeholder from '@tiptap/extension-placeholder';
import Link from '@tiptap/extension-link';
import TextStyle from '@tiptap/extension-text-style';
import { Color } from '@tiptap/extension-color';
import Highlight from '@tiptap/extension-highlight';
import Underline from '@tiptap/extension-underline';
import type { QElement } from '../../api/types';
import { t } from '../../i18n';
import { cycleDir, elementDir, smartDigitsTextInput, type TextDirection } from '../../lib/direction';
import { sendEditing } from '../../realtime/socket';
import { updateOp, useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import { prompt } from '../ui/Prompt';
import {
  BoldIcon, CodeIcon, DirAutoIcon, DirLtrIcon, DirRtlIcon, H1Icon, H2Icon,
  ItalicIcon, LinkIcon, ListIcon, ListOrderedIcon, NoteColorIcon, QuoteIcon,
  StrikeIcon, SyncIcon, TextColorIcon, UnderlineIcon,
} from '../Icons';

// Ink palette for text color / highlight, and pastel card backgrounds —
// Milanote's restrained sets, tuned for both themes.
const TEXT_COLORS = ['', '#e8590c', '#e64980', '#5e5ce6', '#1c7ed6', '#0ca678', '#f2a20d', '#868e96'];
const HIGHLIGHTS = ['', '#fff3bf', '#ffe3e3', '#d3f9d8', '#d0ebff', '#e5dbff'];
const NOTE_COLORS = ['', '#fff9db', '#ffe8e8', '#e6fcf0', '#e7f5ff', '#f3f0ff', '#fff4e6', '#f8f0fc', '#2b3035'];

// isLightColor: perceived luminance of a #rrggbb color.
function isLightColor(hex: string): boolean {
  const m = /^#([0-9a-f]{6})$/i.exec(hex);
  if (!m) return true;
  const n = parseInt(m[1], 16);
  const r = (n >> 16) & 255, g = (n >> 8) & 255, b = n & 255;
  return 0.299 * r + 0.587 * g + 0.114 * b > 140;
}

interface Props {
  element: QElement;       // the CARD/DOCUMENT whose content we edit
  cloneShellId?: string;   // set when rendered inside a CLONE shell (§4.15)
}

export function NoteCard({ element, cloneShellId }: Props) {
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const editingId = useView((s) => s.editingId);
  const shellId = cloneShellId ?? element.id;
  const autoEdit = editingId === shellId;
  const isHeading = element.content?.variant === 'heading';
  const [editing, setEditing] = useState(false);

  const editor = useEditor({
    extensions: [
      StarterKit,
      Placeholder.configure({ placeholder: isHeading ? 'Heading' : 'Write something…' }),
      Link.configure({ openOnClick: false, autolink: true }),
      TextStyle,
      Color,
      Highlight.configure({ multicolor: true }),
      Underline,
    ],
    content: element.content?.doc ?? '',
    editable: false,
    // Typing digits inside Arabic text produces Arabic-Indic numerals.
    editorProps: { handleTextInput: smartDigitsTextInput },
    onBlur: ({ editor }) => {
      useView.getState().setEditing(null);
      editor.setEditable(false);
      setEditing(false);
      sendEditing(element.id, false);
      const doc = editor.getJSON();
      const textPreview = editor.getText().slice(0, 500);
      const prev = JSON.stringify(element.content?.doc ?? null);
      if (JSON.stringify(doc) !== prev) {
        void commitTransaction([updateOp(element, { content: { doc, textPreview } })]);
      }
    },
  });

  // Remote updates: refresh the (non-editing) editor when content changes.
  useEffect(() => {
    if (!editor || editor.isEditable) return;
    const incoming = element.content?.doc;
    if (incoming && JSON.stringify(editor.getJSON()) !== JSON.stringify(incoming)) {
      editor.commands.setContent(incoming, false);
    }
  }, [editor, element.content?.doc]);

  const startEditing = useCallback(() => {
    if (!editor || editor.isEditable) return;
    editor.setEditable(true);
    editor.commands.focus('end');
    setEditing(true);
    sendEditing(element.id, true);
  }, [editor, element.id]);

  useEffect(() => {
    if (autoEdit && editor) {
      const t = setTimeout(startEditing, 30);
      return () => clearTimeout(t);
    }
  }, [autoEdit, editor, startEditing]);

  const bg = element.content?.backgroundColor;
  // A colored card pins its ink to match the paper, whatever the app theme —
  // pastel backgrounds stay readable in dark mode and vice versa.
  const bgInk = bg ? (isLightColor(bg) ? '#1d1d1f' : '#f5f5f7') : undefined;

  // Text direction: 'auto' resolves per paragraph from its first strong
  // letter (Arabic → RTL) via unicode-bidi: plaintext; a manual override
  // forces the whole card until switched back (format bar or context menu).
  const dir = elementDir(element);
  const setDir = (next: TextDirection) => {
    void commitTransaction([updateOp(element, {
      content: { textDirection: next === 'auto' ? null : next },
    })]);
  };

  return (
    <div
      className={`note-card${isHeading ? ' heading-note' : ''}${dir === 'auto' ? ' bidi-auto' : ''}`}
      dir={dir === 'auto' ? undefined : dir}
      style={bg ? ({ background: bg, '--ink': bgInk } as React.CSSProperties) : undefined}
      onDoubleClick={(e) => { e.stopPropagation(); startEditing(); }}
    >
      {editing && editor && !isHeading && (
        <FormatBar
          editor={editor}
          dir={dir}
          onCycleDir={() => setDir(cycleDir(dir))}
          noteColor={bg ?? ''}
          onNoteColor={(hex) => void commitTransaction([updateOp(element, { content: { backgroundColor: hex || null } })])}
        />
      )}
      <EditorContent editor={editor} />
      {cloneShellId && (
        <div className="clone-footer" title="This note is synced — edits update every copy">
          <SyncIcon size={11} /> synced note
        </div>
      )}
    </div>
  );
}

// FormatBar: the dark floating toolbar shown while a note is being edited.
// onMouseDown preventDefault keeps focus in the editor while clicking.
function FormatBar({ editor, dir, onCycleDir, noteColor, onNoteColor }: {
  editor: NonNullable<ReturnType<typeof useEditor>>;
  dir: TextDirection;
  onCycleDir: () => void;
  noteColor: string;
  onNoteColor: (hex: string) => void;
}) {
  const [palette, setPalette] = useState<'text' | 'highlight' | 'note' | null>(null);

  const btn = (
    key: string,
    icon: JSX.Element,
    action: () => void,
    isOn: boolean,
    title?: string,
  ) => (
    <button
      key={key}
      className={isOn ? 'on' : undefined}
      title={title}
      onMouseDown={(e) => e.preventDefault()}
      onPointerDown={(e) => e.stopPropagation()}
      onClick={action}
    >
      {icon}
    </button>
  );
  const c = () => editor.chain().focus();

  const setLink = async () => {
    const prev = editor.getAttributes('link').href as string | undefined;
    const url = await prompt({ title: 'Link text to…', placeholder: 'https://…', defaultValue: prev ?? '', confirmLabel: 'Link' });
    if (url === null) return;
    if (!url.trim()) { c().unsetLink().run(); return; }
    const href = /^https?:\/\//.test(url.trim()) ? url.trim() : `https://${url.trim()}`;
    c().extendMarkRange('link').setLink({ href }).run();
  };

  // One row of swatches for whichever palette is open.
  const swatches = palette && (
    <div className="fmt-palette" onMouseDown={(e) => e.preventDefault()} onPointerDown={(e) => e.stopPropagation()}>
      {(palette === 'text' ? TEXT_COLORS : palette === 'highlight' ? HIGHLIGHTS : NOTE_COLORS).map((hex) => (
        <button
          key={hex || 'none'}
          className={`fmt-swatch${hex === '' ? ' none' : ''}`}
          style={hex ? { background: hex } : undefined}
          title={hex === '' ? 'Default' : hex}
          onClick={() => {
            if (palette === 'text') hex ? c().setColor(hex).run() : c().unsetColor().run();
            else if (palette === 'highlight') hex ? c().setHighlight({ color: hex }).run() : c().unsetHighlight().run();
            else onNoteColor(hex);
            setPalette(null);
          }}
        />
      ))}
    </div>
  );

  return (
    <div className="fmt-bar-wrap">
      <div className="fmt-bar">
        {btn('b', <BoldIcon size={15} />, () => c().toggleBold().run(), editor.isActive('bold'), 'Bold')}
        {btn('i', <ItalicIcon size={15} />, () => c().toggleItalic().run(), editor.isActive('italic'), 'Italic')}
        {btn('u', <UnderlineIcon size={15} />, () => c().toggleUnderline().run(), editor.isActive('underline'), 'Underline')}
        {btn('s', <StrikeIcon size={15} />, () => c().toggleStrike().run(), editor.isActive('strike'), 'Strikethrough')}
        {btn('h1', <H1Icon size={15} />, () => c().toggleHeading({ level: 1 }).run(), editor.isActive('heading', { level: 1 }), 'Heading 1')}
        {btn('h2', <H2Icon size={15} />, () => c().toggleHeading({ level: 2 }).run(), editor.isActive('heading', { level: 2 }), 'Heading 2')}
        {btn('ul', <ListIcon size={15} />, () => c().toggleBulletList().run(), editor.isActive('bulletList'), 'Bullet list')}
        {btn('ol', <ListOrderedIcon size={15} />, () => c().toggleOrderedList().run(), editor.isActive('orderedList'), 'Numbered list')}
        {btn('q', <QuoteIcon size={15} />, () => c().toggleBlockquote().run(), editor.isActive('blockquote'), 'Quote')}
        {btn('code', <CodeIcon size={15} />, () => c().toggleCodeBlock().run(), editor.isActive('codeBlock'), 'Code block')}
        {btn('link', <LinkIcon size={15} />, () => void setLink(), editor.isActive('link'), 'Link')}
        {btn('tc', <TextColorIcon size={15} />, () => setPalette(palette === 'text' ? null : 'text'), palette === 'text' || !!editor.getAttributes('textStyle').color, 'Text color')}
        {btn('hl', <span className="fmt-hl-swatch" />, () => setPalette(palette === 'highlight' ? null : 'highlight'), palette === 'highlight' || editor.isActive('highlight'), 'Highlight')}
        {btn('bg', <NoteColorIcon size={15} />, () => setPalette(palette === 'note' ? null : 'note'), palette === 'note' || !!noteColor, 'Note color')}
        <button
          className={dir !== 'auto' ? 'on' : undefined}
          title={`${t('dir.cycleTip')} · ${t(dir === 'auto' ? 'dir.auto' : dir === 'rtl' ? 'dir.rtl' : 'dir.ltr')}`}
          onMouseDown={(e) => e.preventDefault()}
          onPointerDown={(e) => e.stopPropagation()}
          onClick={onCycleDir}
        >
          {dir === 'rtl' ? <DirRtlIcon size={15} /> : dir === 'ltr' ? <DirLtrIcon size={15} /> : <DirAutoIcon size={15} />}
        </button>
      </div>
      {swatches}
    </div>
  );
}
