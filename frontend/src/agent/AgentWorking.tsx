// Working — the plan writes itself.
//
// Staged changes tick past as they are decided, so the wait is spent watching
// the plan form rather than watching a spinner.
import { useEffect, useState } from 'react';
import { useT, type TKey } from '../i18n';
import type { AgentEvent } from '../api/types';
import { kindLabel, useAgent } from './agentStore';

/**
 * What the agent is doing right now, in one clause.
 *
 * The server's tool names are the ground truth and they are not translated;
 * this maps them to a phrase key. Anything unrecognized falls to "Thinking…",
 * which is honest — the alternative is showing a raw tool name to a person who
 * has never read the tool catalogue.
 *
 * `look_at` and `read_url` are checked FIRST and are not a cosmetic addition.
 * They were the two cases with no branch at all, so the most
 * privacy-consequential action the agent can take — a storyboard, a contract or
 * a rushes still leaving the deployment as raw bytes to a third-party model —
 * fell through to the least informative string this component can produce. The
 * person watching saw the same word whether the agent was reasoning about
 * column titles or uploading their client's NDA. And because a read leaves no
 * plan row, no ghost and no audit entry, this line is the only moment the event
 * is ever visible to a human.
 */
export function phraseKeyFor(ev: AgentEvent | undefined): TKey {
  if (!ev) return 'agent.work.reading';
  if (ev.message?.includes('look_at')) return 'agent.work.looking';
  if (ev.message?.includes('read_url')) return 'agent.work.fetching';
  if (ev.message?.startsWith('read board')) return 'agent.work.reading';
  if (ev.message?.includes('read_board')) return 'agent.work.insideBoard';
  if (ev.message?.includes('search')) return 'agent.work.searching';
  if (ev.message?.includes('create')) return 'agent.work.drafting';
  if (ev.message?.includes('move')) return 'agent.work.placing';
  return 'agent.work.thinking';
}

/** How long a gap in the event stream stops being "thinking" and starts being
 *  a question the person deserves an answer to. */
const SILENCE_MS = 45_000;

/**
 * Corrections this run has been handed, and how far each one has got.
 *
 * A steer is accepted by the server and parked until the next turn boundary,
 * and until now the person had no evidence of either half: they typed "no, not
 * the archive", watched the identical spinner, typed it again, and the third
 * repetition hit `maxSteersPerRun` and was refused with a message that reads as
 * a telling-off. Two states, both read off the journal the server has always
 * broadcast: QUEUED is the server holding it, DELIVERED is the turn where it
 * actually reached the model.
 *
 * Deliberately reads `run.steer.delivered` defensively. A deployment that does
 * not emit it yet shows every note as queued — which is true, and truer than
 * claiming a delivery nothing observed.
 */
export function steerQueue(events: AgentEvent[]): Array<{ sequence: number; note: string; delivered: boolean }> {
  const delivered = new Set<string>();
  for (const e of events) {
    if (e.type !== 'run.steer.delivered') continue;
    const note = typeof e.data?.note === 'string' ? e.data.note : e.message;
    if (note) delivered.add(note);
  }
  const out: Array<{ sequence: number; note: string; delivered: boolean }> = [];
  for (const e of events) {
    if (e.type !== 'run.steered') continue;
    const note = typeof e.data?.note === 'string' ? e.data.note : '';
    if (!note) continue;
    out.push({ sequence: e.sequence, note, delivered: delivered.has(note) });
  }
  return out;
}

