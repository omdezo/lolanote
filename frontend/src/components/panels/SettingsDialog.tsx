// Settings dialog — the full account & preferences surface, modeled on
// Milanote's settings modal: a tabbed sidebar (Account, Emails &
// notifications, Appearance, Preferences, Localization, Toolbar options,
// Privacy) with a danger zone for account deletion. Every toggle writes
// through the settings store (optimistic + debounced PATCH).
import { useRef, useState } from 'react';
import { api, exportMyDataBlob, uploadFile } from '../../api/client';
import type { UserSettings } from '../../api/types';
import { logout } from '../../auth/keycloak';
import { t, type TKey } from '../../i18n';
import { useBoard } from '../../store/boardStore';
import { useSettings } from '../../store/settingsStore';
import { toast } from '../ui/Toaster';
import {
  BellIcon, CheckIcon, CloseIcon, DownloadIcon, GlobeIcon, PaletteIcon,
  RailIcon, ShieldIcon, SlidersIcon, UserIcon,
  NoteIcon, LinkIcon, TodoIcon, LineIcon, BoardIcon, ColumnIcon, CommentIcon,
  TableIcon, SketchIcon, ColorIcon, DocumentIcon, AudioIcon, MapIcon,
  VideoIcon, HeadingIcon, ImageIcon, UploadIcon, DrawIcon,
} from '../Icons';

type Tab = 'account' | 'notifications' | 'appearance' | 'preferences' | 'localization' | 'toolbar' | 'privacy';

const TABS: Array<{ id: Tab; label: TKey; icon: JSX.Element }> = [
  { id: 'account', label: 'settings.account', icon: <UserIcon size={16} /> },
  { id: 'notifications', label: 'settings.notifications', icon: <BellIcon size={16} /> },
  { id: 'appearance', label: 'settings.appearance', icon: <PaletteIcon size={16} /> },
  { id: 'preferences', label: 'settings.preferences', icon: <SlidersIcon size={16} /> },
  { id: 'localization', label: 'settings.localization', icon: <GlobeIcon size={16} /> },
  { id: 'toolbar', label: 'settings.toolbar', icon: <RailIcon size={16} /> },
  { id: 'privacy', label: 'settings.privacy', icon: <ShieldIcon size={16} /> },
];

