// Resting state: a quiet invitation that is always on screen.
import { CloseIcon, SparkleIcon } from '../components/Icons';
import { useT } from '../i18n';
import { useAgent } from './agentStore';

/**
 * When the board has visibly drifted the pill says so instead — one clause,
 * dismissible, and clicking it sends the request rather than opening an empty
 * composer. It costs nothing to detect, so it can be honest about being
 * optional: a hint that nags is one people learn to ignore.
 */
export function Pill() {
  const drift = useAgent((s) => s.drift);
  const t = useT();

  if (drift) {
    return (
      <div className="agent-pill drift">
        <SparkleIcon size={15} />
        <button className="drift-take" onClick={() => {
          useAgent.getState().dismissDrift();
          // Labelled as a hint, not as typed. A canned sentence accepted with one
          // click and a paragraph somebody composed at 2 a.m. have different
          // priors on every rate this run will feed.
          void useAgent.getState().start({ intent: drift.intent, scope: 'board', autonomy: 'preview', source: 'hint' });
        }}>
          {/* The server writes the observation; only the offer is ours to translate. */}
          {drift.message} — <em>{t('agent.tidyIt')}</em>
        </button>
        <button className="drift-no" title={t('agent.notNow')}
          onClick={() => useAgent.getState().dismissDrift()}>
          <CloseIcon size={12} />
        </button>
      </div>
    );
  }

  return (
    <button className="agent-pill" onClick={() => useAgent.getState().setOpen(true)}>
      <SparkleIcon size={15} />
      <span>{t('agent.ask')}</span>
      <kbd>⌘K</kbd>
    </button>
  );
}
