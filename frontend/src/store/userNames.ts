// Display-name cache: comment authors, task assignees, and mention pickers
// resolve Keycloak subs to names through POST /users/resolve, batched and
// memoized for the session.
import { create } from 'zustand';
import { api } from '../api/client';

export interface ResolvedUser {
  sub: string;
  name: string;
  avatarUrl?: string;
  email?: string;
}

interface UserNamesState {
  users: Record<string, ResolvedUser>;
  resolve(subs: string[]): Promise<void>;
}

let inFlight: Set<string> = new Set();

export const useUserNames = create<UserNamesState>((set, get) => ({
  users: {},

  async resolve(subs) {
    const missing = subs.filter((s) => s && !get().users[s] && !inFlight.has(s));
    if (missing.length === 0) return;
    missing.forEach((s) => inFlight.add(s));
    try {
      const resolved = await api.resolveUsers(missing);
      set((state) => ({
        users: {
          ...state.users,
          ...Object.fromEntries(resolved.map((u) => [u.sub, u])),
        },
      }));
    } catch {
      // Transient failure — allow a retry later.
    } finally {
      missing.forEach((s) => inFlight.delete(s));
    }
  },
}));

// nameOf renders a cached name (falls back to a truncated sub).
export function nameOf(sub: string): string {
  const u = useUserNames.getState().users[sub];
  return u?.name || sub.slice(0, 8);
}
