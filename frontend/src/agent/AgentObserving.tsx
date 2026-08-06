// Somebody else is running the agent on this board.
//
// The board's own content is about to change under you, and until now nothing
// said so: the run's journal was broadcast to everyone and consumed by nobody,
// so cards simply moved. This is the smallest honest answer — who, that it is
// happening, and roughly how much.
//
// What it deliberately does not show: their request, their plan, or any control
// over it. The request is their words and the plan is their decision; a
// collaborator needs to know the board is in motion, not to read over a
// shoulder.
import { useT } from '../i18n';
import { SparkleIcon } from '../components/Icons';
import { plural, useAgent, WORKING } from './agentStore';

export function Observing() {
  const active = useAgent((s) => s.active);
  const staged = useAgent((s) => s.observedStaged);
  const t = useT();

  if (!active || active.mine) return null;

  const working = WORKING.includes(active.state);
  const what = working ? t('agent.theirRunWorking') : t('agent.theirRunProposed');

  return (
    <div className="agent-pill observing" title={t('agent.watching')}>
      {working && <span className="dock-pulse" />}
      {!working && <SparkleIcon size={14} />}
      <span>
        <b>{active.actor}</b> {what}
      </span>
      {staged > 0 && (
        <span className="ac-badge">
          {staged} {plural(staged, t('agent.change'), t('agent.changes'))}
        </span>
      )}
    </div>
  );
}
