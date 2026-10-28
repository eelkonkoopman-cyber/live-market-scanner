import { useEffect, useState } from 'react';

// 1920s exchange-floor wall clocks (New York + London) for the charts-grid
// sidebar. Pure helpers are exported for tests; one shared 1 s timer drives
// both faces and pauses while the document is hidden.

export interface ZonedTime {
  h: number;
  m: number;
  s: number;
  weekday: number; // 0 = Sun .. 6 = Sat
  abbr: string; // EST/EDT, GMT/BST
}

const fmtCache = new Map<string, Intl.DateTimeFormat>();
function fmt(tz: string): Intl.DateTimeFormat {
  let f = fmtCache.get(tz);
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', {
      timeZone: tz, hourCycle: 'h23', weekday: 'short',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
    });
    fmtCache.set(tz, f);
  }
  return f;
}
const WD: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 };

/** Offset of tz from UTC in minutes at instant ms. */
function offsetMin(tz: string, ms: number): number {
  const p = fmt(tz).formatToParts(new Date(ms));
  const g = (t: string) => Number(p.find((x) => x.type === t)?.value ?? 0);
  const d = new Date(ms);
  const local = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate(), g('hour') % 24, g('minute'), g('second'));
  // local may be on the neighbouring UTC day; normalise to +-12 h
  let diff = Math.round((local - Math.floor(ms / 1000) * 1000) / 60000);
  if (diff > 14 * 60) diff -= 1440;
  if (diff < -14 * 60) diff += 1440;
  return diff;
}

export function zonedTime(tz: string, ms: number): ZonedTime {
  const p = fmt(tz).formatToParts(new Date(ms));
  const g = (t: string) => p.find((x) => x.type === t)?.value ?? '0';
  const off = offsetMin(tz, ms);
  let abbr: string;
  if (tz === 'America/New_York') abbr = off === -240 ? 'EDT' : 'EST';
  else if (tz === 'Europe/London') abbr = off === 60 ? 'BST' : 'GMT';
  else abbr = `UTC${off >= 0 ? '+' : '-'}${Math.abs(off / 60)}`;
  return { h: Number(g('hour')) % 24, m: Number(g('minute')), s: Number(g('second')), weekday: WD[g('weekday')] ?? 0, abbr };
}

const pad = (n: number) => String(n).padStart(2, '0');
export const hhmmss = (t: ZonedTime) => `${pad(t.h)}:${pad(t.m)}:${pad(t.s)}`;

export type SessionTag = 'PRE' | 'MARKET OPEN' | 'AFTER' | 'CLOSED';
/** NY session from New York wall time (Mon–Fri; holidays not modelled). */
export function sessionTag(ny: ZonedTime): SessionTag {
  if (ny.weekday === 0 || ny.weekday === 6) return 'CLOSED';
  const min = ny.h * 60 + ny.m;
  if (min >= 240 && min < 570) return 'PRE';
  if (min >= 570 && min < 960) return 'MARKET OPEN';
  if (min >= 960 && min < 1200) return 'AFTER';
  return 'CLOSED';
}

/** Hand angles in degrees clockwise from 12. */
export function handAngles(t: ZonedTime): { hour: number; minute: number; second: number } {
  return {
    hour: ((t.h % 12) + t.m / 60 + t.s / 3600) * 30,
    minute: (t.m + t.s / 60) * 6,
    second: t.s * 6,
  };
}

function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    let id: number | undefined;
    const start = () => {
      if (id !== undefined || document.hidden) return;
      setNow(Date.now());
      // align ticks to the wall-clock second
      id = window.setTimeout(function tick() {
        setNow(Date.now());
        id = window.setTimeout(tick, 1000 - (Date.now() % 1000) + 5);
      }, 1000 - (Date.now() % 1000) + 5);
    };
    const stop = () => { if (id !== undefined) { clearTimeout(id); id = undefined; } };
    const vis = () => (document.hidden ? stop() : start());
    start();
    document.addEventListener('visibilitychange', vis);
    return () => { stop(); document.removeEventListener('visibilitychange', vis); };
  }, []);
  return now;
}

const ROMAN = ['XII', 'I', 'II', 'III', 'IV', 'V', 'VI', 'VII', 'VIII', 'IX', 'X', 'XI'];

