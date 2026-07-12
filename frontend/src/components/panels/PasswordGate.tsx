// Password gate for password-protected view links (§6.1). Shown when
// resolving a shared link returns 401; on submit the password rides along as
// X-Share-Password and the resolve is retried.
import { useState } from 'react';

export function PasswordGate({ onSubmit }: { onSubmit: (password: string) => Promise<void> }) {
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!password) return;
    setBusy(true);
    setError('');
    try {
      await onSubmit(password);
    } catch {
      setError('Incorrect password. Try again.');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="boot-screen">
      <div className="boot-mark">Q</div>
      <div className="gate-card">
        <div className="gate-title">This board is password protected</div>
        <input
          className="search-input"
          type="password"
          autoFocus
          placeholder="Enter password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && void submit()}
        />
        {error && <div className="gate-error">{error}</div>}
        <button className="topbar-btn primary gate-submit" onClick={() => void submit()} disabled={busy}>
          {busy ? 'Checking…' : 'View board'}
        </button>
      </div>
    </div>
  );
}
