// Keycloak OIDC (PKCE) integration. The adapter owns login redirects and
// silent token refresh; the rest of the app only ever asks for a fresh token.
// Tokens live in memory only (never storage). Refreshes are single-flight,
// proactive (onTokenExpired + interval), and a dead session surfaces as one
// polite toast before the re-login redirect instead of a hard failure.
import Keycloak from 'keycloak-js';
import { toast } from '../components/ui/Toaster';

const keycloak = new Keycloak({
  url: (import.meta.env.VITE_KEYCLOAK_URL as string) || 'http://localhost:8081',
  realm: (import.meta.env.VITE_KEYCLOAK_REALM as string) || 'qomranote',
  clientId: (import.meta.env.VITE_KEYCLOAK_CLIENT_ID as string) || 'qomranote-web',
});

let initialized = false;

// initAuth boots Keycloak. In 'required' mode it redirects to login; in
// 'optional' mode (used when opening a public share link) it silently checks
// for an existing session without forcing login, so anonymous visitors can
// still view account-free links (§6.1 mechanism 4).
export async function initAuth(mode: 'required' | 'optional' = 'required'): Promise<boolean> {
  if (initialized) return keycloak.authenticated ?? false;
  initialized = true;
  const ok = await keycloak.init({
    onLoad: mode === 'required' ? 'login-required' : 'check-sso',
    pkceMethod: 'S256',
    checkLoginIframe: false,
  });
  if (ok) {
    // Two safety nets keep the token fresh: the adapter's expiry callback
    // (fires ~when the access token lapses) and a slow heartbeat that
    // renews anything inside the 90-second window. Both share one flight.
    keycloak.onTokenExpired = () => { void refresh(90); };
    setInterval(() => { void refresh(90); }, 60_000);
  }
  return ok;
}

// Single-flight refresh: concurrent callers (interval + API calls + expiry
// callback) await the same round-trip instead of racing the refresh token —
// racing matters now that the realm rotates refresh tokens on every use.
let inFlight: Promise<boolean> | null = null;

function refresh(minValidity: number): Promise<boolean> {
  if (!keycloak.authenticated) return Promise.resolve(false);
  if (!inFlight) {
    inFlight = keycloak
      .updateToken(minValidity)
      .then(() => true)
      .catch(() => {
        sessionExpired();
        return false;
      })
      .finally(() => { inFlight = null; });
  }
  return inFlight;
}

// sessionExpired runs once: the SSO session is gone (idle/max timeout or
// logout elsewhere), so tell the user and bounce through the branded login.
let expiredHandled = false;
function sessionExpired() {
  if (expiredHandled) return;
  expiredHandled = true;
  toast.info('Your session expired — taking you back to sign in…');
  setTimeout(() => keycloak.login(), 1400);
}

export function isAuthenticated(): boolean {
  return keycloak.authenticated ?? false;
}

export function login() {
  keycloak.login();
}

// getToken returns a token valid for at least ~45 seconds. Anonymous
// (share-link) visitors have no session — empty string means "no bearer",
// and the optional-auth routes treat them as guests gated by the share token.
export async function getToken(): Promise<string> {
  if (!keycloak.authenticated) return '';
  await refresh(45);
  return keycloak.token ?? '';
}

// forceRefreshToken discards the cached access token (a huge minValidity
// always triggers the refresh grant) — the API client uses it to retry
// exactly once when a request comes back 401.
export async function forceRefreshToken(): Promise<string> {
  if (!keycloak.authenticated) return '';
  await refresh(86_400);
  return keycloak.token ?? '';
}

export function logout() {
  keycloak.logout({ redirectUri: window.location.origin });
}

export function currentSub(): string {
  return keycloak.subject ?? '';
}

export function currentName(): string {
  return (keycloak.tokenParsed?.name as string) || (keycloak.tokenParsed?.preferred_username as string) || '';
}
