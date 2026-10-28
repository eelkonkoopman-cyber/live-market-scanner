import { describe, it, expect } from 'vitest';
import { DILDO_NEWS_WINDOW_MS, dildoNewsSuffix, latestNewsFor, newsBadge, shortAge, spokenAge } from './dildoNews';
import type { NewsItem } from './types';

const T = 1_800_000_000_000;
const item = (id: string, tickers: string[], ts: number, headline = `h${id}`): NewsItem =>
  ({ id, tickers, headline, source: 's', url: '', ts, sentiment: '' });

describe('dildo news', () => {
  it('window is 60 minutes', () => expect(DILDO_NEWS_WINDOW_MS).toBe(3_600_000));
  it('picks the newest headline for the symbol within the window, ignoring later ones', () => {
    const news = [item('a', ['ABCD'], T - 300_000), item('b', ['ABCD', 'X'], T - 10_000), item('c', ['ZZZ'], T - 1000), item('d', ['ABCD'], T + 5000)];
    expect(latestNewsFor(news, 'ABCD', T)?.id).toBe('b');
    expect(latestNewsFor([item('o', ['ABCD'], T - DILDO_NEWS_WINDOW_MS - 1)], 'ABCD', T)).toBeNull();
    expect(latestNewsFor([item('e', ['ABCD'], T - DILDO_NEWS_WINDOW_MS)], 'ABCD', T)?.id).toBe('e');
  });
  it('speaks natural ages', () => {
    expect(spokenAge(1000)).toBe('1 second');
    expect(spokenAge(10_400)).toBe('10 seconds');
    expect(spokenAge(60_000)).toBe('1 minute');
    expect(spokenAge(4 * 60_000 + 59_000)).toBe('4 minutes');
    expect(shortAge(10_000)).toBe('10s');
    expect(shortAge(240_000)).toBe('4m');
  });
  it('suffix only for the Green Dildo scan with fresh news', () => {
    const news = [item('b', ['ABCD'], T - 10_000)];
    expect(dildoNewsSuffix('dildo-1m', 'ABCD', news, T)).toBe('. News headline 10 seconds ago');
    expect(dildoNewsSuffix('dildo-pb', 'ABCD', news, T)).toBe('');
    expect(dildoNewsSuffix('dildo-1m', 'EFGH', news, T)).toBe('');
    expect(dildoNewsSuffix('dildo-1m', 'ABCD', [item('o', ['ABCD'], T - 2 * 3_600_000)], T)).toBe('');
  });
  it('badge: age at alert time by default, headline tooltip', () => {
    const news = [item('b', ['ABCD'], T - 240_000, 'Big FDA news')];
    expect(newsBadge(news, 'ABCD', T)).toEqual({ label: 'NEWS 4m', title: 'Big FDA news' });
    expect(newsBadge(news, 'ABCD', T, T + 60_000)?.label).toBe('NEWS 5m');
    expect(newsBadge(news, 'EFGH', T)).toBeNull();
  });
});
