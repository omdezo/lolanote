// Keycloak OIDC (PKCE) integration. The adapter owns login redirects and
// silent token refresh; the rest of the app only ever asks for a fresh token.
// Tokens live in memory only (never storage). Refreshes are single-flight,
// proactive (onTokenExpired + interval), and a dead session surfaces as one
// polite toast before the re-login redirect instead of a hard failure.
import Keycloak from 'keycloak-js';
import { toast } from '../components/ui/Toaster';
import { clearLocalData } from '../lib/boardCache';

// Same-origin by default. The served bundle is baked at build time, so a
// hard-coded host is wrong the moment the app is reached by anything other
// than the machine that built it — a phone on the LAN, a tunnel, a real
// domain. nginx proxies /realms and /resources to Keycloak, so the browser
// talks to exactly one origin and the issuer in the token matches the address
// the user typed. Set VITE_KEYCLOAK_URL only when Keycloak lives elsewhere.
const keycloak = new Keycloak({
  url: (import.meta.env.VITE_KEYCLOAK_URL as string)
    || (typeof window !== 'undefined' ? window.location.origin : 'http://localhost:8081'),
  realm: (import.meta.env.VITE_KEYCLOAK_REALM as string) || 'qomranote',
  clientId: (import.meta.env.VITE_KEYCLOAK_CLIENT_ID as string) || 'qomranote-web',
});

let initFlight: Promise<boolean> | null = null;

// initAuth boots Keycloak. In 'required' mode it redirects to login; in
// 'optional' mode (used when opening a public share link) it silently checks
// for an existing session without forcing login, so anonymous visitors can
// still view account-free links (§6.1 mechanism 4).
//
// Single-flight, for the same reason refresh() below is: a second caller must
// await the SAME init rather than race past it. A boolean "already started"
// guard returned `authenticated === false` while the first init was still in
// flight, so the caller went on to make a tokenless API call and the app hung
// on the splash screen forever. React's StrictMode double-invokes effects in
// development, which made that the normal path on the dev server.
export function initAuth(mode: 'required' | 'optional' = 'required'): Promise<boolean> {
  if (initFlight) return initFlight;
  initFlight = keycloak
    .init({
      onLoad: mode === 'required' ? 'login-required' : 'check-sso',
      pkceMethod: 'S256',
      checkLoginIframe: false,
    })
    .then((ok) => {
      if (ok) {
        // Two safety nets keep the token fresh: the adapter's expiry callback
        // (fires ~when the access token lapses) and a slow heartbeat that
        // renews anything inside the 90-second window. Both share one flight.
        keycloak.onTokenExpired = () => { void refresh(90); };
        setInterval(() => { void refresh(90); }, 60_000);
      }
      return ok;
    });
  return initFlight;
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

/**
 * Sign out, and take the local copy with you.
 *
 * Tokens have always lived in memory only — and the boards did not. The
 * IndexedDB mirror kept up to fourteen days of full board content, and the
 * function written to clear it was never called from anywhere. Awaited before
 * the redirect, because the navigation kills the transaction otherwise, and the
 * whole point is that it has finished before the next person sits down.
 */
export async function logout(): Promise<void> {
  await clearLocalData();
  keycloak.logout({ redirectUri: window.location.origin });
}

// The same residue survives a sign-out that happened somewhere else — another
// tab, another device, "sign me out everywhere", an idle timeout. Those paths
// never reach logout() above, so the clear is hung on the adapter's own event
// as well: whichever way the session ends, the device is left clean.
keycloak.onAuthLogout = () => { void clearLocalData(); };

export function currentSub(): string {
  return keycloak.subject ?? '';
}

export function currentName(): string {
  return (keycloak.tokenParsed?.name as string) || (keycloak.tokenParsed?.preferred_username as string) || '';
}
