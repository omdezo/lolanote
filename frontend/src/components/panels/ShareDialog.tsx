// The Share dialog (§6.1) — all four mechanisms: invite editors by email,
// shareable editor link, read-only link with feedback, and the current
// editors list. Links copy to the clipboard with one click.
import { useEffect, useState } from 'react';
import { api } from '../../api/client';
import type { ShareState } from '../../api/types';
import { CloseIcon } from '../Icons';

interface Props { boardId: string; onClose: () => void }

export function ShareDialog({ boardId, onClose }: Props) {
  const [state, setState] = useState<ShareState | null>(null);
  const [email, setEmail] = useState('');
  const [error, setError] = useState('');
  const [copied, setCopied] = useState('');

  useEffect(() => {
    api.shareState(boardId).then(setState).catch((e) => setError(e.message));
  }, [boardId]);

  const invite = async () => {
    setError('');
    try {
      setState(await api.inviteEditor(boardId, email.trim()));
      setEmail('');
    } catch (e: any) {
      setError(e.status === 404 ? 'No QomraNote account with that email.' : e.message);
    }
  };

  const copy = async (url: string, tag: string) => {
    await navigator.clipboard.writeText(url);
    setCopied(tag);
    setTimeout(() => setCopied(''), 1500);
  };

  const shareUrl = (token: string) => `${window.location.origin}/?share=${token}&board=${boardId}`;

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head"><h3>Share this board</h3><button className="panel-close" onClick={onClose}><CloseIcon size={15} /></button></div>
        <div className="modal-body">
          {error && <div style={{ color: '#d63031', fontSize: 13, marginBottom: 10 }}>{error}</div>}

          <div className="share-section">
            <h4>Invite editors by email</h4>
            <div className="share-row">
              <input
                placeholder="name@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && void invite()}
              />
              <button className="pi-btn" onClick={() => void invite()}>Invite</button>
            </div>
            {state?.editors.map((sub) => (
              <div key={sub} className="editor-row">
                <span style={{ fontFamily: 'Consolas, monospace', fontSize: 12 }}>{sub}</span>
                <button
                  className="pi-btn danger"
                  onClick={async () => setState(await api.removeEditor(boardId, sub))}
                >
                  Remove
                </button>
              </div>
            ))}
            <div style={{ fontSize: 12, color: '#9a97a5', marginTop: 4 }}>
              Sharing cascades to every nested sub-board automatically.
            </div>
          </div>

          <div className="share-section">
            <h4>Editor link — any signed-in user with the URL can edit</h4>
            {state?.publicEditLink ? (
              <>
                <div className="share-link-box">{shareUrl(state.publicEditLink)}</div>
                <div className="pi-actions">
                  <button className="pi-btn" onClick={() => void copy(shareUrl(state.publicEditLink!), 'edit')}>
                    {copied === 'edit' ? 'Copied!' : 'Copy link'}
                  </button>
                  <button className="pi-btn danger" onClick={async () => setState(await api.revokeShareLink(boardId, 'edit'))}>
                    Disable
                  </button>
                </div>
              </>
            ) : (
              <button className="pi-btn" onClick={async () => setState(await api.createShareLink(boardId, { kind: 'edit' }))}>
                Create editor link
              </button>
            )}
          </div>

          <div className="share-section">
            <h4>Read-only link — viewers can comment &amp; react</h4>
            {state?.viewLink ? (
              <>
                <div className="share-link-box">{shareUrl(state.viewLink.token)}</div>
                <div className="pi-actions">
                  <button className="pi-btn" onClick={() => void copy(shareUrl(state.viewLink!.token), 'view')}>
                    {copied === 'view' ? 'Copied!' : 'Copy link'}
                  </button>
                  <button className="pi-btn danger" onClick={async () => setState(await api.revokeShareLink(boardId, 'view'))}>
                    Disable
                  </button>
                </div>
              </>
            ) : (
              <button
                className="pi-btn"
                onClick={async () => setState(await api.createShareLink(boardId, { kind: 'view', allowFeedback: true }))}
              >
                Create read-only link
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