export function SettingsDialog({ onClose }: { onClose: () => void }) {
  const [tab, setTab] = useState<Tab>('account');
  const saveState = useSettings((s) => s.saveState);

  return (
    <div className="modal-backdrop" onPointerDown={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="modal settings-modal" onPointerDown={(e) => e.stopPropagation()}>
        <nav className="settings-nav">
          <div className="settings-nav-title">{t('settings.title')}</div>
          {TABS.map((entry) => (
            <button
              key={entry.id}
              className={`settings-tab${tab === entry.id ? ' active' : ''}`}
              onClick={() => setTab(entry.id)}
            >
              <span className="st-icon">{entry.icon}</span>
              {t(entry.label)}
            </button>
          ))}
          <div className={`settings-save-state${saveState === 'error' ? ' error' : ''}`}>
            {saveState === 'saving' && t('common.saving')}
            {saveState === 'saved' && <><CheckIcon size={12} /> {t('common.saved')}</>}
            {saveState === 'error' && 'Could not save'}
          </div>
        </nav>

        <div className="settings-content">
          <div className="settings-content-head">
            <h2>{t(TABS.find((x) => x.id === tab)!.label)}</h2>
            <button className="panel-close" onClick={onClose} title={t('common.close')}><CloseIcon size={16} /></button>
          </div>
          <div className="settings-body">
            {tab === 'account' && <AccountTab onDeleted={onClose} />}
            {tab === 'notifications' && <NotificationsTab />}
            {tab === 'appearance' && <AppearanceTab />}
            {tab === 'preferences' && <PreferencesTab />}
            {tab === 'localization' && <LocalizationTab />}
            {tab === 'toolbar' && <ToolbarTab />}
            {tab === 'privacy' && <PrivacyTab />}
          </div>
        </div>
      </div>
    </div>
  );
}

// ---- shared row primitives ----

function Row({ label, sub, children }: { label: string; sub?: string; children?: React.ReactNode }) {
  return (
    <div className="settings-row">
      <div className="sr-text">
        <div className="sr-label">{label}</div>
        {sub && <div className="sr-sub">{sub}</div>}
      </div>
      {children}
    </div>
  );
}

function Switch({ on, onChange }: { on: boolean; onChange: (next: boolean) => void }) {
  return <button className={`switch${on ? ' on' : ''}`} role="switch" aria-checked={on} onClick={() => onChange(!on)} />;
}

function Segmented<V extends string>({ value, options, onChange }: {
  value: V;
  options: Array<{ v: V; label: string }>;
  onChange: (v: V) => void;
}) {
  return (
    <div className="segmented">
      {options.map((o) => (
        <button key={o.v} className={value === o.v ? 'on' : ''} onClick={() => onChange(o.v)}>{o.label}</button>
      ))}
    </div>
  );
}

// ---- Account settings ----

function AccountTab({ onDeleted }: { onDeleted: () => void }) {
  const { user, setUser } = useBoard();
  const [editing, setEditing] = useState<'name' | 'email' | 'password' | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmText, setConfirmText] = useState('');
  const [busy, setBusy] = useState(false);
  const [field1, setField1] = useState('');
  const [field2, setField2] = useState('');
  const avatarInput = useRef<HTMLInputElement>(null);

  if (!user) return null;

  const uploadAvatar = async (file: File | undefined | null) => {
    if (!file || !file.type.startsWith('image/')) return;
    setBusy(true);
    try {
      const { url } = await uploadFile(file);
      setUser(await api.updateMe({ avatarUrl: url }));
      toast.success('Photo updated');
    } catch {
      toast.error('Photo upload failed');
    } finally {
      setBusy(false);
    }
  };

  const open = (which: 'name' | 'email' | 'password') => {
    setEditing(editing === which ? null : which);
    setField1(which === 'name' ? user.displayName : which === 'email' ? user.email : '');
    setField2('');
  };

  const save = async () => {
    setBusy(true);
    try {
      if (editing === 'name') {
        setUser(await api.updateMe({ displayName: field1.trim() }));
        toast.success(t('account.nameSaved'));
      } else if (editing === 'email') {
        setUser(await api.updateMe({ email: field1.trim() }));
        toast.success(t('account.emailSaved'));
      } else if (editing === 'password') {
        await api.changePassword(field1, field2);
        toast.success(t('account.passwordChanged'));
      }
      setEditing(null);
    } catch (err: any) {
      toast.error(editing === 'password' ? t('account.passwordFailed') : (err?.message || 'Failed'));
    } finally {
      setBusy(false);
    }
  };

  const doDelete = async () => {
    setBusy(true);
    try {
      await api.deleteAccount();
      onDeleted();
      logout();
    } catch (err: any) {
      toast.error(err?.message || 'Account deletion failed');
      setBusy(false);
    }
  };

  const passwordValid = editing !== 'password' || (field1.length > 0 && field2.length >= 8);

  return (
    <>
      <Row label="Photo" sub={user.avatarUrl ? 'Custom photo' : 'Initials'}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span className="avatar-btn" style={{ cursor: 'default' }}>
            {user.avatarUrl
              ? <img className="avatar-img" src={user.avatarUrl} alt="" />
              : (user.displayName || '?').split(/\s+/).map((w) => w[0]).slice(0, 2).join('')}
          </span>
          <button className="sr-action" disabled={busy} onClick={() => avatarInput.current?.click()}>{t('account.change')}</button>
          {user.avatarUrl && (
            <button className="sr-action danger" disabled={busy}
              onClick={() => void api.updateMe({ avatarUrl: '-' }).then(setUser)}>
              Remove
            </button>
          )}
          <input ref={avatarInput} type="file" accept="image/*" hidden onChange={(e) => void uploadAvatar(e.target.files?.[0])} />
        </div>
      </Row>
      <Row label={t('account.name')} sub={user.displayName}>
        <button className="sr-action" onClick={() => open('name')}>{t('account.change')}</button>
      </Row>
      {editing === 'name' && (
        <div className="settings-inline-form">
          <input value={field1} placeholder={t('account.newName')} onChange={(e) => setField1(e.target.value)} autoFocus />
          <div className="sif-actions">
            <button className="btn-quiet" onClick={() => setEditing(null)}>{t('account.cancel')}</button>
            <button className="btn-primary" disabled={busy || !field1.trim()} onClick={() => void save()}>{t('account.save')}</button>
          </div>
        </div>
      )}

      <Row label={t('account.email')} sub={user.email}>
        <button className="sr-action" onClick={() => open('email')}>{t('account.change')}</button>
      </Row>
      {editing === 'email' && (
        <div className="settings-inline-form">
          <input value={field1} type="email" placeholder={t('account.newEmail')} onChange={(e) => setField1(e.target.value)} autoFocus />
          <div className="sif-actions">
            <button className="btn-quiet" onClick={() => setEditing(null)}>{t('account.cancel')}</button>
            <button className="btn-primary" disabled={busy || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(field1)} onClick={() => void save()}>{t('account.save')}</button>
          </div>
        </div>
      )}

      <Row label={t('account.password')} sub="••••••••">
        <button className="sr-action" onClick={() => open('password')}>{t('account.reset')}</button>
      </Row>
      {editing === 'password' && (
        <div className="settings-inline-form">
          <input value={field1} type="password" placeholder={t('account.currentPassword')} onChange={(e) => setField1(e.target.value)} autoFocus autoComplete="current-password" />
          <input value={field2} type="password" placeholder={t('account.newPassword')} onChange={(e) => setField2(e.target.value)} autoComplete="new-password" />
          <div className="sif-actions">
            <button className="btn-quiet" onClick={() => setEditing(null)}>{t('account.cancel')}</button>
            <button className="btn-primary" disabled={busy || !passwordValid} onClick={() => void save()}>{t('account.save')}</button>
          </div>
        </div>
      )}

      <Row label={t('account.plan')}>
        <span className="sr-value" style={{ textTransform: 'capitalize' }}>{user.plan}</span>
      </Row>

      <div className="danger-zone">
        <h4>{t('account.dangerZone')}</h4>
        <button className="danger-link" onClick={() => setConfirmDelete(!confirmDelete)}>
          {t('account.deleteAccount')}
        </button>
        {confirmDelete && (
          <div className="danger-confirm">
            <p>{t('account.deleteWarning')}</p>
            <input
              value={confirmText}
              placeholder={t('account.deleteConfirmType')}
              onChange={(e) => setConfirmText(e.target.value)}
            />
            <div className="dc-actions">
              <button className="btn-quiet" onClick={() => { setConfirmDelete(false); setConfirmText(''); }}>{t('account.cancel')}</button>
              <button className="btn-danger" disabled={busy || confirmText !== 'DELETE'} onClick={() => void doDelete()}>
                {t('account.deleteForever')}
              </button>
            </div>
          </div>
        )}
      </div>
    </>
  );
}

