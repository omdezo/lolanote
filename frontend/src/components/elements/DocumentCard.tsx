// Document (§4.2): long-form writing. On the board it renders as a compact
// card (icon + title + preview); double-click opens the focused, full-width
// writing view — the same distinction Milanote draws between notes and
// documents.
import { useEffect, useId, useState } from 'react';
import { EditorContent, useEditor } from '@tiptap/react';
import StarterKit from '@tiptap/starter-kit';
import Placeholder from '@tiptap/extension-placeholder';
import type { QElement } from '../../api/types';
import { useT } from '../../i18n';
import { dirAttr, elementDir, smartDigitsTextInput } from '../../lib/direction';
import { toTextPreview } from '../../lib/textPreview';
import { sendEditing } from '../../realtime/socket';
import { updateOp, useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import { useDialog } from '../ui/Modal';
import { CheckIcon, DocumentIcon } from '../Icons';

export function DocumentCard({ element }: { element: QElement }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  // OPEN arrives from three places and they must not drift: the mouse's
  // double-click, Enter on a focused shell, and the second tap on a selected
  // card. The latter two go through activateElement, which spells "open this"
  // as `setEditing(id)` — and this card listened to neither, so the keyboard's
  // half of AX2 reached the DOCUMENT shell and then died silently there.
  const editingId = useView((s) => s.editingId);
  useEffect(() => {
    if (editingId === element.id) setOpen(true);
  }, [editingId, element.id]);

  const close = () => {
    setOpen(false);
    if (useView.getState().editingId === element.id) useView.getState().setEditing(null);
  };

  return (
    <>
      <div className="doc-card" onDoubleClick={(e) => { e.stopPropagation(); setOpen(true); }} title={t('a11y.openHint')}>
        <div className="doc-badge"><DocumentIcon size={20} /></div>
        <div style={{ minWidth: 0 }}>
          <div className="doc-title">{element.content?.title || t('doc.untitled')}</div>
          <div className="doc-preview">{element.content?.textPreview || t('doc.empty')}</div>
        </div>
      </div>
      {open && <DocumentEditor element={element} onClose={close} />}
    </>
  );
}

function DocumentEditor({ element, onClose }: { element: QElement; onClose: () => void }) {
  const t = useT();
  const titleId = useId();
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [title, setTitle] = useState<string>(element.content?.title ?? '');

  const editor = useEditor({
    extensions: [StarterKit, Placeholder.configure({ placeholder: t('doc.write') })],
    content: element.content?.doc ?? '',
    autofocus: 'end',
    // Typing digits inside Arabic text produces Arabic-Indic numerals.
    editorProps: { handleTextInput: smartDigitsTextInput },
  });

  useEffect(() => {
    sendEditing(element.id, true);
    return () => sendEditing(element.id, false);
  }, [element.id]);

  const save = () => {
    if (!editor) { onClose(); return; }
    const doc = editor.getJSON();
    const textPreview = toTextPreview(editor.getText());
    const changed =
      JSON.stringify(doc) !== JSON.stringify(element.content?.doc ?? null) ||
      title !== (element.content?.title ?? '');
    if (changed) {
      void commitTransaction([updateOp(element, { content: { doc, textPreview, title } })]);
    }
    onClose();
  };

  // Escape saves and closes, like clicking Done. A writing surface that throws
  // away what you typed because you reached for the key that closes everything
  // else in the app would be a worse bug than not having the key at all.
  const { boxRef, onKeyDown } = useDialog('doc-editor', save);

  // 'auto' detects per paragraph from the first strong letter; a manual
  // override (element context menu) forces the whole document.
  const dir = elementDir(element);

  return (
    <div className="modal-backdrop" onClick={save} onPointerDown={(e) => e.stopPropagation()}>
      {/* AX11: a full-bleed writing view rather than a `.modal-head` box, so it
          takes the dialog behaviour without the chrome. Escape lives on the
          container — which is exactly why nothing here ever had one and the
          only way out of a document was to click the backdrop. */}
      <div
        ref={boxRef}
        className="modal doc-editor"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="doc-editor-head">
          <span className="sr-only" id={titleId}>{title || t('doc.untitled')}</span>
          <input
            className="doc-editor-title"
            dir={dirAttr(dir)}
            value={title}
            aria-label={t('tool.document')}
            placeholder={t('doc.untitled')}
            onChange={(e) => setTitle(e.target.value)}
          />
          <button className="pi-btn" onClick={save}><CheckIcon size={14} /> {t('doc.done')}</button>
        </div>
        <div
          className={`doc-editor-body${dir === 'auto' ? ' bidi-auto' : ''}`}
          dir={dir === 'auto' ? undefined : dir}
        >
          <EditorContent editor={editor} />
        </div>
      </div>
    </div>
  );
}
