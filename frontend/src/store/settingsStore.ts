// Settings store: hydrated from /me on boot, optimistically merged on every
// change, persisted with a debounced PATCH (absent fields keep their server
// value — the backend merges). All DOM side effects (theme, accent, language,
// density) funnel through applySideEffects so boot and updates share one path.
import { create } from 'zustand';
import { api, type DeepPartial } from '../api/client';
import { DEFAULT_SETTINGS, type User, type UserSettings } from '../api/types';
import { setLanguage, t } from '../i18n';
import { toast } from '../components/ui/Toaster';

type SaveState = 'idle' | 'saving' | 'saved' | 'error';

interface SettingsState {
  settings: UserSettings;
  saveState: SaveState;
  /** The signed-in user's subject. Needed to tell your own writes from a
   *  collaborator's when both arrive over the same socket. */
  sub: string;
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

// Guarded like the three below it, and for the reason matchOrNull's own comment
// gives: jsdom has no matchMedia at all. This line predated that helper and was
// left bare, so the moment anything in the settings store became a dependency of
// a component under test, the whole test FILE failed to load — not one
// assertion, the import.
const systemDark = matchOrNull('(prefers-color-scheme: dark)');

// AX19/AX30. The only matchMedia in the whole product was the one above, for
// the OS colour scheme — while nine JS-driven GSAP animations, including the
// 0.55s pan-and-zoom the agent fires on EVERY plan arrival, read no media query
// at all. The team plainly knew the query (prefers-reduced-motion appears twice
// in CSS), which made it a scoping error rather than an unknown: the two worst
// offenders are viewport transforms driven from JS, which CSS can never reach.
//
// So the OS signals are resolved here, next to the theme, and stamped onto the
// root as attributes — the same channel dot grids already use. `system` means
// ask the OS; an explicit value overrides it, because an OS-level preference is
// a good default and a bad prison.
const systemMotion = matchOrNull('(prefers-reduced-motion: reduce)');
const systemContrast = matchOrNull('(prefers-contrast: more)');
const systemTransparency = matchOrNull('(prefers-reduced-transparency: reduce)');

/** matchMedia for a query the browser may not know: an unsupported query
 *  reports `matches: false`, which is the right answer here — no signal is not
 *  a signal to reduce. jsdom has no matchMedia at all in some harnesses. */
function matchOrNull(query: string): MediaQueryList | null {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return null;
  try { return window.matchMedia(query); } catch { return null; }
}

/**
 * AX30's persistence, and why it is not simply the settings PATCH.
 *
 * The server's typed decode drops fields its struct does not have, and the
 * normalized response is adopted wholesale six hundred milliseconds later — the
 * exact mechanism that made the agent consent card reappear after being
 * dismissed. Until `domain.UserSettings` grows an `Accessibility` block, a
 * PATCH round trip would silently reset a person's motion preference.
 *
 * A local mirror is therefore not a stopgap for the browser's benefit; it is
 * what keeps the setting from being undone by its own save. It is read back
 * over whatever the server returns, so the day the server does store it the
 * mirror simply agrees. Motion sensitivity is not a preference to lose.
 */
const A11Y_KEY = 'qomra.accessibility';

function readMirror(): Partial<UserSettings['accessibility']> {
  try {
    const raw = localStorage.getItem(A11Y_KEY);
    return raw ? (JSON.parse(raw) as Partial<UserSettings['accessibility']>) : {};
  } catch { return {}; }
}

function writeMirror(a: UserSettings['accessibility']) {
  try { localStorage.setItem(A11Y_KEY, JSON.stringify(a)); } catch { /* private mode */ }
}

/** Overlay the local mirror onto a settings object the server just handed us. */
function withMirroredA11y(s: UserSettings): UserSettings {
  const mirror = readMirror();
  if (!Object.keys(mirror).length) return s;
  return { ...s, accessibility: { ...DEFAULT_SETTINGS.accessibility, ...s.accessibility, ...mirror } };
}

/** Whether motion should be suppressed right now, for the JS tween wrapper.
 *  Exported rather than read off the DOM by callers so there is one answer. */
export function motionReduced(): boolean {
  const pref = useSettings.getState().settings.accessibility?.motion ?? 'system';
  if (pref === 'reduced') return true;
  if (pref === 'full') return false;
  return !!systemMotion?.matches;
}

/** The announcement budget AX5's live regions spend. */
export function announceVerbosity(): UserSettings['accessibility']['announceVerbosity'] {
  return useSettings.getState().settings.accessibility?.announceVerbosity ?? 'normal';
}

/** The document direction a UI language implies. */
export function dirFor(lang: UserSettings['localization']['language']): 'rtl' | 'ltr' {
  return lang === 'ar' ? 'rtl' : 'ltr';
}

/** Whether the chrome is currently mirrored. Read by the few places that
 *  animate along the inline axis and therefore cannot express themselves in
 *  CSS logical properties — a GSAP tween takes a number, not a direction. */
export function isRTL(): boolean {
  return typeof document !== 'undefined' && document.documentElement.getAttribute('dir') === 'rtl';
}

function resolveTheme(pref: UserSettings['appearance']['theme']): 'light' | 'dark' {
  if (pref === 'system') return systemDark?.matches ? 'dark' : 'light';
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
  // Direction, not just language.
  //
  // Full `ar` translations shipped into a layout that never mirrored: topbar,
  // tool rail, breadcrumbs, agent bar, review list, outcome card and ghost
  // badges all stayed left-to-right, because `setLanguage` stamped <html lang>
  // and nothing anywhere wrote `dir`. Per-element content was beautifully
  // bidi-aware; the chrome around it was not. For an Arabic-first user, an
  // Arabic agent answering in Arabic inside an unmirrored dock is the single
  // most visible incompleteness in the product.
  //
  // The canvas is deliberately NOT part of this: `.canvas-layer` pins
  // `direction: ltr` in global.css, because a card's position is an absolute
  // board coordinate and mirroring it would move everyone's boards.
  root.setAttribute('dir', dirFor(s.localization.language));
  root.setAttribute('data-theme', resolveTheme(s.appearance.theme));
  root.setAttribute('data-density', s.appearance.uiDensity);
  root.setAttribute('data-dotgrid', s.appearance.dotGrid ? 'on' : 'off');
  root.setAttribute('data-shadows', s.appearance.cardShadows ? 'on' : 'off');

  // AX30's four accessibility channels, resolved OS-signal-then-override and
  // stamped as attributes so CSS and the tween wrapper read one answer.
  const a11y = { ...DEFAULT_SETTINGS.accessibility, ...(s.accessibility ?? {}) };
  const motion = a11y.motion === 'system' ? (systemMotion?.matches ? 'reduced' : 'full') : a11y.motion;
  const contrast = a11y.contrast === 'system' ? (systemContrast?.matches ? 'more' : 'normal') : a11y.contrast;
  const transparency = a11y.transparency === 'system'
    ? (systemTransparency?.matches ? 'reduced' : 'full')
    : a11y.transparency;
  root.setAttribute('data-motion', motion);
  root.setAttribute('data-contrast', contrast);
  root.setAttribute('data-transparency', transparency);
  root.setAttribute('data-textscale', String(a11y.textScale));
  root.style.setProperty('--accent', s.appearance.accentColor);
  root.style.setProperty('--accent-deep', shade(s.appearance.accentColor, -34));
  root.style.setProperty('--accent-rgb', hexToRgb(s.appearance.accentColor));
  root.style.setProperty('--accent-tint', `rgba(${hexToRgb(s.appearance.accentColor)}, 0.1)`);
  setLanguage(s.localization.language);
}

// Re-resolve when the OS theme flips and the user follows the system.
systemDark?.addEventListener('change', () => {
  const s = useSettings.getState().settings;
  if (s.appearance.theme === 'system') applySideEffects(s);
});

// The same for the three accessibility signals. A person who turns on Reduce
// Motion in the OS while the app is open has said something about right now,
// not about the next reload.
for (const mq of [systemMotion, systemContrast, systemTransparency]) {
  mq?.addEventListener?.('change', () => applySideEffects(useSettings.getState().settings));
}

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
      // Adopt the server's normalized copy unless newer edits are in flight —
      // with the accessibility mirror read back over it, because a decode that
      // does not know the field returns it absent and "absent" would undo a
      // motion preference the person set four hundred milliseconds ago.
      if (!flushTimer) {
        const merged = withMirroredA11y(server);
        useSettings.setState({ settings: merged, saveState: 'saved' });
        applySideEffects(merged);
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
      toast.error(t('toast.settingsFailed'));
    }
  }, 600);
}

export const useSettings = create<SettingsState>((set, get) => ({
  // The mirror is read at construction, not at hydrate: /me is a round trip and
  // the first camera tween can fire before it lands.
  settings: withMirroredA11y(DEFAULT_SETTINGS),
  saveState: 'idle',
  sub: '',

  hydrate(user: User) {
    const base = user.settings
      ? deepMerge(DEFAULT_SETTINGS, user.settings as DeepPartial<UserSettings>)
      : DEFAULT_SETTINGS;
    const settings = withMirroredA11y(base);
    set({ settings, saveState: 'idle', sub: user.keycloakSub });
    applySideEffects(settings);
  },

  update(patch) {
    const settings = deepMerge(get().settings, patch);
    set({ settings });
    if (patch.accessibility) writeMirror(settings.accessibility);
    applySideEffects(settings);
    queuePatch(patch);
  },
}));

// Convenience selectors used across the app.
export const usePrefs = () => useSettings((s) => s.settings.preferences);
export const useAppearance = () => useSettings((s) => s.settings.appearance);
export const useLocalization = () => useSettings((s) => s.settings.localization);