// ---- Emails & notifications ----

function NotificationsTab() {
  const { settings, update } = useSettings();
  const n = settings.notifications;
  const set = (patch: Partial<UserSettings['notifications']>) => update({ notifications: patch });

  return (
    <>
      <div className="settings-section-label">{t('notif.inApp')}</div>
      <Row label={t('notif.mentions')} sub={t('notif.mentionsSub')}><Switch on={n.mentions} onChange={(v) => set({ mentions: v })} /></Row>
      <Row label={t('notif.comments')} sub={t('notif.commentsSub')}><Switch on={n.comments} onChange={(v) => set({ comments: v })} /></Row>
      <Row label={t('notif.shares')} sub={t('notif.sharesSub')}><Switch on={n.shares} onChange={(v) => set({ shares: v })} /></Row>
      <Row label={t('notif.assignments')} sub={t('notif.assignmentsSub')}><Switch on={n.assignments} onChange={(v) => set({ assignments: v })} /></Row>
      <Row label={t('notif.reminders')} sub={t('notif.remindersSub')}><Switch on={n.reminders} onChange={(v) => set({ reminders: v })} /></Row>
      <Row label={t('notif.boardChanges')} sub={t('notif.boardChangesSub')}><Switch on={n.boardChanges} onChange={(v) => set({ boardChanges: v })} /></Row>

      <div className="settings-section-label">{t('notif.email')}</div>
      <Row label={t('notif.emailEnabled')} sub={t('notif.emailEnabledSub')}><Switch on={n.emailEnabled} onChange={(v) => set({ emailEnabled: v })} /></Row>
      <Row label={t('notif.digest')} sub={t('notif.digestSub')}>
        <Segmented
          value={n.emailDigest}
          options={[
            { v: 'off', label: t('notif.digest.off') },
            { v: 'daily', label: t('notif.digest.daily') },
            { v: 'weekly', label: t('notif.digest.weekly') },
          ]}
          onChange={(v) => set({ emailDigest: v })}
        />
      </Row>
    </>
  );
}

// ---- Appearance ----

const ACCENTS = ['#5e5ce6', '#1c7ed6', '#0ca678', '#2eb85c', '#f2a20d', '#e8590c', '#e64980', '#845ef7', '#212529'];

