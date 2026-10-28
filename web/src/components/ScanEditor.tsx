import { Fragment, useState } from 'react';
import { useStore } from '../store';
import { upsertScan, deleteScan } from '../ws';
import { SOUNDS, play, unlock } from '../audio';
import { groupedFields, FIELD_GROUPS, filterableFields, columnableFields, fieldsInGroup, fieldMenuLabel, fieldTitle } from '../fields';
import { Modal } from './Modal';
import type { ScanDef, Filter } from '../types';

const CMPS = ['>', '>=', '<', '<=', 'between', '==', 'in'];
// Security types the scanner's universe tracks (internal/universe.allowed) --
// the only values a secType filter can ever usefully match against.
const SEC_TYPES = [
  { value: 'CS', label: 'Common Stock' },
  { value: 'ETF', label: 'ETF' },
  { value: 'ADRC', label: 'ADR' },
];
// The "Active session" gate maps to a `session == <value>` predicate; 'any' omits it.
const SESSIONS = ['any', 'pre', 'regular', 'post'];

interface Cond {
  field: string;
  cmp: string;
  value: string;
}

const isSessionGate = (c: Filter) => c.field === 'session' && c.cmp === '==';

function sessionOf(def: ScanDef | null): string {
  const c = def?.filter?.conds?.find(isSessionGate);
  return typeof c?.value === 'string' ? c.value : 'any';
}

function defToConds(def: ScanDef | null): Cond[] {
  const conds = def?.filter?.conds;
  if (!conds || conds.length === 0) return [{ field: 'chg5m', cmp: '>', value: '2' }];
  // Strip the session gate — it's edited via the dedicated dropdown, not as a row.
  return conds
    .filter((c) => c.field && !isSessionGate(c))
    .map((c) => ({
      field: c.field as string,
      cmp: c.cmp as string,
      value: Array.isArray(c.value) ? (c.value as unknown[]).join(',') : String(c.value ?? ''),
    }));
}

