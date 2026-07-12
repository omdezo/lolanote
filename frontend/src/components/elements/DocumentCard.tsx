// Document (§4.2): long-form writing. On the board it renders as a compact
// card (icon + title + preview); double-click opens the focused, full-width
// writing view — the same distinction Milanote draws between notes and
// documents.
import { useEffect, useState } from 'react';
import { EditorContent, useEditor } from '@tiptap/react';
import StarterKit from '@tiptap/starter-kit';
import Placeholder from '@tiptap/extension-placeholder';
import type { QElement } from '../../api/types';
import { sendEditing } from '../../realtime/socket';
import { updateOp, useBoard } from '../../store/boardStore';
import { CheckIcon, DocumentIcon } from '../Icons';

export function DocumentCard({ element }: { element: QElement }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <div className="doc-card" onDoubleClick={(e) => { e.stopPropagation(); setOpen(true); }} title="Double-click to write">
        <div className="doc-badge"><DocumentIcon size={20} /></div>
        <div style={{ minWidth: 0 }}>
          <div className="doc-title">{element.content?.title || 'Untitled document'}</div>
          <div className="doc-preview">{element.content?.textPreview || 'Empty document'}</div>
        </div>
      </div>
      {open && <DocumentEditor element={element} onClose={() => setOpen(false)} />}
    </>
  );
}

function DocumentEditor({ element, onClose }: { element: QElement; onClose: () => void }) {
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [title, setTitle] = useState<string>(element.content?.title ?? '');

  const editor = useEditor({
    extensions: [StarterKit, Placeholder.configure({ placeholder: 'Start writing…' })],
    content: element.content?.doc ?? '',
    autofocus: 'end',
  });

  useEffect(() => {
    sendEditing(element.id, true);
    return () => sendEditing(element.id, false);
  }, [element.id]);

  const save = () => {
    if (!editor) { onClose(); return; }
    const doc = editor.getJSON();
    const textPreview = editor.getText().slice(0, 500);
    const changed =
      JSON.stringify(doc) !== JSON.stringify(element.content?.doc ?? null) ||
      title !== (element.content?.title ?? '');
    if (changed) {
      void commitTransaction([updateOp(element, { content: { doc, textPreview, title } })]);
    }
    onClose();
  };

  return (
    <div className="modal-backdrop" onClick={save} onPointerDown={(e) => e.stopPropagation()}>
      <div className="modal doc-editor" onClick={(e) => e.stopPropagation()}>
        <div className="doc-editor-head">
          <input
            className="doc-editor-title"
            value={title}
            placeholder="Untitled document"
            onChange={(e) => setTitle(e.target.value)}
          />
          <button className="pi-btn" onClick={save}><CheckIcon size={14} /> Done</button>
        </div>
        <div className="doc-editor-body">
          <EditorContent editor={editor} />
        </div>
      </div>
    </div>
  );
}
