import { useSyncExternalStore } from 'react';
import { getTheme, subscribeTheme, type ThemeId } from './theme';

// React binding for theme.ts's plain-JS store. useSyncExternalStore (rather
// than a useState+useEffect combo) is what lets a component repaint
// correctly when ANOTHER window changes the theme: subscribeTheme's callback
// can fire from the `storage` event listener installed by initTheme(),
// entirely outside any React event, and useSyncExternalStore is the hook
// React designed for exactly that external-mutation case. There's no real
// server render in this app, so the same getTheme() snapshot doubles as the
// "server" snapshot.
export function useTheme(): ThemeId {
  return useSyncExternalStore(subscribeTheme, getTheme, getTheme);
}
