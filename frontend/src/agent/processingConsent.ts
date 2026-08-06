// Whether this person has been told that board content leaves the deployment.
//
// Two records, deliberately, and the local one is not a convenience.
// `PATCH /me/settings` decodes the patch into the typed settings struct and
// returns its own normalized copy, so a field the server does not yet know
// about is dropped on the way in AND overwritten on the way back — which would
// have made the consent card reappear about half a second after the person
// dismissed it, forever. The account stamp is the durable, cross-device record
// and is what should win once the backend carries the field; this is what makes
// the question get asked once on this machine in the meantime.
//
// Keyed by subject: a shared browser must not let one person's answer speak for
// the next one who signs in.

const KEY = 'qomra.agent.processingAck';

function store(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    // Private mode, or a browser with storage disabled. The question gets asked
    // again next session, which is the safe direction to fail in.
    return null;
  }
}

export function localAcknowledgement(sub: string): string {
  if (!sub) return '';
  return store()?.getItem(`${KEY}:${sub}`) ?? '';
}

export function recordLocalAcknowledgement(sub: string, at: string): void {
  if (!sub) return;
  try { store()?.setItem(`${KEY}:${sub}`, at); } catch { /* nothing to do */ }
}
