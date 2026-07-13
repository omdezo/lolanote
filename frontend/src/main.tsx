import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import { initSmartDigits } from './lib/direction';
import './styles/global.css';
import './styles/settings.css';

// Typing digits inside Arabic text produces Arabic-Indic numerals — one
// native beforeinput listener covers every plain text field in the app.
initSmartDigits();

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
