import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import { initSmartDigits } from './lib/direction';
import './styles/global.css';
import './styles/settings.css';

// Typing digits inside Arabic text produces Arabic-Indic numerals — one
// native beforeinput listener covers every plain text field in the app.
initSmartDigits();

// Installable PWA: register the app-shell service worker (production only —
// it would fight Vite's dev server).
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => undefined);
  });
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
