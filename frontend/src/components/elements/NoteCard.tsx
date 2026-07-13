// Rich-text note (§4.1) on Tiptap. Minimal, deliberate formatting with a
// dark floating format bar while editing; the 'heading' variant renders as
// large chromeless title text on the canvas. Edits commit one transaction
// on blur (Tiptap JSON + plain-text preview for search).
import { useCallback, useEffect, useState } from 'react';
import { EditorContent, useEditor } from '@tiptap/react';
import StarterKit from '@tiptap/starter-kit';
import Placeholder from '@tiptap/extension-placeholder';
import type { QElement } from '../../api/types';
import { t } from '../../i18n';
import { cycleDir, elementDir, type TextDirection } from '../../lib/direction';
import { sendEditing } from '../../realtime/socket';
import { updateOp, useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import {
  BoldIcon, CodeIcon, DirAutoIcon, DirLtrIcon, DirRtlIcon, H1Icon, H2Icon,
  ItalicIcon, ListIcon, QuoteIcon, StrikeIcon, SyncIcon,
} from '../Icons';

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
    ],
    content: element.content?.doc ?? '',
    editable: false,
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
      style={bg ? { background: bg } : undefined}
      onDoubleClick={(e) => { e.stopPropagation(); startEditing(); }}
    >
      {editing && editor && !isHeading && <FormatBar editor={editor} dir={dir} onCycleDir={() => setDir(cycleDir(dir))} />}
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
function FormatBar({ editor, dir, onCycleDir }: {
  editor: NonNullable<ReturnType<typeof useEditor>>;
  dir: TextDirection;
  onCycleDir: () => void;
}) {
  const btn = (
    key: string,
    icon: JSX.Element,
    action: () => void,
    isOn: boolean,
  ) => (
    <button
      key={key}
      className={isOn ? 'on' : undefined}
      onMouseDown={(e) => e.preventDefault()}
      onPointerDown={(e) => e.stopPropagation()}
      onClick={action}
    >
      {icon}
    </button>
  );
  const c = () => editor.chain().focus();
  return (
    <div className="fmt-bar">
      {btn('b', <BoldIcon size={15} />, () => c().toggleBold().run(), editor.isActive('bold'))}
      {btn('i', <ItalicIcon size={15} />, () => c().toggleItalic().run(), editor.isActive('italic'))}
      {btn('s', <StrikeIcon size={15} />, () => c().toggleStrike().run(), editor.isActive('strike'))}
      {btn('h1', <H1Icon size={15} />, () => c().toggleHeading({ level: 1 }).run(), editor.isActive('heading', { level: 1 }))}
      {btn('h2', <H2Icon size={15} />, () => c().toggleHeading({ level: 2 }).run(), editor.isActive('heading', { level: 2 }))}
      {btn('ul', <ListIcon size={15} />, () => c().toggleBulletList().run(), editor.isActive('bulletList'))}
      {btn('q', <QuoteIcon size={15} />, () => c().toggleBlockquote().run(), editor.isActive('blockquote'))}
      {btn('code', <CodeIcon size={15} />, () => c().toggleCodeBlock().run(), editor.isActive('codeBlock'))}
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
  );
}
