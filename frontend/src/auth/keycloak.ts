// Keycloak OIDC (PKCE) integration. The adapter owns login redirects and
// silent token refresh; the rest of the app only ever asks for a fresh token.
import Keycloak from 'keycloak-js';

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
    // Keep the token fresh in the background only when authenticated.
    setInterval(() => {
      keycloak.updateToken(60).catch(() => keycloak.login());
    }, 30_000);
  }
  return ok;
}

export function isAuthenticated(): boolean {
  return keycloak.authenticated ?? false;
}

export function login() {
  keycloak.login();
}

export async function getToken(): Promise<string> {
  // Anonymous (share-link) visitors have no session — return empty so the
  // API client sends no bearer and the optional-auth routes treat them as a
  // guest gated by the share token.
  if (!keycloak.authenticated) return '';
  try {
    await keycloak.updateToken(30);
  } catch {
    keycloak.login();
  }
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
