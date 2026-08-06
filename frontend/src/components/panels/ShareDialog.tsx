// The Share dialog (§6.1) — all four mechanisms: invite editors by email,
// shareable editor link, read-only link with feedback, and the current
// editors list. Links copy to the clipboard with one click.
import { useEffect, useState } from 'react';
import { api } from '../../api/client';
import type { ShareState } from '../../api/types';
import { useT } from '../../i18n';
import { useUserNames, nameOf } from '../../store/userNames';
import { useSettings } from '../../store/settingsStore';
import { Modal } from '../ui/Modal';
import { CloseIcon } from '../Icons';

interface Props { boardId: string; onClose: () => void }

export function ShareDialog({ boardId, onClose }: Props) {
  const t = useT();
  const [state, setState] = useState<ShareState | null>(null);
  const [email, setEmail] = useState('');
  const [error, setError] = useState('');
  const [copied, setCopied] = useState('');
  // JN13. A lookup miss used to be a flat refusal with nowhere to go. This is
  // the email it missed on, kept so the dialog can say something useful about
  // that specific person instead of a generic sentence.
  const [noAccount, setNoAccount] = useState('');
  // Re-render as names arrive.
  useUserNames((s) => s.users);
  const mySub = useSettings((s) => s.sub);

  useEffect(() => {
    api.shareState(boardId).then(setState).catch((e) => setError(e.message));
  }, [boardId]);

  /**
   * JN14. This list used to render `f47ac10b-58cc-4372-a567-0e02b2c3d479` in
   * Consolas next to a red Remove button, on the one screen where a person
   * decides who has access to their film. With two collaborators, revoking the
   * wrong one was a coin flip; with four the dialog was unusable and the only
   * recourse was to remove everyone and re-invite.
   *
   * The resolver was two files away, hydrated and memoised, already used by
   * comment bubbles and task assignees. Not a missing capability — an unwired
   * one, in the highest-consequence panel in the app.
   */
  useEffect(() => {
    const subs = [state?.ownerId, ...(state?.editors ?? [])].filter(Boolean) as string[];
    if (subs.length) void useUserNames.getState().resolve(subs);
  }, [state]);

  const invite = async () => {
    setError('');
    setNoAccount('');
    const addr = email.trim();
    try {
      setState(await api.inviteEditor(boardId, addr));
      setEmail('');
    } catch (e: any) {
      if (e.status === 404) setNoAccount(addr);
      else setError(e.message);
    }
  };

  const copy = async (url: string, tag: string) => {
    await navigator.clipboard.writeText(url);
    setCopied(tag);
    setTimeout(() => setCopied(''), 1500);
  };

  const shareUrl = (token: string) => `${window.location.origin}/?share=${token}&board=${boardId}`;

  return (
    <Modal
      title={t('topbar.shareThis')}
      overlayId="share-dialog"
      onClose={onClose}
      headExtra={<button className="panel-close" onClick={onClose} aria-label={t('common.close')} title={t('common.close')}><CloseIcon size={15} /></button>}
    >
        <div className="modal-body">
          {error && <div className="share-error" role="alert">{error}</div>}

          <div className="share-section">
            <h4>{t('share.inviteHead')}</h4>
            <div className="share-row">
              <input
                aria-label={t('share.inviteHead')}
                placeholder={t('share.emailPlaceholder')}
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && void invite()}
              />
              <button className="pi-btn" onClick={() => void invite()}>{t('share.invite')}</button>
            </div>
            {/* JN13. "No QomraNote account with that email." was a dead end:
                journey 5 is "a producer joins mid-project", and not having an
                account is what JOINING means. The owner typed the address, got
                a flat refusal, and the only path forward was out of band —
                message them, tell them to sign up, wait, ask which address
                they used, try again. Outbound email does not exist anywhere in
                this deployment, so the honest minimum is to say what happened,
                say what has to happen next, and hand over something sendable —
                labelled as exactly what it is, because an editor link is a
                bearer token and calling it an invitation would be a lie. */}
            {noAccount && (
              <div className="share-noaccount" role="status">
                <strong>{t('share.noAccount')}</strong>
                <p>{t('share.noAccountNext').replace('{email}', noAccount)}</p>
                {state?.publicEditLink ? (
                  <button className="pi-btn" onClick={() => void copy(shareUrl(state.publicEditLink!), 'invite')}>
                    {copied === 'invite' ? t('share.copied') : t('share.copyEditLink')}
                  </button>
                ) : (
                  <button className="pi-btn" onClick={async () => setState(await api.createShareLink(boardId, { kind: 'edit' }))}>
                    {t('share.createEdit')}
                  </button>
                )}
                <p className="share-caveat">{t('share.linkCaveat')}</p>
              </div>
            )}
            {state?.editors.map((sub) => (
              <div key={sub} className="editor-row">
                <span className="editor-name">
                  {nameOf(sub)}
                  {sub === mySub && <em className="editor-you"> — {t('share.you')}</em>}
                </span>
                <button
                  className="pi-btn danger"
                  aria-label={`${t('share.remove')} ${nameOf(sub)}`}
                  onClick={async () => setState(await api.removeEditor(boardId, sub))}
                >
                  {t('share.remove')}
                </button>
              </div>
            ))}
            <div className="share-note">
              {t('share.cascades')}
            </div>
          </div>

          <div className="share-section">
            <h4>{t('share.editHead')}</h4>
            {state?.publicEditLink ? (
              <>
                <div className="share-link-box">{shareUrl(state.publicEditLink)}</div>
                <div className="pi-actions">
                  <button className="pi-btn" onClick={() => void copy(shareUrl(state.publicEditLink!), 'edit')}>
                    {copied === 'edit' ? t('share.copied') : t('share.copy')}
                  </button>
                  <button className="pi-btn danger" onClick={async () => setState(await api.revokeShareLink(boardId, 'edit'))}>
                    {t('share.disable')}
                  </button>
                </div>
              </>
            ) : (
              <button className="pi-btn" onClick={async () => setState(await api.createShareLink(boardId, { kind: 'edit' }))}>
                {t('share.createEdit')}
              </button>
            )}
          </div>

          <div className="share-section">
            <h4>{t('share.viewHead')}</h4>
            {state?.viewLink ? (
              <>
                <div className="share-link-box">{shareUrl(state.viewLink.token)}</div>
                <div className="pi-actions">
                  <button className="pi-btn" onClick={() => void copy(shareUrl(state.viewLink!.token), 'view')}>
                    {copied === 'view' ? t('share.copied') : t('share.copy')}
                  </button>
                  <button className="pi-btn danger" onClick={async () => setState(await api.revokeShareLink(boardId, 'view'))}>
                    {t('share.disable')}
                  </button>
                </div>
              </>
            ) : (
              <button
                className="pi-btn"
                onClick={async () => setState(await api.createShareLink(boardId, { kind: 'view', allowFeedback: true }))}
              >
                {t('share.createView')}
              </button>
            )}
          </div>
        </div>
    </Modal>
  );
}
