// Settings store: hydrated from /me on boot, optimistically merged on every
// change, persisted with a debounced PATCH (absent fields keep their server
// value — the backend merges). All DOM side effects (theme, accent, language,
// density) funnel through applySideEffects so boot and updates share one path.
import { create } from 'zustand';
import { api, type DeepPartial } from '../api/client';
import { DEFAULT_SETTINGS, type User, type UserSettings } from '../api/types';
import { setLanguage } from '../i18n';
import { toast } from '../components/ui/Toaster';

type SaveState = 'idle' | 'saving' | 'saved' | 'error';

interface SettingsState {
  settings: UserSettings;
  saveState: SaveState;
  hydrate(user: User): void;
  update(patch: DeepPartial<UserSettings>): void;
}

// deepMerge overlays patch onto base without mutating either. Arrays replace.
function deepMerge<T>(base: T, patch: DeepPartial<T>): T {
  const out: any = Array.isArray(base) ? [...(base as any)] : { ...base };
  for (const key of Object.keys(patch as object)) {
    const pv = (patch as any)[key];
    const bv = (base as any)[key];
    out[key] =
      pv !== null && typeof pv === 'object' && !Array.isArray(pv) && bv && typeof bv === 'object'
        ? deepMerge(bv, pv)
        : pv;
  }
  return out;
}

// ---- DOM side effects ----

const systemDark = window.matchMedia('(prefers-color-scheme: dark)');

function resolveTheme(pref: UserSettings['appearance']['theme']): 'light' | 'dark' {
  if (pref === 'system') return systemDark.matches ? 'dark' : 'light';
  return pref;
}

// shade darkens (negative) or lightens (positive) a #rrggbb color.
function shade(hex: string, amount: number): string {
  const m = /^#([0-9a-f]{6})$/i.exec(hex);
  if (!m) return hex;
  const n = parseInt(m[1], 16);
  const ch = (v: number) => Math.max(0, Math.min(255, Math.round(v + amount)));
  const r = ch((n >> 16) & 255), g = ch((n >> 8) & 255), b = ch(n & 255);
  return `#${((r << 16) | (g << 8) | b).toString(16).padStart(6, '0')}`;
}

function hexToRgb(hex: string): string {
  const m = /^#([0-9a-f]{6})$/i.exec(hex);
  if (!m) return '94, 92, 230';
  const n = parseInt(m[1], 16);
  return `${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}`;
}

function applySideEffects(s: UserSettings) {
  const root = document.documentElement;
  root.setAttribute('data-theme', resolveTheme(s.appearance.theme));
  root.setAttribute('data-density', s.appearance.uiDensity);
  root.setAttribute('data-dotgrid', s.appearance.dotGrid ? 'on' : 'off');
  root.setAttribute('data-shadows', s.appearance.cardShadows ? 'on' : 'off');
  root.style.setProperty('--accent', s.appearance.accentColor);
  root.style.setProperty('--accent-deep', shade(s.appearance.accentColor, -34));
  root.style.setProperty('--accent-rgb', hexToRgb(s.appearance.accentColor));
  root.style.setProperty('--accent-tint', `rgba(${hexToRgb(s.appearance.accentColor)}, 0.1)`);
  setLanguage(s.localization.language);
}

// Re-resolve when the OS theme flips and the user follows the system.
systemDark.addEventListener('change', () => {
  const s = useSettings.getState().settings;
  if (s.appearance.theme === 'system') applySideEffects(s);
});

// ---- debounced persistence ----

let pendingPatch: DeepPartial<UserSettings> = {};
let flushTimer: ReturnType<typeof setTimeout> | null = null;

function queuePatch(patch: DeepPartial<UserSettings>) {
  pendingPatch = deepMerge(pendingPatch as any, patch as any);
  if (flushTimer) clearTimeout(flushTimer);
  useSettings.setState({ saveState: 'saving' });
  flushTimer = setTimeout(async () => {
    const body = pendingPatch;
    pendingPatch = {};
    flushTimer = null;
    try {
      const server = await api.updateSettings(body);
      // Adopt the server's normalized copy unless newer edits are in flight.
      if (!flushTimer) {
        useSettings.setState({ settings: server, saveState: 'saved' });
        applySideEffects(server);
      }
      // Presence visibility is decided at the WS handshake — reconnect so
      // the change applies immediately instead of on the next board open.
      if (body.privacy && 'showPresence' in body.privacy) {
        const { connectBoard } = await import('../realtime/socket');
        const { boardId } = (await import('./boardStore')).useBoard.getState();
        if (boardId) void connectBoard(boardId);
      }
    } catch {
      useSettings.setState({ saveState: 'error' });
      toast.error('Could not save settings');
    }
  }, 600);
}

export const useSettings = create<SettingsState>((set, get) => ({
  settings: DEFAULT_SETTINGS,
  saveState: 'idle',

  hydrate(user: User) {
    const settings = user.settings
      ? deepMerge(DEFAULT_SETTINGS, user.settings as DeepPartial<UserSettings>)
      : DEFAULT_SETTINGS;
    set({ settings, saveState: 'idle' });
    applySideEffects(settings);
  },

  update(patch) {
    const settings = deepMerge(get().settings, patch);
    set({ settings });
    applySideEffects(settings);
    queuePatch(patch);
  },
}));

// Convenience selectors used across the app.
export const usePrefs = () => useSettings((s) => s.settings.preferences);
export const useAppearance = () => useSettings((s) => s.settings.appearance);
export const useLocalization = () => useSettings((s) => s.settings.localization);
