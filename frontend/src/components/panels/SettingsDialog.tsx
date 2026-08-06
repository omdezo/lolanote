// Settings dialog — the full account & preferences surface, modeled on
// Milanote's settings modal: a tabbed sidebar (Account, Emails &
// notifications, Appearance, Preferences, Localization, Toolbar options,
// Privacy) with a danger zone for account deletion. Every toggle writes
// through the settings store (optimistic + debounced PATCH).
import { createContext, useContext, useId, useRef, useState } from 'react';
import { api, exportMyDataBlob, uploadFile } from '../../api/client';
import type { UserSettings } from '../../api/types';
import { logout } from '../../auth/keycloak';
import { t, type TKey } from '../../i18n';
import { useBoard } from '../../store/boardStore';
import { useSettings } from '../../store/settingsStore';
import { SHORTCUTS } from '../../lib/shortcuts';
import { useDialog } from '../ui/Modal';
import { toast } from '../ui/Toaster';
import {
  AccessIcon, BellIcon, CheckIcon, CloseIcon, DownloadIcon, GlobeIcon, PaletteIcon,
  RailIcon, ShieldIcon, SlidersIcon, SparkleIcon, UserIcon,
  NoteIcon, LinkIcon, TodoIcon, LineIcon, BoardIcon, ColumnIcon, CommentIcon,
  TableIcon, SketchIcon, ColorIcon, DocumentIcon, AudioIcon, MapIcon,
  VideoIcon, HeadingIcon, ImageIcon, UploadIcon, DrawIcon,
} from '../Icons';

type Tab = 'account' | 'notifications' | 'appearance' | 'accessibility' | 'preferences' | 'localization' | 'toolbar' | 'agent' | 'privacy';

const TABS: Array<{ id: Tab; label: TKey; icon: JSX.Element }> = [
  { id: 'account', label: 'settings.account', icon: <UserIcon size={16} /> },
  { id: 'notifications', label: 'settings.notifications', icon: <BellIcon size={16} /> },
  { id: 'appearance', label: 'settings.appearance', icon: <PaletteIcon size={16} /> },
  { id: 'accessibility', label: 'settings.accessibility', icon: <AccessIcon size={16} /> },
  { id: 'preferences', label: 'settings.preferences', icon: <SlidersIcon size={16} /> },
  { id: 'localization', label: 'settings.localization', icon: <GlobeIcon size={16} /> },
  { id: 'toolbar', label: 'settings.toolbar', icon: <RailIcon size={16} /> },
  { id: 'agent', label: 'settings.agent', icon: <SparkleIcon size={16} /> },
  { id: 'privacy', label: 'settings.privacy', icon: <ShieldIcon size={16} /> },
];

