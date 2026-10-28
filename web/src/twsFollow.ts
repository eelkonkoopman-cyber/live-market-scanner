// The Signal window's global "TWS follow" switch: when ON (the default) a scan
// with "Send to TWS when it alerts" (Green Dildo) moves the linked TWS window to
// the alerting symbol; OFF skips every alert-driven TWS Show. Server-owned
// (persisted, broadcast to all windows as alerttws.data); a symbol you pick
// yourself still moves TWS either way. This is the button's pure view.
export interface TwsFollowView {
  label: string;
  title: string;
  on: boolean;
}

export function twsFollowView(enabled: boolean): TwsFollowView {
  return enabled
    ? {
        on: true,
        label: 'TWS ●',
        title: 'TWS follow ON: scans set to "Send to TWS" (Green Dildo) move the linked TWS window to the alerting symbol. Click to switch off. Picking a symbol yourself still moves TWS.',
      }
    : {
        on: false,
        label: 'TWS ○',
        title: 'TWS follow OFF: alerts no longer move the linked TWS window. Click to switch on. Picking a symbol yourself still moves TWS.',
      };
}

export function parseTwsFollow(d: unknown): boolean {
  return (d as { enabled?: unknown } | null)?.enabled !== false;
}