function AppearanceTab() {
  const { settings, update } = useSettings();
  const a = settings.appearance;
  const set = (patch: Partial<UserSettings['appearance']>) => update({ appearance: patch });

  return (
    <>
      <Row label={t('appearance.theme')}>
        <Segmented
          value={a.theme}
          options={[
            { v: 'light', label: t('appearance.theme.light') },
            { v: 'dark', label: t('appearance.theme.dark') },
            { v: 'system', label: t('appearance.theme.system') },
          ]}
          onChange={(v) => set({ theme: v })}
        />
      </Row>
      <Row label={t('appearance.accent')}>
        <div className="accent-swatches">
          {ACCENTS.map((hex) => (
            <button
              key={hex}
              className={`accent-swatch${a.accentColor === hex ? ' on' : ''}`}
              style={{ background: hex }}
              onClick={() => set({ accentColor: hex })}
              title={hex}
            />
          ))}
        </div>
      </Row>
      <Row label={t('appearance.dotGrid')} sub={t('appearance.dotGridSub')}>
        <Switch on={a.dotGrid} onChange={(v) => set({ dotGrid: v })} />
      </Row>
      <Row label={t('appearance.cardShadows')} sub={t('appearance.cardShadowsSub')}>
        <Switch on={a.cardShadows} onChange={(v) => set({ cardShadows: v })} />
      </Row>
      <Row label={t('appearance.density')}>
        <Segmented
          value={a.uiDensity}
          options={[
            { v: 'comfortable', label: t('appearance.density.comfortable') },
            { v: 'compact', label: t('appearance.density.compact') },
          ]}
          onChange={(v) => set({ uiDensity: v })}
        />
      </Row>
    </>
  );
}

// ---- Preferences ----

function PreferencesTab() {
  const { settings, update } = useSettings();
  const p = settings.preferences;
  const set = (patch: Partial<UserSettings['preferences']>) => update({ preferences: patch });

  return (
    <>
      <Row label={t('prefs.doubleClick')}>
        <Segmented
          value={p.doubleClickCreates}
          options={[
            { v: 'note', label: t('prefs.doubleClick.note') },
            { v: 'board', label: t('prefs.doubleClick.board') },
            { v: 'none', label: t('prefs.doubleClick.none') },
          ]}
          onChange={(v) => set({ doubleClickCreates: v })}
        />
      </Row>
      <Row label={t('prefs.wheel')} sub={t('prefs.wheelSub')}>
        <Segmented
          value={p.wheelMode}
          options={[
            { v: 'pan', label: t('prefs.wheel.pan') },
            { v: 'zoom', label: t('prefs.wheel.zoom') },
          ]}
          onChange={(v) => set({ wheelMode: v })}
        />
      </Row>
      <Row label={t('prefs.snap')} sub={t('prefs.snapSub')}>
        <Switch on={p.snapToGrid} onChange={(v) => set({ snapToGrid: v })} />
      </Row>
      <Row label={t('prefs.spell')} sub={t('prefs.spellSub')}>
        <Switch on={p.spellCheck} onChange={(v) => set({ spellCheck: v })} />
      </Row>
      <Row label={t('prefs.hints')} sub={t('prefs.hintsSub')}>
        <Switch on={p.showHints} onChange={(v) => set({ showHints: v })} />
      </Row>
    </>
  );
}

// ---- Localization ----

function LocalizationTab() {
  const { settings, update } = useSettings();
  const l = settings.localization;
  const set = (patch: Partial<UserSettings['localization']>) => update({ localization: patch });

  return (
    <>
      <Row label={t('loc.language')}>
        <Segmented
          value={l.language}
          options={[
            { v: 'en', label: 'English' },
            { v: 'ar', label: 'العربية' },
          ]}
          onChange={(v) => set({ language: v })}
        />
      </Row>
      <Row label={t('loc.firstDay')}>
        <Segmented
          value={String(l.firstDayOfWeek) as '0' | '1' | '6'}
          options={[
            { v: '1', label: t('loc.day.monday') },
            { v: '0', label: t('loc.day.sunday') },
            { v: '6', label: t('loc.day.saturday') },
          ]}
          onChange={(v) => set({ firstDayOfWeek: Number(v) as 0 | 1 | 6 })}
        />
      </Row>
      <Row label={t('loc.dateFormat')}>
        <Segmented
          value={l.dateFormat}
          options={[
            { v: 'auto', label: t('loc.dateFormat.auto') },
            { v: 'dmy', label: 'DD/MM/YYYY' },
            { v: 'mdy', label: 'MM/DD/YYYY' },
            { v: 'ymd', label: 'YYYY-MM-DD' },
          ]}
          onChange={(v) => set({ dateFormat: v })}
        />
      </Row>
      <Row label={t('loc.timeFormat')}>
        <Segmented
          value={l.timeFormat}
          options={[
            { v: '12h', label: t('loc.timeFormat.12h') },
            { v: '24h', label: t('loc.timeFormat.24h') },
          ]}
          onChange={(v) => set({ timeFormat: v })}
        />
      </Row>
    </>
  );
}