export function SettingsDialog({ onClose }: { onClose: () => void }) {
  const [tab, setTab] = useState<Tab>('account');
  const saveState = useSettings((s) => s.saveState);
  const { boxRef, onKeyDown } = useDialog('settings-dialog', onClose);
  const titleId = useId();

  return (
    <div className="modal-backdrop" onPointerDown={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      {/* AX11: a real dialog — named, modal, trapped, restorable, Escape on the
          container. It cannot use <Modal> because it is a two-column layout
          with its own nav where the head row would be, so it takes the
          behaviour without the chrome. */}
      <div
        ref={boxRef}
        className="modal settings-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
        onPointerDown={(e) => e.stopPropagation()}
      >
        <nav className="settings-nav">
          <div className="settings-nav-title" id={titleId}>{t('settings.title')}</div>
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
            {saveState === 'error' && t('settings.saveFailed')}
          </div>
        </nav>

        <div className="settings-content">
          <div className="settings-content-head">
            <h2>{t(TABS.find((x) => x.id === tab)!.label)}</h2>
            <button className="panel-close" onClick={onClose} aria-label={t('common.close')} title={t('common.close')}><CloseIcon size={16} /></button>
          </div>
          <div className="settings-body">
            {tab === 'account' && <AccountTab onDeleted={onClose} />}
            {tab === 'notifications' && <NotificationsTab />}
            {tab === 'appearance' && <AppearanceTab />}
            {tab === 'accessibility' && <AccessibilityTab />}
            {tab === 'preferences' && <PreferencesTab />}
            {tab === 'localization' && <LocalizationTab />}
            {tab === 'toolbar' && <ToolbarTab />}
            {tab === 'agent' && <AgentTab />}
            {tab === 'privacy' && <PrivacyTab />}
          </div>
        </div>
      </div>
    </div>
  );
}

// ---- shared row primitives ----

/**
 * AX21. The one place in this product that reached for ARIA reached halfway.
 *
 * `<button role="switch" aria-checked={on} />` with NO accessible name, sixteen
 * times — including `showEmailToOthers` and `showPresence`, which are privacy
 * settings. The label was right there in a sibling `<div className="sr-label">`
 * with no id and nothing pointing at it. A blind user could hear that sixteen
 * switches existed and the state of each, and could not learn what any of them
 * controlled. `role="switch"` without a name is strictly worse than a plain
 * checkbox inside a `<label>`, which would have been correct with no ARIA at
 * all.
 *
 * The fix is structural rather than per-call-site: `Row` already owns the label
 * and the sub-line, so it mints ids for them and publishes them here. Every
 * control inside a Row is named by its Row automatically, which is the only
 * arrangement in which the seventeenth switch is also named.
 */
const RowLabel = createContext<{ labelId: string; subId?: string } | null>(null);

function Row({ label, sub, children }: { label: string; sub?: string; children?: React.ReactNode }) {
  const base = useId();
  const labelId = `${base}-l`;
  const subId = sub ? `${base}-s` : undefined;
  return (
    <RowLabel.Provider value={{ labelId, subId }}>
      <div className="settings-row">
        <div className="sr-text">
          <div className="sr-label" id={labelId}>{label}</div>
          {sub && <div className="sr-sub" id={subId}>{sub}</div>}
        </div>
        {children}
      </div>
    </RowLabel.Provider>
  );
}

function Switch({ on, onChange }: { on: boolean; onChange: (next: boolean) => void }) {
  const named = useContext(RowLabel);
  return (
    <button
      className={`switch${on ? ' on' : ''}`}
      role="switch"
      aria-checked={on}
      aria-labelledby={named?.labelId}
      aria-describedby={named?.subId}
      onClick={() => onChange(!on)}
    />
  );
}

/**
 * A choice among a few, which is a radio group and never was one: selection was
 * conveyed by a CSS class alone, so every option announced identically and the
 * current one was indistinguishable from the rest. Arrow keys move between
 * options, which is what a radio group's roving tabindex is for and what makes
 * this operable with one hand on the keyboard.
 */
function Segmented<V extends string>({ value, options, onChange }: {
  value: V;
  options: Array<{ v: V; label: string }>;
  onChange: (v: V) => void;
}) {
  const named = useContext(RowLabel);
  const move = (i: number, delta: number) => {
    const next = options[(i + delta + options.length) % options.length];
    onChange(next.v);
  };
  return (
    <div className="segmented" role="radiogroup" aria-labelledby={named?.labelId} aria-describedby={named?.subId}>
      {options.map((o, i) => (
        <button
          key={o.v}
          role="radio"
          aria-checked={value === o.v}
          tabIndex={value === o.v ? 0 : -1}
          className={value === o.v ? 'on' : ''}
          onKeyDown={(e) => {
            if (e.key === 'ArrowRight' || e.key === 'ArrowDown') { e.preventDefault(); move(i, 1); }
            if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') { e.preventDefault(); move(i, -1); }
          }}
          onClick={() => onChange(o.v)}
        >{o.label}</button>
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
      toast.success(t('toast.photoSaved'));
    } catch {
      toast.error(t('toast.photoFailed'));
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
      // Awaited: the person has just asked for everything to be destroyed, and
      // the local mirror is the most accessible copy of all of it.
      await logout();
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
              {t('share.remove')}
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

/** The nine swatches, said aloud. */
const ACCENT_NAMES: Record<string, TKey> = {
  '#5e5ce6': 'accent.indigo', '#1c7ed6': 'accent.blue', '#0ca678': 'accent.teal',
  '#2eb85c': 'accent.green', '#f2a20d': 'accent.amber', '#e8590c': 'accent.orange',
  '#e64980': 'accent.pink', '#845ef7': 'accent.violet', '#212529': 'accent.graphite',
};

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
        {/* Named by colour, not by hex. "#845ef7" is not a colour to anybody
            who cannot see it, and it was the swatch's only accessible name. */}
        <div className="accent-swatches" role="radiogroup" aria-label={t('appearance.accent')}>
          {ACCENTS.map((hex) => (
            <button
              key={hex}
              role="radio"
              aria-checked={a.accentColor === hex}
              aria-label={t(ACCENT_NAMES[hex])}
              className={`accent-swatch${a.accentColor === hex ? ' on' : ''}`}
              style={{ background: hex }}
              onClick={() => set({ accentColor: hex })}
              title={t(ACCENT_NAMES[hex])}
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

// ---- Accessibility ----

/**
 * AX30. Settings had five root-attribute channels — theme, density, dot grid,
 * card shadows, accent — and zero accessibility options. The product would let
 * a person turn off card shadows and could not let them turn off a
 * full-viewport camera tween the agent fires on every plan arrival.
 *
 * The plumbing was never the obstacle; `applySideEffects` already stamps five
 * attributes on the root and already subscribes to one OS media query. What was
 * missing was anybody deciding these were settings. Stating it as one product
 * surface with one owner is what turns a scattered list of CSS fixes into a
 * place a person can go — and, for `announceVerbosity`, into the only place a
 * question with no correct default can be answered by the person it is about.
 */
function AccessibilityTab() {
  const { settings, update } = useSettings();
  const a = settings.accessibility;
  const set = (patch: Partial<UserSettings['accessibility']>) => update({ accessibility: patch });

  return (
    <>
      <Row label={t('a11y.motion')} sub={t('a11y.motionSub')}>
        <Segmented
          value={a.motion}
          options={[
            { v: 'system', label: t('a11y.system') },
            { v: 'full', label: t('a11y.motionFull') },
            { v: 'reduced', label: t('a11y.motionReduced') },
          ]}
          onChange={(v) => set({ motion: v })}
        />
      </Row>
      <Row label={t('a11y.contrast')} sub={t('a11y.contrastSub')}>
        <Segmented
          value={a.contrast}
          options={[
            { v: 'system', label: t('a11y.system') },
            { v: 'normal', label: t('a11y.contrastNormal') },
            { v: 'more', label: t('a11y.contrastMore') },
          ]}
          onChange={(v) => set({ contrast: v })}
        />
      </Row>
      <Row label={t('a11y.textScale')} sub={t('a11y.textScaleSub')}>
        <Segmented
          value={String(a.textScale) as '100' | '115' | '130' | '150'}
          options={[
            { v: '100', label: t('a11y.scale100') },
            { v: '115', label: t('a11y.scale115') },
            { v: '130', label: t('a11y.scale130') },
            { v: '150', label: t('a11y.scale150') },
          ]}
          onChange={(v) => set({ textScale: Number(v) as UserSettings['accessibility']['textScale'] })}
        />
      </Row>
      <Row label={t('a11y.transparency')} sub={t('a11y.transparencySub')}>
        <Segmented
          value={a.transparency}
          options={[
            { v: 'system', label: t('a11y.system') },
            { v: 'full', label: t('a11y.transparencyFull') },
            { v: 'reduced', label: t('a11y.transparencyReduced') },
          ]}
          onChange={(v) => set({ transparency: v })}
        />
      </Row>
      {/* No default can guess this one. For one person forty staged actions
          ARE the review; for another they are forty interruptions. */}
      <Row label={t('a11y.verbosity')} sub={t('a11y.verbositySub')}>
        <Segmented
          value={a.announceVerbosity}
          options={[
            { v: 'outcome', label: t('a11y.verbosityOutcome') },
            { v: 'normal', label: t('a11y.verbosityNormal') },
            { v: 'verbose', label: t('a11y.verbosityVerbose') },
          ]}
          onChange={(v) => set({ announceVerbosity: v })}
        />
      </Row>
      <Row label={t('a11y.singleKey')} sub={t('a11y.singleKeySub')}>
        <Switch on={a.singleKeyShortcuts} onChange={(v) => set({ singleKeyShortcuts: v })} />
      </Row>

      {/* AX20's other half. A shortcut a person cannot enumerate is a shortcut
          they cannot avoid, and this keymap lived in three files with no shared
          vocabulary — which is exactly why `z` went unnoticed for so long. */}
      <div className="settings-section-head">{t('a11y.shortcuts')}</div>
      <table className="shortcut-table">
        <tbody>
          {SHORTCUTS.map((sc) => (
            <tr key={`${sc.id}-${sc.combo}`}>
              <th scope="row">{t(`shortcut.${sc.id}` as TKey)}</th>
              <td><kbd>{sc.combo}</kbd></td>
              <td className="sc-scope">{sc.scope === 'canvas' ? t('a11y.scopeCanvas') : t('a11y.scopeGlobal')}</td>
            </tr>
          ))}
        </tbody>
      </table>
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

// ---- AI agent ----

/**
 * Standing notes that apply to every board this person owns.
 *
 * Board rules already existed and remain the right default — most conventions
 * belong to a board, not a person. But a rule that is true everywhere had to be
 * retyped on every board, so in practice it was written nowhere.
 */
function AgentTab() {
  const { settings, update } = useSettings();
  const [draft, setDraft] = useState(settings.agent.instructions);

  return (
    <Row label={t('agentprefs.instructions')} sub={t('agentprefs.instructionsSub')}>
      <textarea
        className="sr-textarea"
        dir="auto"
        rows={5}
        value={draft}
        placeholder={t('agentprefs.placeholder')}
        onChange={(e) => setDraft(e.target.value)}
        // Committed on blur rather than per keystroke: this is prose, and the
        // debounced PATCH would otherwise write a dozen half-sentences.
        onBlur={() => {
          if (draft !== settings.agent.instructions) update({ agent: { instructions: draft } });
        }}
      />
    </Row>
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
      toast.error(t('toast.exportFailed'));
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
