// The composer: what you are asking for, what the agent may see, and whether
// it applies straight away. Everything else about the run is decided elsewhere.
import { useEffect, useMemo, useRef, useState } from 'react';
import { useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import { uploadFile } from '../api/client';
import { toast } from '../components/ui/Toaster';
import { prompt } from '../components/ui/Prompt';
import { CloseIcon, ShieldIcon, SparkleIcon } from '../components/Icons';
import { useT, type TKey } from '../i18n';
import type { AgentAuditEntry, AgentAutonomy, AgentScope } from '../api/types';
import { plural, useAgent } from './agentStore';
import { localAcknowledgement, recordLocalAcknowledgement } from './processingConsent';
import { RunLedger } from './RunLedger';

/**
 * Where this board goes, said on the surface that sends it.
 *
 * Every trust mechanism the product has built — preview, revert, delegation,
 * quarantine — governs what the agent WRITES. Not one of them governed or
 * disclosed what it SENDS, and for a filmmaker whose boards hold unreleased
 * scripts, talent details and client budgets, that is the whole question.
 *
 * Deliberately quiet and permanent rather than a one-time modal: the honest
 * version of this is a fact visible every time, not a dialog dismissed once and
 * never seen again. Expanding it is the detail; the line itself is the
 * disclosure. The name is whatever the deployment reports, so a self-hosted
 * install states its own truth and this never becomes a false claim.
 */
function Processing() {
  const processing = useAgent((s) => s.capabilities?.processing);
  const t = useT();
  const [open, setOpen] = useState(false);
  const provider = processing?.provider?.trim();

  return (
    <div className="ac-processing">
      <button className="ac-processing-line" aria-expanded={open} onClick={() => setOpen(!open)}>
        <ShieldIcon size={11} />
        {t('agent.processing')} {provider || t('agent.processingUnknown')}
        {processing?.model ? ` (${processing.model})` : ''}
      </button>
      {open && (
        <div className="ac-processing-body">
          <strong>{t('agent.processingTitle')}</strong>
          <p>{t('agent.processingBody')}</p>
          {processing?.region && <p className="ac-audit-note">{processing.region}</p>}
        </div>
      )}
    </div>
  );
}

/**
 * Asked before the first run, not discovered afterwards.
 *
 * The permanent line above is the fact a person can re-read; this is the one
 * moment where they are actually given the choice. It stands IN PLACE of the
 * composer rather than beside it, because a disclosure you can send a request
 * past without reading is a disclosure in the same sense a cookie banner is
 * consent. Nothing here is a dark pattern: the alternative to agreeing is not
 * using the agent, and that is said plainly.
 *
 * Recorded on the account, once, so it never asks twice. The provider's name is
 * whatever the deployment reports — a self-hosted install states its own truth,
 * and a hard-coded name would be a false claim the first time a key changed.
 */
function ProcessingConsent({ onAgree }: { onAgree: () => void }) {
  const processing = useAgent((s) => s.capabilities?.processing);
  const t = useT();
  const provider = processing?.provider?.trim();

  return (
    <div className="agent-card">
      <div className="ac-consent" role="group" aria-labelledby="ac-consent-title">
        <div className="ac-consent-head">
          <ShieldIcon size={15} />
          <strong id="ac-consent-title">{t('agent.consentTitle')}</strong>
        </div>
        <p className="ac-consent-body">
          {t('agent.processing')} <strong>{provider || t('agent.processingUnknown')}</strong>
          {processing?.model ? ` (${processing.model})` : ''}
          {processing?.region ? ` — ${processing.region}` : ''}.
        </p>
        <p className="ac-consent-body">{t('agent.processingBody')}</p>
        {processing?.retentionPolicyUrl && (
          <p className="ac-consent-body">
            <a href={processing.retentionPolicyUrl} target="_blank" rel="noreferrer noopener">
              {t('agent.consentPolicy')}
            </a>
          </p>
        )}
        <div className="ac-consent-actions">
          <span className="ac-audit-note">{t('agent.consentDecline')}</span>
          <span className="ac-spacer" />
          <button className="ac-apply" onClick={onAgree}>{t('agent.consentAgree')}</button>
        </div>
      </div>
    </div>
  );
}

/**
 * What left this deployment, and when.
 *
 * The durable half of the disclosure above: a line at the moment of sending is
 * only useful to whoever is watching. This is the record that makes the same
 * event answerable a month later, and it is why the read block belongs on the
 * run rather than in this component — a hand-maintained inventory of what was
 * sent drifts within one wave and is worse than none, because it is believed.
 */
function ReadRecord({ entries }: { entries: AgentAuditEntry[] }) {
  const t = useT();
  const withReads = entries.filter((a) => (a.reads?.elementCount ?? 0) > 0
    || (a.reads?.attachmentIds?.length ?? 0) > 0
    || (a.reads?.urlsFetched?.length ?? 0) > 0);
  if (withReads.length === 0) return null;

  return (
    <div className="ac-reads">
      <div className="ac-reads-head">{t('agent.read.head')}</div>
      {withReads.slice(0, 2).map((a) => {
        const r = a.reads!;
        const parts: string[] = [];
        if (r.elementCount) parts.push(`${r.elementCount} ${t('agent.read.items')}`);
        if (r.attachmentIds?.length) parts.push(`${r.attachmentIds.length} ${t('agent.read.attachments')}`);
        if (r.urlsFetched?.length) parts.push(`${r.urlsFetched.length} ${t('agent.read.pages')}`);
        return (
          <div key={a.runId} className="ac-recent-row">
            <span title={a.intent}>{a.intent}</span>
            <em className="ac-audit-note">{parts.join(' · ')}</em>
          </div>
        );
      })}
    </div>
  );
}

/** Auto-apply. No bolt in the shared set, and this is the only place one is used. */
function BoltIcon({ size = 12 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M13 2 4 14h7l-1 8 9-12h-7l1-8z" />
    </svg>
  );
}

/* Short enough to sit on one line. Four wrapped to two rows, which made the
   resting card look like a menu before a single word had been typed. */
const PRESET_KEYS: TKey[] = ['agent.preset.columns', 'agent.preset.plan', 'agent.preset.summary'];

const SCOPE_KEYS: Record<AgentScope, TKey> = {
  board: 'agent.scope.board',
  unsorted: 'agent.scope.unsorted',
  selection: 'agent.scope.selection',
};

/** The last request, recalled with ↑ — people re-run variations constantly. */
let lastIntent = '';

export function Ask() {
  // Asked once, before anything can be sent. An empty value is "never asked",
  // which is also what an older account looks like — so the first time they
  // open the composer after this ships, they are asked too. Either record
  // counts; see processingConsent.ts for why there are two.
  const sub = useSettings((s) => s.sub);
  const accountAck = useSettings((s) => !!s.settings.agent?.processingAcknowledgedAt);
  // Read on each render rather than captured at mount: `sub` arrives with the
  // account, and a value captured before it landed would ask a person who has
  // already answered.
  const [justAgreed, setJustAgreed] = useState(false);
  const acknowledged = accountAck || justAgreed || !!localAcknowledgement(sub);
  const busy = useAgent((s) => s.busy);
  const recent = useAgent((s) => s.recent);
  const audit = useAgent((s) => s.audit);
  const maxCostUsd = useAgent((s) => s.capabilities?.limits.maxCostUsd ?? 0);
  const maxPasses = useAgent((s) => s.capabilities?.limits.maxPasses ?? 1);
  const reach = useAgent((s) => s.reach);
  // Undefined means an older server that does not report it; only an explicit
  // false is a claim that the provider is down.
  const offline = useAgent((s) => s.capabilities?.providerHealthy === false);
  const budget = useAgent((s) => s.capabilities?.budget);
  const t = useT();
  const boardId = useBoard((s) => s.boardId);
  const elements = useBoard((s) => s.elements);
  const selection = useBoard((s) => s.selection);
  const [intent, setIntent] = useState('');
  // JN8's ledger, open on request rather than always: the composer is for
  // asking, and the history is for checking up on what was asked.
  const [ledger, setLedger] = useState(false);
  const pendingImport = useAgent((s) => s.pendingImport);
  const [files, setFiles] = useState<{ id: string; name: string }[]>([]);
  const [uploading, setUploading] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const [autonomy, setAutonomy] = useState<AgentAutonomy>('preview');
  const [scope, setScope] = useState<AgentScope>('board');
  const inputRef = useRef<HTMLTextAreaElement>(null);

  const counts = useMemo(() => {
    let unsorted = 0;
    let canvas = 0;
    for (const el of Object.values(elements)) {
      if (el.location.parentId !== boardId || el.deletedAt || el.type === 'LINE') continue;
      if (el.location.section === 'UNSORTED') unsorted += 1;
      else canvas += 1;
    }
    return { unsorted, board: unsorted + canvas, selection: selection.size };
  }, [elements, boardId, selection]);

  useEffect(() => {
    // Opened from a right-click on a selection, the composer starts there —
    // otherwise the entry point would silently lose what you pointed at.
    const pending = useAgent.getState().pendingScope;
    setScope(pending ?? (counts.selection > 0 ? 'selection' : 'board'));
    const id = window.setTimeout(() => inputRef.current?.focus(), 50);
    return () => window.clearTimeout(id);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  /**
   * JN7's landing. A big paste has already been uploaded by the time the
   * composer opens, so this only has to adopt it: the attachment joins the
   * chips beside any files the person picked by hand, and the field is
   * pre-filled with the request the paste implies rather than left blank in
   * front of somebody who did not ask to be here.
   */
  useEffect(() => {
    if (!pendingImport) return;
    setFiles((prev) => (prev.some((f) => f.id === pendingImport.attachmentId)
      ? prev
      : [...prev, { id: pendingImport.attachmentId, name: pendingImport.name }].slice(0, 4)));
    setIntent((cur) => cur || t('agent.preset.structure'));
    inputRef.current?.focus();
    useAgent.getState().setPendingImport(null);
  }, [pendingImport, t]);

  const go = () => {
    if (!intent.trim() || uploading || offline) return;
    lastIntent = intent.trim();
    void useAgent.getState().start({
      intent, scope, autonomy,
      selectionIds: scope === 'selection' ? Array.from(selection) : undefined,
      attachmentIds: files.map((f) => f.id),
    });
    setIntent('');
    setFiles([]);
  };

  /**
   * Files attached to the REQUEST, as opposed to something already on the
   * board. Uploaded immediately through the ordinary presign pipeline, so by
   * the time the run starts the server has them and only needs their ids.
   */
  const attach = async (list: FileList | File[]) => {
    const picked = Array.from(list).slice(0, 4 - files.length);
    if (picked.length === 0) return;
    setUploading(true);
    try {
      const done = await Promise.all(picked.map(async (f) => {
        const { attachmentId } = await uploadFile(f);
        return { id: attachmentId, name: f.name };
      }));
      setFiles((prev) => [...prev, ...done].slice(0, 4));
    } catch {
      toast.error(t('agent.attachFailed'));
    } finally {
      setUploading(false);
    }
  };

  // Only the scopes that mean something right now: "selection" is not offered
  // when nothing is selected, so the chip never cycles through a dead option.
  const scopes: AgentScope[] = ['board', 'unsorted', ...(counts.selection > 0 ? ['selection' as const] : [])];
  const cycleScope = () => setScope(scopes[(scopes.indexOf(scope) + 1) % scopes.length]);
  const scopeLabel = (s: AgentScope) => t(SCOPE_KEYS[s]);

  // What the agent can ACTUALLY see, which is not the same as what is on the
  // board. The digest is budgeted, so a busy board is read in part — and a chip
  // that showed the board's own count promised a reach the agent never had.
  // Only the board scope is capped; a selection is read whole.
  const elided = scope === 'board' ? (reach?.elided ?? 0) : 0;
  const shown = elided > 0 && reach ? reach.visible : counts[scope];

  const board = boardId ? elements[boardId] : undefined;
  const rules = (board?.content?.agentInstructions as string | undefined) ?? '';
  const editRules = async () => {
    const next = await prompt({
      title: t('agent.rulesTitle'),
      placeholder: t('agent.rulesPlaceholder'),
      defaultValue: rules,
      confirmLabel: t('account.save'),
      multiline: true,
    });
    if (next === null || !boardId || next === rules) return;
    // Through the ordinary transaction path, so it is undoable and syncs like
    // any other edit rather than being agent-only state.
    void useBoard.getState().commitTransaction([{
      elementId: boardId,
      action: 'update',
      changes: { content: { agentInstructions: next } },
      undoChanges: { content: { agentInstructions: rules } },
    }]);
  };
  const revertible = recent.filter((r) => r.state === 'COMPLETED');
  // De-duplicated, newest first, short enough to sit on one line.
  const asked = useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const r of recent) {
      const q = r.task.intent.trim();
      if (!q || seen.has(q)) continue;
      seen.add(q);
      out.push(q.length > 42 ? `${q.slice(0, 41)}…` : q);
      if (out.length === 3) break;
    }
    return out;
  }, [recent]);

  // Nothing else on this card renders until the question is answered: the
  // composer's field, its presets, its recent runs and its send button are all
  // ways to transmit a board, and offering them behind a line of small print is
  // how "we told you" gets to mean "you never noticed".
  if (!acknowledged) {
    return (
      <ProcessingConsent
        onAgree={() => {
          const at = new Date().toISOString();
          recordLocalAcknowledgement(sub, at);
          setJustAgreed(true);
          useSettings.getState().update({ agent: { processingAcknowledgedAt: at } });
        }}
      />
    );
  }

  return (
    <div className="agent-card">
      {/* Said once, up front, instead of accepting the request and letting the
          run fail. role=status so it is announced rather than only drawn. */}
      {offline && (
        <div className="ac-offline" role="status">{t('agent.unavailable')}</div>
      )}

      {/* Your own recent requests come first — people re-run variations
          constantly, and the module-level ↑ history died on every reload while
          the server had every intent all along. Presets only fill the gap on a
          board you have never asked about. */}
      {!intent && (
        <div className="ac-presets">
          {(asked.length > 0 ? asked : PRESET_KEYS.map((k) => t(k))).map((p) => (
            <button key={p} className="ac-preset" title={p}
              onClick={() => { setIntent(p); inputRef.current?.focus(); }}>
              {p}
            </button>
          ))}
        </div>
      )}

      {/* Earlier runs stay revertible — regret usually arrives a few minutes late. */}
      {!intent && !ledger && revertible.length > 0 && (
        <div className="ac-recent">
          {revertible.slice(0, 2).map((r) => (
            <div key={r.id} className="ac-recent-row">
              <span title={r.task.intent}>{r.task.intent}</span>
              <button onClick={async () => { useAgent.setState({ run: r }); await useAgent.getState().revert(); }}>
                {t('agent.undo')}
              </button>
            </div>
          ))}
        </div>
      )}

      {/* JN8. The two rows above are a shortcut to the two most recent runs,
          and for a long time they were the ONLY door — after seven requests on
          a board, run #1 was unreachable from every surface in the app while
          its inverse ops sat in the journal costing nothing. This is the door
          to the rest of them. */}
      {!intent && ledger && <RunLedger onClose={() => setLedger(false)} />}
      {!intent && !ledger && (revertible.length > 0 || audit.length > 0) && (
        <button className="ac-ghostbtn ac-ledger-open" onClick={() => setLedger(true)}>
          {t('agent.ledgerOpen')}
        </button>
      )}

      {/* What the agent has already changed here. Shown only when there is
          something to show, and only before you start typing. */}
      {!intent && revertible.length === 0 && audit.length > 0 && (
        <div className="ac-recent">
          {audit.slice(0, 2).map((a) => (
            <div key={a.runId} className="ac-recent-row">
              <span title={a.intent}>{a.intent}</span>
              <em className="ac-audit-note">
                {a.reverted
                  ? t('agent.out.reverted')
                  : `${a.ops} ${plural(a.ops, t('agent.change'), t('agent.changes'))}`}
              </em>
            </div>
          ))}
        </div>
      )}

      {/* And — separately, because they answer different questions — what was
          READ. A run that read this whole board and was then discarded left no
          record at all: reading happened regardless of whether writing did. */}
      {!intent && <ReadRecord entries={audit} />}

      {/* Attached files, before the field, so it is obvious they are part of
          the request rather than something the agent found. */}
      {files.length > 0 && (
        <div className="ac-files">
          {files.map((f) => (
            <span key={f.id} className="ac-file">
              {f.name}
              <button title={t('common.close')} aria-label={`${t('common.close')} — ${f.name}`}
                onClick={() => setFiles((p) => p.filter((x) => x.id !== f.id))}>
                <CloseIcon size={10} />
              </button>
            </span>
          ))}
        </div>
      )}

      <div className="ac-inputrow">
        <SparkleIcon size={16} />
        <textarea
          ref={inputRef}
          className="ac-intent"
          dir="auto"
          rows={1}
          value={intent}
          // AX14, WCAG 3.3.2/1.3.1. A placeholder is not a programmatic name,
          // and it disappears the moment you type — so the field's purpose was
          // gone exactly when working memory needed it. This is the field where
          // a person describes what an agent should do to their board; it had
          // no name at all, and (before AX15) its only visible label was the
          // lowest-contrast text in the product.
          aria-label={t('agent.placeholder')}
          placeholder={t('agent.placeholder')}
          onChange={(e) => {
            setIntent(e.target.value);
            // Grow with the text instead of scrolling a two-line box.
            e.target.style.height = 'auto';
            e.target.style.height = `${Math.min(e.target.scrollHeight, 140)}px`;
          }}
          onPaste={(e) => {
            const f = Array.from(e.clipboardData.files);
            if (f.length) { e.preventDefault(); void attach(f); }
          }}
          onDrop={(e) => {
            if (e.dataTransfer.files.length) { e.preventDefault(); void attach(e.dataTransfer.files); }
          }}
          onDragOver={(e) => { if (e.dataTransfer.types.includes('Files')) e.preventDefault(); }}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); go(); }
            if (e.key === 'ArrowUp' && !intent && lastIntent) { e.preventDefault(); setIntent(lastIntent); }
          }}
        />
        <input ref={fileRef} type="file" multiple hidden
          accept="image/png,image/jpeg,image/webp,image/gif,application/pdf"
          onChange={(e) => { void attach(e.target.files ?? []); e.target.value = ''; }} />
        <button className="ac-clip" title={t('agent.attach')} aria-label={t('agent.attachAdd')}
          disabled={uploading || files.length >= 4} onClick={() => fileRef.current?.click()}>
          <span aria-hidden="true">{uploading ? '…' : '+'}</span>
        </button>
        {/* The arrow is compact, but on its own it never says what pressing it
            does. The title carries the outcome of the current mode, so the
            promise is legible before the click, not after. */}
        <button
          className="ac-send"
          disabled={busy || offline || !intent.trim()}
          onClick={go}
          title={t(autonomy === 'preview' ? 'agent.send.preview' : 'agent.send.auto')}
          aria-label={t('agent.send')}
        >
          <span aria-hidden="true">{busy ? '…' : '↑'}</span>
        </button>
      </div>

      {/* Said here, on the composer, at the moment the person is about to send.
          A privacy tab nobody opens is not a disclosure; a line beside the
          button that spends the money is. The provider's name comes from the
          live deployment, never from a constant in this bundle. */}
      <Processing />

      {/* Two chips, each showing its current value. The old row spelled the
          options out in two segmented tracks with lead-in words — five controls
          and three labels to say two things that already have safe defaults. */}
      <div className="ac-controls">
        <button
          className={`ac-chip${elided > 0 ? ' partial' : ''}`}
          onClick={cycleScope}
          title={elided > 0 ? t('agent.scope.partialHint') : t('agent.scope.hint')}
        >
          {scopeLabel(scope)} <b>{shown}</b>
          {elided > 0 && <i>{t('agent.scope.of')} {shown + elided}</i>}
        </button>
        <button
          className={`ac-chip mode ${autonomy}`}
          onClick={() => setAutonomy(autonomy === 'preview' ? 'auto' : 'preview')}
          title={t(autonomy === 'preview' ? 'agent.mode.previewHint' : 'agent.mode.autoHint')}
        >
          {autonomy === 'preview' ? <ShieldIcon size={12} /> : <BoltIcon size={12} />}
          {t(autonomy === 'preview' ? 'agent.mode.preview' : 'agent.mode.auto')}
        </button>
        <span className="ac-spacer" />
        {/* Board conventions the agent should always honour. Author-written:
            rules it inferred for itself would be invisible and could not be
            argued with. */}
        <button className="ac-chip" onClick={editRules} title={t('agent.rulesHint')}>
          {t(rules ? 'agent.rulesSet' : 'agent.rules')}
        </button>
        {/* The ceiling, not a guess. An estimate of what a run WILL cost cannot
            be honest before the model has read the board, but the worst case is
            known exactly — and the worst case is what a person actually wants
            before pressing a button that spends money. */}
        {/* The worst case, honestly. A run may be revised, and each pass gets a
            fresh budget — so the ceiling is per-pass times the passes allowed,
            not the single-pass figure this first showed. */}
        {budget && budget.dailyCapUsd > 0 ? (
          // What is actually left today beats a per-run ceiling: it is the
          // number that decides whether the next request happens at all.
          <span
            className={`ac-ceiling${budget.spentTodayUsd >= budget.dailyCapUsd * 0.8 ? ' low' : ''}`}
            title={t('agent.budgetLeft')}
          >
            ${Math.max(0, budget.dailyCapUsd - budget.spentTodayUsd).toFixed(2)} {t('agent.left')}
          </span>
        ) : maxCostUsd > 0 ? (
          <span className="ac-ceiling" title={t('agent.ceiling')}>
            ≤ ${(maxCostUsd * maxPasses).toFixed(2)}
          </span>
        ) : null}
      </div>
    </div>
  );
}
