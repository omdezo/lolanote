// Template picker (§5): a gallery of system + custom templates. "Use" stamps
// a fresh editable copy of the template's subtree into the current board via
// the duplicate service.
import { useEffect, useState } from 'react';
import { api } from '../../api/client';
import type { QElement } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import { useT } from '../../i18n';
import { Modal } from '../ui/Modal';
import { toast } from '../ui/Toaster';
import { CloseIcon, TemplateIcon } from '../Icons';

export function TemplatePicker({ onClose }: { onClose: () => void }) {
  const t = useT();
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
      toast.success(`${t('tmpl.added')} “${tpl.content?.title || t('common.untitled')}”`);
      onClose();
    } catch {
      toast.error(t('tmpl.failed'));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Modal
      title={<><TemplateIcon size={17} /> &nbsp;{t('topbar.templates')}</>}
      overlayId="template-picker"
      onClose={onClose}
      style={{ width: 640 }}
      headExtra={<button className="panel-close" onClick={onClose} aria-label={t('common.close')} title={t('common.close')}><CloseIcon size={15} /></button>}
    >
        <div className="modal-body">
          {templates.length === 0 && <div className="panel-empty" style={{ paddingTop: 30 }}>{t('tmpl.none')}</div>}
          <div className="template-grid">
            {templates.map((tpl) => (
              <div key={tpl.id} className="template-card">
                <div className="template-thumb"><TemplateIcon size={26} /></div>
                <div className="template-title">{tpl.content?.title || t('common.untitled')}</div>
                <button className="pi-btn" disabled={busy === tpl.id} onClick={() => void use(tpl)}>
                  {busy === tpl.id ? t('tmpl.adding') : t('tmpl.use')}
                </button>
              </div>
            ))}
          </div>
        </div>
    </Modal>
  );
}
