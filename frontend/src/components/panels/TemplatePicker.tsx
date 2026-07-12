// Template picker (§5): a gallery of system + custom templates. "Use" stamps
// a fresh editable copy of the template's subtree into the current board via
// the duplicate service.
import { useEffect, useState } from 'react';
import { api } from '../../api/client';
import type { QElement } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import { toast } from '../ui/Toaster';
import { CloseIcon, TemplateIcon } from '../Icons';

export function TemplatePicker({ onClose }: { onClose: () => void }) {
  const [templates, setTemplates] = useState<QElement[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const refreshBoard = useBoard((s) => s.refreshBoard);

  useEffect(() => {
    api.templates().then(setTemplates).catch(() => setTemplates([]));
  }, []);

  const use = async (tpl: QElement) => {
    setBusy(tpl.id);
    try {
      // Server-side stamp: duplicates the template subtree straight into the
      // current board (respects the cross-board move guard).
      await api.useTemplate(tpl.id, useBoard.getState().boardId, { x: 140, y: 140 });
      await refreshBoard();
      toast.success(`Added “${tpl.content?.title || 'template'}”`);
      onClose();
    } catch {
      toast.error('Could not add template');
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" style={{ width: 640 }} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3><TemplateIcon size={17} /> &nbsp;Templates</h3>
          <button className="panel-close" onClick={onClose}><CloseIcon size={15} /></button>
        </div>
        <div className="modal-body">
          {templates.length === 0 && <div className="panel-empty" style={{ paddingTop: 30 }}>No templates yet. Right-click a board → “Convert to template”.</div>}
          <div className="template-grid">
            {templates.map((t) => (
              <div key={t.id} className="template-card">
                <div className="template-thumb"><TemplateIcon size={26} /></div>
                <div className="template-title">{t.content?.title || 'Untitled'}</div>
                <button className="pi-btn" disabled={busy === t.id} onClick={() => void use(t)}>
                  {busy === t.id ? 'Adding…' : 'Use template'}
                </button>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