export function ScanEditor() {
  const editing = useStore((s) => s.editing);
  const close = useStore((s) => s.closeEditor);

  const [name, setName] = useState(editing?.name ?? 'New Scan');
  const [enabled, setEnabled] = useState(editing?.enabled ?? true);
  const [limit, setLimit] = useState(String(editing?.limit ?? 50));
  const [sortField, setSortField] = useState(editing?.sort?.[0]?.field ?? 'chg5m');
  const [sortDir, setSortDir] = useState(editing?.sort?.[0]?.dir ?? 'desc');
  // Optional tiebreak applied only when the primary values are equal.
  const [sort2Field, setSort2Field] = useState(editing?.sort?.[1]?.field ?? '');
  const [sort2Dir, setSort2Dir] = useState(editing?.sort?.[1]?.dir ?? 'desc');
  const [conds, setConds] = useState<Cond[]>(defToConds(editing));
  const [activeSession, setActiveSession] = useState(sessionOf(editing));
  const [cols, setCols] = useState<string[]>(
    editing?.columns?.length ? editing.columns : ['last', 'chg5m', 'chgDay', 'vol', 'rvol', 'news'],
  );

  // Alert section. The form only understands a subset of AlertConfig fields —
  // 'cross' mirrors the seeded flag scans, which fire once when a field like
  // iflagState crosses a level rather than on every entry (see save() below).
  const [alertEnabled, setAlertEnabled] = useState(editing?.alert?.enabled ?? true);
  const [alertOn, setAlertOn] = useState<'enter' | 'cross'>(editing?.alert?.on === 'cross' ? 'cross' : 'enter');
  const [trigField, setTrigField] = useState(editing?.alert?.trigger?.field ?? 'chg5m');
  const [trigDir, setTrigDir] = useState<'above' | 'below'>(
    editing?.alert?.trigger?.cmp === 'crossBelow' ? 'below' : 'above',
  );
  const [trigValue, setTrigValue] = useState(
    editing?.alert?.trigger?.value != null ? String(editing.alert.trigger.value) : '',
  );
  const [cooldown, setCooldown] = useState(String(editing?.alert?.cooldownSec ?? 120));
  const [requireReentry, setRequireReentry] = useState(editing?.alert?.requireReentry ?? true);
  const [sound, setSound] = useState(editing?.alert?.sound ?? 'ping');
  const [speakSym, setSpeakSym] = useState(editing?.alert?.speak ?? false);
  const [sendToTws, setSendToTws] = useState(editing?.alert?.sendToTws ?? false);
  const [err, setErr] = useState('');

  const [confirmDel, setConfirmDel] = useState(false);

  const setCond = (i: number, patch: Partial<Cond>) =>
    setConds((cs) => cs.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  const addCond = () => setConds((cs) => [...cs, { field: 'rvol', cmp: '>', value: '3' }]);
  const rmCond = (i: number) => setConds((cs) => cs.filter((_, j) => j !== i));
  const toggleCol = (c: string) =>
    setCols((cur) => (cur.includes(c) ? cur.filter((x) => x !== c) : [...cur, c]));

  function save() {
    // A cross alert with no parseable level would silently arm at value NaN
    // server-side and never fire — refuse the save instead. A blank field is
    // also invalid: Number('') is 0, which is finite and would otherwise slip
    // through as a silent "crosses 0" trigger.
    if (alertOn === 'cross' && (trigValue.trim() === '' || !Number.isFinite(Number(trigValue)))) {
      setErr('Enter a numeric level for the cross alert');
      return;
    }
    const metricConds = conds.map((c) => {
      if (c.cmp === 'between') {
        const parts = c.value.split(',').map((x) => Number(x.trim()));
        return { field: c.field, cmp: 'between', value: parts };
      }
      if (c.cmp === 'in') {
        // Each entry converts to a number when it looks like one (so 'in' on
        // a numeric field works too), otherwise stays a string — which is
        // what secType's CS/ETF/ADRC codes need.
        const parts = c.value
          .split(',')
          .map((x) => x.trim())
          .filter(Boolean)
          .map((x) => {
            const n = Number(x);
            return Number.isNaN(n) ? x : n;
          });
        return { field: c.field, cmp: 'in', value: parts };
      }
      const num = Number(c.value);
      return { field: c.field, cmp: c.cmp, value: Number.isNaN(num) || c.value.trim() === '' ? c.value : num };
    });
    // Prepend the session gate when scoped to a specific session.
    const gate: Filter[] = activeSession === 'any' ? [] : [{ field: 'session', cmp: '==', value: activeSession }];
    const filter: Filter = { op: 'and', conds: [...gate, ...metricConds] };
    const def: ScanDef = {
      id: editing?.id ?? `scan-${Date.now()}`,
      name: name.trim() || 'Scan',
      enabled,
      filter,
      // The secondary key is omitted when unset or when it duplicates the
      // primary (a repeated field can never break its own tie).
      sort:
        sort2Field && sort2Field !== sortField
          ? [{ field: sortField, dir: sortDir }, { field: sort2Field, dir: sort2Dir }]
          : [{ field: sortField, dir: sortDir }],
      limit: Number(limit) || 50,
      columns: cols,
      // Spread the existing alert first so any field this form does not know
      // about survives a round trip.
      alert: {
        ...(editing?.alert ?? {}),
        enabled: alertEnabled,
        on: alertOn,
        trigger:
          alertOn === 'cross'
            ? { field: trigField, cmp: trigDir === 'above' ? 'crossAbove' : 'crossBelow', value: Number(trigValue) }
            : null,
        sound,
        speak: speakSym,
        sendToTws,
        cooldownSec: Math.max(0, Math.floor(Number(cooldown)) || 0),
        requireReentry,
      },
    };
    upsertScan(def);
    close();
  }

  function remove() {
    if (!confirmDel) {
      setConfirmDel(true);
      return;
    }
    if (editing) deleteScan(editing.id);
    close();
  }

  return (
    <Modal title={editing ? 'Edit scan' : 'New scan'} onClose={close}>
      <div className="row">
        <label>Name</label>
        <input value={name} onChange={(e) => setName(e.target.value)} />
      </div>

      <div className="row">
        <label>
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Enabled
        </label>
        <label>Top N</label>
        <input className="num" value={limit} onChange={(e) => setLimit(e.target.value)} />
      </div>

      <div className="row">
        <label>Sort</label>
        <select value={sortField} onChange={(e) => setSortField(e.target.value)}>
          {groupedFields(filterableFields()).map((g) => (
            <optgroup key={g.label} label={g.label}>
              {g.defs.map((d) => (
                <option key={d.key} value={d.key}>
                  {fieldMenuLabel(d.key)}
                </option>
              ))}
            </optgroup>
          ))}
        </select>
        <select value={sortDir} onChange={(e) => setSortDir(e.target.value)}>
          <option value="desc">desc</option>
          <option value="asc">asc</option>
        </select>
      </div>

      <div className="row">
        <label>then by</label>
        <select value={sort2Field} onChange={(e) => setSort2Field(e.target.value)}>
          <option value="">— none —</option>
          {groupedFields(filterableFields().filter((d) => d.key !== sortField)).map((g) => (
            <optgroup key={g.label} label={g.label}>
              {g.defs.map((d) => (
                <option key={d.key} value={d.key}>
                  {fieldMenuLabel(d.key)}
                </option>
              ))}
            </optgroup>
          ))}
        </select>
        <select value={sort2Dir} disabled={!sort2Field} onChange={(e) => setSort2Dir(e.target.value)}>
          <option value="desc">desc</option>
          <option value="asc">asc</option>
        </select>
        <span className="muted hint">breaks ties on the primary sort</span>
      </div>

      <div className="row">
        <label>Active session</label>
        <select value={activeSession} onChange={(e) => setActiveSession(e.target.value)}>
          {SESSIONS.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <span className="muted">
          {activeSession === 'any' ? 'matches in every session' : `only matches during ${activeSession}-market`}
        </span>
      </div>

      <div className="section">Filters (all must match)</div>
      {conds.map((c, i) => (
        <div className="row cond" key={i}>
          <select
            value={c.field}
            title={fieldTitle(c.field)}
            onChange={(e) => {
              const field = e.target.value;
              // secType is enum-like (pick any of CS/ETF/ADR) — 'in' is the
              // only operator that makes sense for it, so switching to it
              // starts clean rather than carrying over a numeric cmp/value.
              setCond(i, field === 'secType' ? { field, cmp: 'in', value: '' } : { field });
            }}
          >
            {/* value stays the bare field key; the unit is display-only. */}
            {groupedFields(filterableFields()).map((g) => (
            <optgroup key={g.label} label={g.label}>
              {g.defs.map((d) => (
                <option key={d.key} value={d.key}>
                  {fieldMenuLabel(d.key)}
                </option>
              ))}
            </optgroup>
          ))}
          </select>
          {c.field === 'secType' ? (
            <div className="cols cond-sectypes">
              {SEC_TYPES.map((t) => {
                const selected = c.value.split(',').includes(t.value);
                return (
                  <label key={t.value} className={`chip ${selected ? 'on' : ''}`}>
                    <input
                      type="checkbox"
                      checked={selected}
                      onChange={() => {
                        const set = new Set(c.value.split(',').map((s) => s.trim()).filter(Boolean));
                        if (set.has(t.value)) set.delete(t.value);
                        else set.add(t.value);
                        setCond(i, { cmp: 'in', value: Array.from(set).join(',') });
                      }}
                    />
                    {t.label}
                  </label>
                );
              })}
            </div>
          ) : (
            <>
              <select value={c.cmp} onChange={(e) => setCond(i, { cmp: e.target.value })}>
                {CMPS.map((m) => (
                  <option key={m}>{m}</option>
                ))}
              </select>
              <input
                value={c.value}
                placeholder={c.cmp === 'between' ? 'lo,hi' : c.cmp === 'in' ? 'a,b,c' : 'value'}
                onChange={(e) => setCond(i, { value: e.target.value })}
              />
            </>
          )}
          <button className="mini" onClick={() => rmCond(i)}>
            ✕
          </button>
        </div>
      ))}
      <button className="mini" onClick={addCond}>
        + condition
      </button>

      <div className="section">Columns</div>
      <div className="cols">
        {FIELD_GROUPS.map((g) => {
          const defs = fieldsInGroup(g.id, columnableFields());
          if (defs.length === 0) return null;
          return (
            <Fragment key={g.id}>
              <div className="muted hint" style={{ width: '100%', marginTop: 6 }}>
                {g.label}
              </div>
              {defs.map((d) => (
                <label key={d.key} className={`chip ${cols.includes(d.key) ? 'on' : ''}`} title={fieldTitle(d.key)}>
                  <input type="checkbox" checked={cols.includes(d.key)} onChange={() => toggleCol(d.key)} />
                  {d.label}
                </label>
              ))}
            </Fragment>
          );
        })}
      </div>

      <div className="section">Alert</div>
      <div className="row">
        <label>
          <input type="checkbox" checked={alertEnabled} onChange={(e) => setAlertEnabled(e.target.checked)} /> Sound alert
        </label>
        <select value={sound} onChange={(e) => setSound(e.target.value)}>
          {SOUNDS.map((s) => (
            <option key={s}>{s}</option>
          ))}
        </select>
        <button
          className="mini"
          title="play this sound"
          onClick={() => {
            unlock();
            play(sound);
          }}
        >
          ▶ Preview
        </button>
      </div>

      <div className="row">
        <label title="after the tone, say the symbol and the scan's name out loud, e.g. “BEAT: nine E M A flag, ready” — works even when the tape reader's voice is off; muting the scan silences it too">
          <input type="checkbox" checked={speakSym} onChange={(e) => setSpeakSym(e.target.checked)} /> Speak the symbol
        </label>
        <span className="muted hint">says “SYMBOL: scan name” after the tone</span>
      </div>

      <div className="row">
        <label title="Moves your linked TWS window to the symbol when this scan alerts (display-group link only — it never places orders). Skipped if you picked a symbol yourself in the last 20 s, or another scan was just sent.">
          <input type="checkbox" checked={sendToTws} onChange={(e) => setSendToTws(e.target.checked)} /> Send to TWS when it alerts
        </label>
        <span className="muted hint">display link only, never orders</span>
      </div>

      <div className="row">
        <label>When</label>
        <select
          value={alertOn}
          onChange={(e) => {
            // The row below (and its validation error) only applies in 'cross'
            // mode — drop a stale error left over from switching away from it.
            setAlertOn(e.target.value as 'enter' | 'cross');
            setErr('');
          }}
        >
          <option value="enter">a symbol enters the scan</option>
          <option value="cross">a field crosses a level</option>
        </select>
      </div>

      {alertOn === 'cross' && (
        <div className="row cond">
          <select value={trigField} title={fieldTitle(trigField)} onChange={(e) => setTrigField(e.target.value)}>
            {filterableFields().map((d) => (
              <option key={d.key} value={d.key}>
                {fieldMenuLabel(d.key)}
              </option>
            ))}
          </select>
          <select value={trigDir} onChange={(e) => setTrigDir(e.target.value as 'above' | 'below')}>
            <option value="above">above</option>
            <option value="below">below</option>
          </select>
          <input
            className="num"
            value={trigValue}
            placeholder="level"
            onChange={(e) => {
              setTrigValue(e.target.value);
              setErr('');
            }}
          />
          <span className="muted hint">fires once when the value crosses the level for a symbol already in the scan</span>
        </div>
      )}

      <div className="row">
        <label>Cooldown</label>
        <input className="num" type="number" min={0} value={cooldown} onChange={(e) => setCooldown(e.target.value)} />
        <span>s</span>
        <label title="after an alert, the symbol must leave the scan and come back before it can alert again">
          <input type="checkbox" checked={requireReentry} onChange={(e) => setRequireReentry(e.target.checked)} /> Require re-entry
        </label>
      </div>

      <div className="actions">
        {editing && (
          <button className="danger" onClick={remove}>
            {confirmDel ? 'Confirm delete' : 'Delete'}
          </button>
        )}
        {err && <span style={{ color: 'var(--neg)', fontSize: 12 }}>{err}</span>}
        <span className="spacer" />
        <button onClick={close}>Cancel</button>
        <button className="primary" onClick={save}>
          Save
        </button>
      </div>
    </Modal>
  );
}
