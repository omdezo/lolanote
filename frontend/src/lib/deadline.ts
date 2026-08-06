// JN21, as one mechanism rather than four.
//
// The product makes four independent recovery promises with four different
// lifetimes — trash retention (90 days), the proposal window, the delegation
// grant, and a share link — and it showed NONE of them to the person the
// promise was made to. The trash window was stated in exactly one place: inside
// the empty-state block, so the sentence "deleted items are kept for 3 months"
// rendered only when there was nothing left to lose. Every actual row showed
// the date it was deleted, which is the one date nobody needs.
//
// The law, stated once here so it can be obeyed in four places: ANY PROMISE
// WITH A LIFETIME MUST RENDER ITS LIFETIME AT THE SURFACE THAT MAKES THE
// PROMISE. The agent's own digest already holds itself to exactly this — every
// cap elides visibly, "… 12 more (read_board for the rest)" — on the stated
// grounds that silent elision makes it look like instructions were ignored.
// The product applied that rule to the model and to nobody else.
import type { TKey } from '../i18n';

/**
 * How much of a promise is left, in whole days / hours / minutes, with the
 * unit chosen the way a person would say it out loud. Returns null when there
 * is no deadline to state — a caller must render nothing rather than "0".
 */
export interface Remaining {
  ms: number;
  /** The i18n key for the phrase, e.g. 'deadline.days'. */
  key: TKey;
  /** The number that goes in it. */
  n: number;
  /** How close it is. Drives colour, and only colour — the words say it too. */
  urgency: 'calm' | 'soon' | 'critical';
}

/** A day, in milliseconds. Named because `86400000` is not a duration. */
const DAY = 24 * 60 * 60 * 1000;
const HOUR = 60 * 60 * 1000;
const MINUTE = 60 * 1000;

/**
 * DL17's hard clause, honoured where it can be and admitted where it cannot.
 *
 * The countdown MUST be the server's arithmetic — `domain.TrashRetention` is
 * 90 days, the old UI string said "3 months", and those differ by up to a day
 * at the boundary, which is a whole day of someone believing a sequence is
 * still recoverable. So `purgeAt` (server-computed) is used whenever the
 * server sends it. It does not send it yet, so until it does this falls back
 * to the same constant, in ONE place, named, with the drift risk written down
 * rather than sprinkled through a component as `90`.
 */
export const FALLBACK_TRASH_RETENTION_DAYS = 90;

/** When a trashed item stops being recoverable. Server's answer if we have it. */
export function purgeDeadline(item: { purgeAt?: string; element: { deletedAt?: string | null } }): Date | null {
  if (item.purgeAt) return new Date(item.purgeAt);
  if (!item.element.deletedAt) return null;
  return new Date(new Date(item.element.deletedAt).getTime() + FALLBACK_TRASH_RETENTION_DAYS * DAY);
}

/**
 * The remaining life of a promise, or null if there isn't one.
 *
 * `soon` and `critical` thresholds scale with the unit because "7 days left"
 * and "7 minutes left" are not the same amount of alarm: a week is time to act
 * on a deleted sequence, seven minutes is not time to review forty staged
 * actions.
 */
export function remaining(deadline: Date | string | null | undefined, now = Date.now()): Remaining | null {
  if (!deadline) return null;
  const at = typeof deadline === 'string' ? new Date(deadline) : deadline;
  const ms = at.getTime() - now;
  if (Number.isNaN(ms)) return null;
  if (ms <= 0) return { ms, key: 'deadline.expired', n: 0, urgency: 'critical' };

  if (ms >= DAY) {
    const n = Math.floor(ms / DAY);
    return { ms, key: n === 1 ? 'deadline.day' : 'deadline.days', n, urgency: n <= 7 ? 'soon' : 'calm' };
  }
  if (ms >= HOUR) {
    const n = Math.floor(ms / HOUR);
    return { ms, key: n === 1 ? 'deadline.hour' : 'deadline.hours', n, urgency: 'soon' };
  }
  const n = Math.max(1, Math.floor(ms / MINUTE));
  return { ms, key: n === 1 ? 'deadline.minute' : 'deadline.minutes', n, urgency: ms <= 10 * MINUTE ? 'critical' : 'soon' };
}

/**
 * Whether a deadline is close enough to be worth saying.
 *
 * A proposal that expires in twenty-nine minutes does not need a clock on it;
 * one that expires in six does, because the person is mid-review and about to
 * lose the review. Stating it early is how a deadline becomes furniture people
 * stop reading.
 */
/** The phrase, in the reader's language. `.replace('{n}', …)` is the house
 *  interpolation; this exists so four surfaces cannot spell it four ways. */
export function deadlineLabel(r: Remaining, t: (k: TKey) => string): string {
  return t(r.key).replace('{n}', String(r.n));
}

export function withinWarning(r: Remaining | null, warnWithinMs: number): boolean {
  return !!r && r.ms <= warnWithinMs;
}

export const TEN_MINUTES = 10 * MINUTE;
export const SEVEN_DAYS = 7 * DAY;