// ---- Toolbar options ----

const TOOL_DEFS: Array<{ id: string; label: TKey; icon: JSX.Element }> = [
  { id: 'note', label: 'tool.note', icon: <NoteIcon size={15} /> },
  { id: 'link', label: 'tool.link', icon: <LinkIcon size={15} /> },
  { id: 'todo', label: 'tool.todo', icon: <TodoIcon size={15} /> },
  { id: 'line', label: 'tool.line', icon: <LineIcon size={15} /> },
  { id: 'board', label: 'tool.board', icon: <BoardIcon size={15} /> },
  { id: 'column', label: 'tool.column', icon: <ColumnIcon size={15} /> },
  { id: 'comment', label: 'tool.comment', icon: <CommentIcon size={15} /> },
  { id: 'table', label: 'tool.table', icon: <TableIcon size={15} /> },
  { id: 'sketch', label: 'tool.sketch', icon: <SketchIcon size={15} /> },
  { id: 'color', label: 'tool.color', icon: <ColorIcon size={15} /> },
  { id: 'document', label: 'tool.document', icon: <DocumentIcon size={15} /> },
  { id: 'audio', label: 'tool.audio', icon: <AudioIcon size={15} /> },
  { id: 'map', label: 'tool.map', icon: <MapIcon size={15} /> },
  { id: 'video', label: 'tool.video', icon: <VideoIcon size={15} /> },
  { id: 'heading', label: 'tool.heading', icon: <HeadingIcon size={15} /> },
  { id: 'image', label: 'tool.image', icon: <ImageIcon size={15} /> },
  { id: 'upload', label: 'tool.upload', icon: <UploadIcon size={15} /> },
  { id: 'draw', label: 'tool.draw', icon: <DrawIcon size={15} /> },
];

function ToolbarTab() {
  const { settings, update } = useSettings();
  const hidden = new Set(settings.toolbar.hiddenTools);

  const toggle = (id: string, visible: boolean) => {
    const next = new Set(hidden);
    if (visible) next.delete(id); else next.add(id);
    update({ toolbar: { hiddenTools: Array.from(next) } });
  };

  return (
    <>
      <p className="sr-sub" style={{ margin: '4px 0 14px' }}>{t('toolbaropts.hint')}</p>
      <div className="tool-toggle-grid">
        {TOOL_DEFS.map((tool) => (
          <div key={tool.id} className="tool-toggle">
            <span className="tt-name"><span className="tt-icon">{tool.icon}</span>{t(tool.label)}</span>
            <Switch on={!hidden.has(tool.id)} onChange={(v) => toggle(tool.id, v)} />
          </div>
        ))}
      </div>
    </>
  );
}

// ---- Privacy ----

function PrivacyTab() {
  const { settings, update } = useSettings();
  const p = settings.privacy;
  const set = (patch: Partial<UserSettings['privacy']>) => update({ privacy: patch });
  const [exporting, setExporting] = useState(false);

  const download = async () => {
    setExporting(true);
    try {
      const blob = await exportMyDataBlob();
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'qomranote-export.json';
      a.click();
      URL.revokeObjectURL(a.href);
    } catch {
      toast.error('Export failed');
    } finally {
      setExporting(false);
    }
  };

  return (
    <>
      <Row label={t('privacy.presence')} sub={t('privacy.presenceSub')}>
        <Switch on={p.showPresence} onChange={(v) => set({ showPresence: v })} />
      </Row>
      <Row label={t('privacy.email')} sub={t('privacy.emailSub')}>
        <Switch on={p.showEmailToOthers} onChange={(v) => set({ showEmailToOthers: v })} />
      </Row>
      <Row label={t('privacy.export')} sub={t('privacy.exportSub')}>
        <button className="sr-action" disabled={exporting} onClick={() => void download()}>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
            <DownloadIcon size={14} /> {t('privacy.download')}
          </span>
        </button>
      </Row>
    </>
  );
}
