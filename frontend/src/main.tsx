import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import { initSmartDigits } from './lib/direction';
import { initKeyboardInset } from './lib/keyboardInset';
import './styles/global.css';
import './styles/settings.css';
import './styles/agent.css';
import './styles/agent-bar.css';

// Typing digits inside Arabic text produces Arabic-Indic numerals — one
// native beforeinput listener covers every plain text field in the app.
initSmartDigits();

// Keep --kb-inset current so the agent bar, and everything anchored to the
// bottom edge with it, stays above the software keyboard instead of behind it.
initKeyboardInset();

// Installable PWA: register the app-shell service worker (production only —
// it would fight Vite's dev server).
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    // The build id travels in the query so the worker can name its shell cache
    // off it: a shell entry poisoned by a captive portal or a 502 mid-deploy
    // must not be inherited by the next deploy. Unset in an ordinary build, in
    // which case the worker keeps the name it has always had.
    const build = (import.meta.env.VITE_BUILD_ID as string | undefined) || '';
    navigator.serviceWorker
      .register(build ? `/sw.js?v=${encodeURIComponent(build)}` : '/sw.js')
      .catch(() => undefined);
  });
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