function Face({ t, uid }: { t: ZonedTime; uid: string }) {
  const a = handAngles(t);
  const ticks = [];
  for (let i = 0; i < 60; i++) {
    const major = i % 5 === 0;
    ticks.push(
      <line key={i} x1="100" y1={major ? 26 : 27} x2="100" y2={major ? 34 : 30}
        stroke="#3a2a12" strokeWidth={major ? 2.2 : 0.8} transform={`rotate(${i * 6} 100 100)`} />,
    );
  }
  return (
    <svg className="xc-face" viewBox="0 0 200 200" aria-hidden="true">
      <defs>
        <linearGradient id={`${uid}-brass`} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#f6e3a1" />
          <stop offset="0.35" stopColor="#c9a24a" />
          <stop offset="0.6" stopColor="#8a6420" />
          <stop offset="1" stopColor="#e8c873" />
        </linearGradient>
        <radialGradient id={`${uid}-ivory`} cx="0.45" cy="0.4" r="0.7">
          <stop offset="0" stopColor="#fbf5e3" />
          <stop offset="1" stopColor="#e6d9b4" />
        </radialGradient>
      </defs>
      <circle cx="100" cy="100" r="98" fill={`url(#${uid}-brass)`} />
      <circle cx="100" cy="100" r="88" fill="#2a1d0a" />
      <circle cx="100" cy="100" r="86" fill={`url(#${uid}-brass)`} />
      <circle cx="100" cy="100" r="80" fill={`url(#${uid}-ivory)`} stroke="#6b4e1c" strokeWidth="1" />
      {/* deco sunburst rays */}
      {Array.from({ length: 24 }, (_, i) => (
        <line key={`r${i}`} x1="100" y1="100" x2="100" y2="40" stroke="#c9b68a" strokeWidth="0.5"
          opacity="0.5" transform={`rotate(${i * 15} 100 100)`} />
      ))}
      <circle cx="100" cy="100" r="44" fill="none" stroke="#b49a63" strokeWidth="0.6" />
      {ticks}
      {ROMAN.map((r, i) => {
        const ang = (i * 30 * Math.PI) / 180;
        return (
          <text key={r} x={100 + 59 * Math.sin(ang)} y={100 - 59 * Math.cos(ang)} className="xc-numeral"
            textAnchor="middle" dominantBaseline="central">{r}</text>
        );
      })}
      {/* hour hand: spade */}
      <g transform={`rotate(${a.hour} 100 100)`}>
        <path d="M98 112 L98 74 Q92 68 100 56 Q108 68 102 74 L102 112 Z" fill="#1c140a" />
        <circle cx="100" cy="68" r="3" fill="#e6d9b4" />
      </g>
      {/* minute hand: slim lance */}
      <g transform={`rotate(${a.minute} 100 100)`}>
        <path d="M98.6 114 L98.6 46 L100 32 L101.4 46 L101.4 114 Z" fill="#1c140a" />
        <path d="M100 46 L96 54 L100 50 L104 54 Z" fill="#1c140a" />
      </g>
      {/* seconds: thin red */}
      <g transform={`rotate(${a.second} 100 100)`}>
        <line x1="100" y1="120" x2="100" y2="30" stroke="#b3201b" strokeWidth="1.2" />
        <circle cx="100" cy="118" r="3" fill="#b3201b" />
      </g>
      <circle cx="100" cy="100" r="5" fill={`url(#${uid}-brass)`} stroke="#3a2a12" strokeWidth="0.8" />
    </svg>
  );
}

function Clock({ city, tz, now, ny }: { city: string; tz: string; now: number; ny?: boolean }) {
  const t = zonedTime(tz, now);
  const time = hhmmss(t);
  const tag = ny ? sessionTag(t) : null;
  return (
    <div className="xc-clock" role="img" aria-label={`${city} ${time} ${t.abbr}${tag ? `, ${tag.toLowerCase()}` : ''}`}>
      <Face t={t} uid={`xc-${city.replace(/\s/g, '')}`} />
      <div className="xc-plaque">{city}</div>
      <div className="xc-digital">{time}</div>
      <div className="xc-meta">
        <span>{t.abbr}</span>
        {tag && <span className={`xc-tag xc-tag-${tag.split(' ')[0].toLowerCase()}`}>{tag}</span>}
      </div>
    </div>
  );
}

const LS_KEY = 'exchangeClocks.open';
function readOpen(): boolean {
  try { return localStorage.getItem(LS_KEY) !== '0'; } catch { return true; }
}

export function ExchangeClocks() {
  const [open, setOpen] = useState(readOpen);
  const toggle = () => {
    const v = !open;
    setOpen(v);
    try { localStorage.setItem(LS_KEY, v ? '1' : '0'); } catch { /* ignore */ }
  };
  return (
    <div className={`xc ${open ? '' : 'xc-closed'}`}>
      <button className="xc-toggle" onClick={toggle} aria-expanded={open}
        title={open ? 'hide the exchange clocks' : 'show the exchange clocks'}>
        {open ? '▾' : '▸'} Exchange clocks
      </button>
      {open && <ClocksBody />}
    </div>
  );
}

function ClocksBody() {
  const now = useNow();
  return (
    <div className="xc-body">
      <Clock city="NEW YORK" tz="America/New_York" now={now} ny />
      <Clock city="LONDON" tz="Europe/London" now={now} />
    </div>
  );
}