export function Working() {
  const events = useAgent((s) => s.events);
  const run = useAgent((s) => s.run);
  const maxSteps = useAgent((s) => s.capabilities?.limits.maxSteps ?? 0);
  const t = useT();
  const [note, setNote] = useState('');
  const [sent, setSent] = useState(false);
  const staged = events.filter((e) => e.type === 'action.staged');
  const latest = [...events].reverse().find((e) => e.type === 'step.finished' || e.type === 'action.staged');

  // A staged action carries the server's own sentence about what it decided.
  // That is the one string here worth more than a translated placeholder.
  const status = latest?.type === 'action.staged' && latest.message
    ? latest.message
    : t(phraseKeyFor(latest));

  /**
   * The watchdog.
   *
   * There was no clock on the client at all — an 8-minute deadline behind a
   * spinner reading "Thinking…" is indistinguishable from a wedged run, a dead
   * socket, or a provider hanging on its own timeout. The person had no basis
   * to decide whether to wait or to stop, and no basis to decide whether
   * stopping was worth it, because the cost meter was hidden during exactly the
   * window in which the money is being spent.
   *
   * A tick rather than a timeout, because the answer changes while nothing
   * happens: silence is the state being reported.
   */
  const lastAt = events.length > 0 ? events[events.length - 1].at : run?.createdAt;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);
  const sinceEvent = lastAt ? now - Date.parse(lastAt) : 0;
  const silent = Number.isFinite(sinceEvent) && sinceEvent > SILENCE_MS;
  // Past the point of no return: the transaction is going to the board.
  const writing = run?.state === 'APPLYING' || run?.state === 'VERIFYING';
  const elapsed = run?.createdAt ? Math.max(0, Math.round((now - Date.parse(run.createdAt)) / 1000)) : 0;

  // The step position is on the wire already: the server sends it as event
  // data and the client threw it into a six-phrase lookup and dropped it.
  const stepEvent = [...events].reverse().find((e) => e.type === 'step.finished');
  const step = typeof stepEvent?.data?.step === 'number' ? stepEvent.data.step + 1 : 0;
  const steers = steerQueue(events);

  return (
    <div className="agent-card">
      {staged.length > 0 && (
        <ol className="ac-ticker">
          {staged.slice(-4).map((e) => (
            <li key={e.sequence} className={e.data?.destructive ? 'danger' : ''}>
              <span className={`ap-kind k-${e.data?.kind}`}>{kindLabel(String(e.data?.kind ?? ''), t)}</span>
              <span>{e.message}</span>
            </li>
          ))}
        </ol>
      )}
      {/* The plan writes itself here, one line at a time, and until now it did
          so for sighted people only: nothing on this card was announced, so a
          screen-reader user got a card that never changed while the agent
          rewrote their board. polite rather than assertive — it is progress,
          not an alert, and interrupting every few seconds would be worse than
          silence. */}
      <div className="ac-statusrow" role="status" aria-live="polite">
        <span className={`dock-pulse${silent ? ' stalled' : ''}`} aria-hidden="true" />
        <span className="ac-status">
          {silent
            ? `${t('agent.silent')} ${Math.round(sinceEvent / 1000)} ${t('agent.silentSec')}`
            : status}
        </span>
        {/* Foreseeable rather than announced: where the run is, how long it has
            been going, and what it has spent — the person watching a run
            approach its cost cap is the person best placed to steer or stop
            it, and they were the only one who could not see it. */}
        {step > 0 && (
          <span className="ac-meta">
            {t('agent.step')} {step}{maxSteps > 0 ? ` ${t('agent.stepOf')} ${maxSteps}` : ''}
          </span>
        )}
        {elapsed > 0 && <span className="ac-meta">{formatElapsed(elapsed)}</span>}
        {(run?.usage?.costUsd ?? 0) > 0 && (
          <span className="ac-badge" title={t('agent.spentSoFar')}>${run!.usage.costUsd.toFixed(3)}</span>
        )}
        {staged.length > 0 && <span className="ac-badge">{staged.length} {t('agent.soFar')}</span>}
        {/* A run that has already ended is adopted correctly instead of showing
            a phantom spinner forever. */}
        {silent && (
          <button className="ac-ghostbtn" title={t('agent.checkHint')}
            onClick={() => void useAgent.getState().resync()}>
            {t('agent.check')}
          </button>
        )}
        {/* Once the write has begun there is nothing left to stop, and the
            server says so — it refuses a Stop in APPLYING or VERIFYING with
            "this is already being written to the board — you can undo it once
            it lands". Offering a live button there produced an error toast for
            pressing a control that was on screen and looked ready. */}
        {writing ? (
          <span className="ac-ghostbtn is-disabled" aria-disabled="true" title={t('agent.writingHint')}>
            {t('agent.writing')}
          </span>
        ) : (
          <button className="ac-ghostbtn" onClick={() => void useAgent.getState().cancel()}>
            {t('agent.stop')}
          </button>
        )}
      </div>

      {/* What you have already said, and what happened to it.
          Steering was fire-and-forget: the note went to the server, the same
          spinner kept spinning, and there was no evidence anywhere that anyone
          had heard it. So people typed it again, and the third repetition hit
          the cap and came back reading like a scolding. The journal has carried
          `run.steered` since steering shipped and no component read it. */}
      {steers.length > 0 && (
        <ul className="ac-steers" aria-label={t('agent.steerHeard')}>
          {steers.map((s) => (
            <li key={s.sequence} className={`ac-steer-chip${s.delivered ? ' delivered' : ''}`}>
              <span className="ac-steer-state">
                {t(s.delivered ? 'agent.steerDelivered' : 'agent.steerQueued')}
              </span>
              <span dir="auto">{s.note}</span>
            </li>
          ))}
        </ul>
      )}

      {/* Say something while it works. Stop used to be the only control, so
          noticing a misread request at change three meant throwing away the
          run and the tokens with it. The note is picked up between model turns,
          so it can never land inside a tool call. */}
      <div className="ac-steer">
        <input
          className="ac-steer-input"
          dir="auto"
          // The accessible name used to CHANGE as a side effect of using the
          // control: after sending, the placeholder — the field's only name —
          // became "Sent.", so on its next focus it announced as a different
          // control entirely.
          aria-label={t('agent.steerPlaceholder')}
          // ...and the acknowledgement itself moved OUT of the placeholder, to
          // the polite region below. "Sent." is news about what just happened,
          // which is a live region's job; a placeholder is a hint about what to
          // type, and using one to report the other left the control describing
          // itself as something it is not.
          placeholder={t('agent.steerPlaceholder')}
          value={note}
          onChange={(e) => { setNote(e.target.value); setSent(false); }}
          onKeyDown={(e) => {
            if (e.key !== 'Enter' || !note.trim()) return;
            void useAgent.getState().steer(note);
            setNote('');
            setSent(true);
          }}
        />
        {/* Where "Sent." lives now: a region that says it, and keeps saying it
            visibly, without renaming the control it belongs to. */}
        <div className="ac-steer-ack" role="status" aria-live="polite">{sent ? t('agent.steerSent') : ''}</div>
      </div>
    </div>
  );
}

/** m:ss once past a minute — a bare second count stops being readable there. */
function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}
