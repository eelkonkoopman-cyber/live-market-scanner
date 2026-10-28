// The Signal window's "Alerts" menu (SignalSwitchMenu) and the left-bar list's
// 🔊 / 🔇 when the list shows a switchable signal (FlagScanList), rendered
// server-side (no DOM): one checkbox per signal with its state, and the list's
// button showing - and flipping - the SAME switch, not a separate per-scan mute.
import { afterEach, describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { SignalSwitchMenu } from './SignalSwitchMenu';
import { FlagScanList } from './FlagScanList';
import { defaultSignalSwitches, SIGNAL_SWITCHES } from '../e9';
import { useStore } from '../store';
import type { ScanDef } from '../types';

describe('SignalSwitchMenu', () => {
  it('closed: one button with the on-count', () => {
    const html = renderToStaticMarkup(<SignalSwitchMenu switches={defaultSignalSwitches()} onToggle={() => {}} />);
    expect(html).toContain('Alerts 21/24');
    expect(html).not.toContain('type="checkbox"');
  });

  it('open: a checkbox per signal, ticked as switched, in menu order, under two group heads', () => {
    const sw = { ...defaultSignalSwitches(), 'e9flag-ready': false };
    const html = renderToStaticMarkup(<SignalSwitchMenu switches={sw} onToggle={() => {}} defaultOpen />);
    const rows = html.split('<label').slice(1);
    expect(rows.map((r) => /<span class="tabmenu-name">([^<]+)<\/span>/.exec(r)?.[1])).toEqual(SIGNAL_SWITCHES.map((s) => s.label));
    expect(rows.map((r) => /<input type="checkbox"[^>]*checked=""/.test(r))).toEqual(SIGNAL_SWITCHES.map((s) => s.id !== 'e9flag-ready' && s.id !== 'dildo-touch' && s.id !== 'bouncy-forming' && s.id !== 'daily-breakout'));
    expect(html).toContain('>9EMA flag</div>');
    expect(html).toContain('>1-minute alerts</div>');
  });
});

describe("FlagScanList's 🔊 on a switchable signal", () => {
  const initial = useStore.getInitialState();
  const saved = { feed: initial.feed, scans: initial.scans, grids: initial.grids, mutedScans: initial.mutedScans };
  afterEach(() => {
    Object.assign(useStore.getInitialState(), saved);
  });
  const scans = [{ id: 'e9flag-sideways', name: '9EMA Flag — sideways', enabled: true, filter: {}, sort: [], limit: 50, columns: [], alert: { enabled: true } }] as unknown as ScanDef[];
  const render = (props: { signalOn?: boolean; onToggleSignal?: () => void }, muted = false) => {
    Object.assign(useStore.getInitialState(), { feed: 'up', scans, grids: {}, mutedScans: muted ? { 'e9flag-sideways': true } : {} });
    return renderToStaticMarkup(<FlagScanList scanId="e9flag-sideways" onScanId={() => {}} onChart={new Set()} onPick={() => {}} {...props} />);
  };

  it('shows the switch: on = 🔊, off = 🔇, with the switch tooltip', () => {
    const on = render({ signalOn: true, onToggleSignal: () => {} });
    expect(on).toContain('🔊');
    expect(on).toContain('switched on in this window');
    const off = render({ signalOn: false, onToggleSignal: () => {} });
    expect(off).toContain('🔇');
    expect(off).toContain('switched off in this window');
  });

  it('without a switch (a scan of your own) it is the per-scan mute, as before', () => {
    expect(render({})).toContain('🔊');
    expect(render({}, true)).toContain('🔇');
    expect(render({}, true)).toContain('muted in this window');
  });
});
