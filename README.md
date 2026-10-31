# Live Market Scanner

A local, single-user live US stock-market scanner — a self-hosted replacement for
Scanz.com — built on the **Massive** (massive.com) real-time data feed you already
pay for. Signature features: **% change over the last 5 minutes** across the whole
market, and a **news feed** joined to the grid.

> Status: **feature-complete.** Full pipeline — feed → ingest → engine → scan →
> server → **React UI**, plus the nightly RVOL/float `schedule` job — builds,
> passes `go test ./...` + `-race`, and the binary serves the embedded UI. Run:
>
> ```powershell
> $env:MASSIVE_API_KEY = "YOUR_REALTIME_KEY"
> go run ./cmd/scanner   # opens http://127.0.0.1:7878
> ```

## Prerequisites

- **Go** — installed (go1.26.4 via winget; `go.mod` targets go 1.25). Open a *new* terminal so `go` is on PATH.
- **Node 20+ / npm** — present (Node 24, npm 11). For the React UI under `web/`.
- A **Massive real-time API key** (env `MASSIVE_API_KEY`) and, for the RVOL
  baseline, **flat-file (S3) credentials** (env `MASSIVE_S3_KEY_ID` / `MASSIVE_S3_SECRET`).

## Build & run

The React UI builds to `web/dist`, which the Go binary embeds. `dist/` is
gitignored, so build the UI before the binary on a fresh checkout:

```powershell
cd web; npm install; npm run build; cd ..
go build -o scanner.exe ./cmd/scanner
.\scanner.exe                      # serves http://127.0.0.1:7878
```

No env var needed — the app boots without a key and shows a **"🔑 No API key set"**
banner. Click it (or the **⚙ API key** button), paste your Massive real-time key, Save.
It's saved to `~/.marketscanner/apikey` and the feed connects within seconds; on
later launches it's read automatically. Easiest of all: just double-click
**`run.bat`** — stops any old instance, then rebuilds the web UI and both
binaries only when something under `web/src`, `web/index.html`,
`web/package.json`, `web/vite.config.ts`, `cmd`, `internal`, `assets`,
`tools`, `go.mod` or `go.sum` is newer than `scanner.exe`, or either exe is
missing; otherwise it skips straight to launch instead of re-running
`npm run build` plus two `go build`s on every double-click. `run.bat build`
(first argument) forces the rebuild regardless of file times.

### Running it

> User: "The scanner is running from a powershell. Anyway we can do this
> better?" — chose a tray icon + desktop shortcut + start with Windows.

`scanner.exe` is built as a GUI-subsystem app (`-H windowsgui`), so there is no
console window. It lives in the **system tray**: right-click the icon for
**Open scanner / Charts window / Open log file / Quit**. It opens the main
window itself once it is listening, and it is single-instance — launching it
again while it runs just brings the main window forward and exits.

- `run.bat install` — creates a **Live Market Scanner** shortcut on the Desktop
  and in the Startup folder (starts with Windows). Both run `run.bat`
  minimized, so a console only shows in the taskbar while it rebuilds; if a
  build fails a message box says so and the console stays open with the
  details. Re-run it after the first build to pick up the real app icon.
  `run.bat uninstall` removes both shortcuts.
- `run.bat` — stop, rebuild if stale, start, then the console closes.
  `run.bat build` forces the rebuild.
- `run.bat stop` — graceful stop (`taskkill` without `/F`, so the scanner
  flushes and closes its windows); only force-kills after ~8 s.
- `run.bat logs` — live-tails the log file, for when you want the old
  scrolling console.
- Log file: `%USERPROFILE%\.marketscanner\logs\scanner.log`, rotated at 10 MB
  to `scanner.log.1`.

**Native windows (no browser chrome)**: `run.bat` also builds
**`scannerwin.exe`** (`cmd/scannerwin`, Go + WebView2, no CGo) and the scanner
opens the main window through it — a **borderless** window with no title bar, no URL
bar, nothing but the app. Drag it by the subtle ≡ pill at the top edge
(hover for – minimize and ✕ close), resize from the window edges as normal.
The in-app ⧉ Tape / ⧉ Charts / ⧉ Review / ⧉ Signal buttons (and scan-tile
pop-outs) spawn more native windows — one process each, so one wedged window
can never take down the rest. Every pane remembers its own position and size
(`~/.marketscanner/windows.json`, multi-process-safe, clamped back on-screen
if a remembered monitor is gone). Direct launch:
`scannerwin.exe --pane main | --pane tape --sym AAPL | --pane charts |
--pane scan --id <scanId> | --pane review | --pane signal [--sym ATYR]`.
Ordinary browsers keep working unchanged — the shell is just another client
of the same server.

Dev loop for the UI (hot reload): run the binary (backend on :7878), then
`cd web; npm run dev` (Vite on :5173, proxies `/stream` to the backend). After
changing the chart engine in AfterBell, run `npm run sync:charts` from
`web/` first to pull the change into the scanner (see the AfterBell chart
engine bullet under **How it works**).

## Locked decisions

| Area | Decision |
|---|---|
| Data source | Massive (massive.com), Polygon.io-API-compatible; real-time entitlement |
| Deployment | Local desktop, single user — one Go binary serving a React UI on `127.0.0.1` |
| Universe | "Regular securities": common stock (CS) + ADRs (ADRC) + ETFs; exclude warrants/units/rights/preferreds/ETNs |
| Sessions | Full extended hours, 04:00–20:00 ET; metrics anchor to 04:00, plus **per-session** metrics (`chgSess`/`sessHigh`/`sessLow`/`sessVol`/`sessVwap`) scoped to the current pre/regular/post sub-session, and a per-scan **Active session** gate |
| Alerts | Sound only (played in the browser via Web Audio; backend emits alert events) |
| Filters | RVOL (intraday, cumulative-by-time-of-day) + float / shares outstanding |
| Shell | `net/http` + `embed.FS` (not Wails) — the WS-push design makes Wails' bridge redundant |

## How it works

```
Massive WS (A.*) ─► ingest ─► sharded engine ─► merger ─► scanner ─► WS hub ─► React UI
                    (parse,    (ring buffers,    (4 Hz     (filter,            (grid + news
                     route)     single-writer)   merged     diff, alerts)       + sound)
                                                 snapshots)
Massive REST ─► snapshot seed · news poll                    SQLite ─► nightly job
flat files  ─► RVOL baseline backfill                        (defs, rvol, float, calendar)
```

- **5-min change**: each ticker keeps a `[960]float32` per-second ring (1 Hz
  forward-filled for sparse names); `chg5m = (last − ring[now−300]) / ring[now−300]`.
  Same ring yields 1-/15-min change. ~26 MB for the whole market.
- **Single-writer engine**: `N = GOMAXPROCS` shards, each a goroutine owning its
  tickers' state via one `select` loop (streaming update / 1 Hz clock / snapshot
  request) — no per-ticker locks. The merger allocates a **fresh** snapshot per
  generation (no buffer reuse).
- **Per-session metrics**: alongside the 04:00-anchored day metrics, each ticker
  also tracks `chgSess` (% vs the session-open price — regular anchors to the
  official 09:30 open), `sessHigh`/`sessLow`, `sessVol`, and `sessVwap`, all reset
  at each 04:00 → 09:30 → 16:00 boundary by the session manager. Scans can also be
  gated to a single session (e.g. "biggest pre-market movers") via the **Active
  session** dropdown, which compiles to a `session ==` predicate.
- **5-minute candle break**: each ticker builds clock-aligned 5-min candles
  (09:30, 09:35, …, extended hours included) from every aggregate's own
  `StartMs` — not the snapshot clock, and not `EndMs`, either of which would
  attribute a candle's final-second print to the *next* candle and fake a break
  the instant it opens. `brk5mAge` = whole seconds since the current candle
  traded strictly below the previous candle's low (empty = not broken), so
  `brk5mAge < 60` means "broke in the last minute" and it works as a plain
  numeric filter in the editor. `vsPrev5mLow` (% vs that low) and `prev5mLow`
  (the level) sit alongside. "Previous candle" follows chart convention: the
  last completed candle that traded. Guards: a candle already in progress when
  the scanner started is never used as a reference; once the clock enters a new
  5-min window the old break stops reading as live until the new candle trades;
  a late print for the previous candle revises its low (and can revoke a break).
  **Close variant:** `brk5mCloseAge` / `vsPrev5mClose` / `prev5mClose` measure
  the same break against the previous candle's CLOSE — its latest print by
  timestamp, so an out-of-order agg can't overwrite it. A candle in progress at
  startup has an unknowable low but an exact close, so the close variant arms one
  candle sooner after a restart. It fires far more often than the low variant (a
  new candle usually opens at the previous close; one downtick counts), so pair
  it with a depth filter like `vsPrev5mClose < -0.5`.
  **Long side:** `brk5mCloseUpAge` is the mirror — the current candle traded
  strictly ABOVE the previous close (from each agg's High, so an intra-second
  poke counts). `vsPrev5mClose` / `prev5mClose` serve both sides (positive =
  above); pair with e.g. `vsPrev5mClose > 0.5`. A volatile candle can break both
  ways and sit in the long and short scans at once.
- **Key level breaks** (`brkPmhAge` / `vsPmh`, `brkPrevAhAge` / `vsPrevAh` /
  `prevAhHigh`): seconds since price last broke ABOVE today's pre-market high or
  the previous trading day's after-hours (16:00-20:00 ET) high. A break is the
  first trade above the level while armed; a 1-second bar closing back at or
  below re-arms it, so a reclaim counts again. A stock already above a level
  when the level arrives (restart, late seed) is not reported as a fresh break.
  **Pre-market high** comes from the 09:30 freeze, but only when it is complete:
  the engine must have watched since the 04:00 open (`Engine.preFromOpen`) or
  prefill must have seeded it from full-session REST bars, because a partial
  high is too low and would fake breakouts. **Previous after-hours high** comes
  from `internal/prevah`: the engine rarely sees yesterday's after-hours (the
  20:00 snapshot exists only if the app was open at 01:00 UK, and can be
  partial), so levels are fetched per symbol from Massive minute bars for that
  exact date, for up to `SCANNER_PREVAH_CAP` (500) universe names per day,
  ranked like prefill and topped up every 10 min; the first 429 stops it for the
  day. AfterBell's market database was considered and rejected: it keeps minute
  bars only for symbols AfterBell touched, and it is PGlite inside AfterBell.
  Live check: `SCANNER_LIVE_FEED_TEST=1 go test ./internal/prevah -run TestLivePrevAh -v`.
  **5-minute timeframe** (`brkPmh5mAge` / `brkPrevAh5mAge`, used by the PM High
  Break 5m / Prev AH High Break 5m scans): the same levels, but a break is a
  5-minute candle CLOSING above the level while armed (a 5m close at or below
  re-arms); wicks that close back under don't count. Each candle is evaluated
  once, when the next candle's first print arrives or the 1 Hz clock passes the
  window end + 3 s (`tick5m`). On real bars for 10 active names (2026-10-02),
  62 trade-level pre-market-high breaks became 21 five-minute close breaks, all
  matching an independent computation from the bars
  (`SCANNER_LIVE_FEED_TEST=1 go test ./internal/engine -run TestLiveLevel5mBreaks -v`).
- **1-minute 9 EMA loss** (`brkEma9Age` / `vsEma9` / `ema9`): seconds since a
  1-minute candle CLOSED below the 1-minute 9 EMA right after one closed at or
  above it. It is the charts window's own EMA (engine `emaArr`: SMA seed over the
  first 9 closes, k = 2/10) over completed candles that traded (empty minutes
  skipped, like the chart), with candles keyed on each agg's StartMs and closed
  by the next minute's print or by the 1 Hz clock 3 s after the minute ends.
  The EMA runs on through the 04:00 reset (the chart's EMA includes yesterday);
  only the signal is cleared. The scanner's EMA starts when it starts watching a
  stock, so nothing is shown until 30 completed candles, when the seed's
  influence has decayed below 1%. Verified on real bars: replaying a full day of
  10 active stocks through a fresh scanner EMA matched all 999 of the chart's
  post-warm-up 9 EMA losses (`SCANNER_LIVE_FEED_TEST=1 go test ./internal/engine
  -run TestLiveEma9MatchesChart -v`). Active stocks lose the 1m 9 EMA ~100
  times a day, so pair it with other filters.
- **Pre-market volume vs 10-day average** (`pmVolX` / `adv10`, the "PM Volume
  3x" scan): pre-market volume so far divided by the average FULL-DAY volume of
  the last 10 trading days (today excluded). The average comes from
  `daily_close_history` (Massive's whole-market daily bars, kept current by the
  daily-trend job; their volume includes pre-market and after-hours) via
  `DB.AvgDailyVolume`, loaded by `cmd/scanner/adv10.go` every 5 min (a ticker
  needs 5+ traded days in the window). During pre-market the numerator is the
  day volume, which the periodic snapshot re-seed keeps whole across restarts;
  after 09:30 it is the frozen pre-market volume, only when that freeze is
  complete. Unlike `rvol`/`avgVol`, this does NOT depend on the self-built RVOL
  baseline. Verified live 2026-10-05: engine values matched an independent
  snapshot + database computation within 1.4%.
- **RVOL**: trailing 20-session mean of cumulative volume in 192 five-minute
  buckets from 04:00 ET; live RVOL = today's cumulative ÷ baseline at the current
  bucket. Seeded from flat files, then self-maintaining from the engine's own
  captured curve.
  **Curve saving (fixed 2026-10-05):** the day's curve used to be saved only by
  the 20:05 ET nightly job, which almost never ran because the app is usually
  closed by then, so the baseline sat on 1-3 days and RVOL/avgVol were null
  everywhere. Now `schedule.flushToday` saves today's observed buckets every 10
  minutes between 04:05 and 20:00 ET and once more on shutdown (incremental, so
  it beats run.bat's force-kill), the repo MERGES with MAX per bucket (a
  restarted process or the nightly flush never wipes another's buckets) and
  never stores non-positive (unobserved) buckets, the recompute ignores legacy
  zero rows, and `ensureBaselineFresh` recomputes at startup through the
  previous session (never today). Status is "ready" only once 5 sessions are
  stored, matching the engine's minimum, so the status bar no longer claims
  ready while RVOL is null.
  **Minute-bar backfill (`internal/rvolfill`, 2026-10-05):** so RVOL works
  without first recording 5 sessions live, the worker rebuilds the last 10
  baseline days from Massive 1-minute bars (one range request per symbol) for
  the scans' current symbols first, then the top snapshot movers; it starts 20s
  after launch and re-checks every minute (new movers), caps at 2,000 symbols
  per ET day (`SCANNER_RVOLFILL_CAP`, 0 disables), stops for the day on the
  first 429, and fetches each (symbol, day) once ever (`curve_filled` markers).
  A rebuilt curve is the minute bars' running total plus the daily volume's
  excess over them from the 16:00 bucket on: checked against the raw trade
  tape, finished days' minute bars match it within ~1-5% before 16:00, and the
  excess is the closing auction + late prior-reference prints (0-1% of the day
  on small caps, 17-61% on established names such as PTC/PCVX), so scaling the
  whole day instead would inflate the morning baseline. Against the engine's
  own recorded 2026-09-28 curve the rebuilt values matched with median ratio
  1.0004 at 17:10 and exactly at 20:00 (59 names). Rebuilt days MERGE with
  live ones (larger per bucket wins), and only the rebuilt tickers' baseline
  rows are recomputed and swapped in (`Scheduler.RefreshBaselineFor`), never
  the whole market. Today's minute bars are never used: in pre-market they run
  well short of the tape until late TRF prints are folded in.
- **News latency**: the two sources poll on **independent cadences**, because
  news-detection latency is set by the *real-time* source alone. Benzinga is
  polled every **1s** (`SCANNER_NEWS_POLL`), the hourly-recency reference feed
  every **60s** (`SCANNER_NEWS_REF_POLL`). Both are floored at 0.5s, and an
  HTTP 429 arms an exponential backoff (5s → 2 min) that pauses polling rather
  than hammering through it. Note the price path does **not** wait on this: a
  catalyst move is detectable in ~1.3s via `chg1m`/`rvol`, so gating a scan on
  `news == true` deliberately trades that away for the poll interval.

  Two defenses against Benzinga's REST **indexing lag** (articles become
  queryable seconds after their publish timestamp — the price pop routinely
  beats the headline into the index):
  1. **Cursor overlap**: every poll re-queries the trailing 2 minutes instead
     of exactly-after the newest publish TS seen. Without this, an article that
     indexes late gets leapfrogged by the cursor and is *never fetched at all*
     (regression test: `TestLateIndexedArticleIsNotSkipped`). Duplicates are
     dropped by the existing seen/URL dedupe.
  2. **News chaser**: when a symbol enters a scan with no headline published in
     the last 10 min, the per-ticker fetch retries on a decaying schedule
     (3/6/12/24/45s ≈ 90s) until the headline lands — then it flows through
     MarkNews (grid dot + `newsAge`) and auto-summarization like any other
     article. Capped at 4 concurrent chasers; stops on 403/429; disable with
     `SCANNER_NEWS_CHASE=0`.
- **RTPR wire poller (optional, off by default)**: rtpr.io aggregates the four
  major PR wires (Business Wire, PR Newswire, GlobeNewswire, AccessWire) —
  the sources company press releases actually originate from. Benzinga's own
  write-up of a release can lag the wire by minutes (observed: 9 min on GLMD),
  so this closes that gap. Token via `SCANNER_RTPR_TOKEN` or a one-line
  `~/.marketscanner/rtprtoken` file; without one nothing changes. Their feed
  is body-free by design (refs only) and the permalink returns rendered HTML,
  not JSON (confirmed live) — `internal/rtpr` polls the ref feed every 3s,
  follows each new permalink, and parses the page. All requests share a
  50/min budget (their cap is 60) and articles land in the same
  dedupe/cache/archive as every other source (`via='rtpr'`; ids `rtpr<id>`).
  Refill is smoothed (one token every `window/max`, not `max` dumped once a
  minute) and each article is delivered to the rest of the app the instant
  its OWN fetch completes, not batched until the whole poll's backlog
  finishes — both fixed after a real incident where a burst of 58
  simultaneous 08:00 ET releases delayed two names' news by 90-96s (the tail
  of that batch held everything hostage behind the shared per-minute budget).
  **Fetch priority**: when multiple new articles are pending in the same
  poll, tickers priced under any ENABLED scan's price ceiling (a `last <
  N`/`last <= N`/`last between _ and N` condition anywhere in its filter,
  see `scan.Scanner.IsPriority`) are fetched first, using price data the
  scanner already holds live — no RTPR-side rule needed, since their rule
  DSL has no price field at all (content/ticker/exchange/session only).
- **RTPR Impact Score (Beta)**: a 0-100 catalyst score RTPR assigns to each
  wire release ~45s after publication (an enrichment of an article we
  typically already have, not a speed signal) — shown as a colored badge on
  the news card (green bullish / red bearish / gray neutral, plus the event
  type, e.g. "92 · FDA APPROVAL"). Requires the SAME RTPR token, but also
  requires the account to separately enable **Connect → "Alert me on
  high-impact PRs" → WebSocket delivery** in RTPR's own dashboard — until
  that's on, `internal/rtpr`'s alerts-socket consumer just stays quiet.
  Scores persist to the archive (`news_articles.impact_score/impact_tier/
  event_type/impact_direction`) so they survive restarts and show up in a
  symbol's history too, not just the live feed.
- **Newsfilter recorder (optional, record-only, off by default)**: newsfilter.io
  ($65/month) is being evaluated as a cheaper stand-in for RTPR ($139).
  `internal/newsfilter` connects to its websocket and only RECORDS when each
  article arrives (one JSONL line per article in a UTC day file under
  `~/.marketscanner/newsrace/`); nothing reaches the news panel, alerts, the
  autosummarizer, health, the WS hub or the database, and RTPR is untouched.
  Token via `SCANNER_NEWSFILTER_TOKEN` or a one-line
  `~/.marketscanner/newsfiltertoken` file; without one the package never
  starts. `SCANNER_NEWSFILTER=0` is an explicit off switch that beats the
  token (the preview configs in `.claude/launch.json` set it, so a preview
  never opens a second stream on the key). Compare the two feeds with
  `go run ./cmd/newsrace` (`-days 30`, `-maxpx 20`; read-only on the
  database, never touches `scanner.exe`): it pairs the same release on both
  sides and prints coverage both ways per wire plus the delivery lead
  (p10/p50/p90, overall and for stocks at or under `-maxpx`), and gives a
  verdict only after 200 matched pairs. The protocol was built from their
  docs alone, so the first run also keeps its first 200 frames verbatim in
  `raw-frames.jsonl` next to the day files.
- **SEC EDGAR 13G/13G-A watcher (always on, no token)**: ownership-stake
  disclosures (an investor crossing 5% of a company) are filed by the
  ACQUIRING investor, not the company, so they frequently have zero
  accompanying press release — wire services and Benzinga never see them.
  Observed: stocks moving in after-hours on a 13G with no headline anywhere
  in the pipeline. `internal/edgar` polls EDGAR's public "current filings"
  feed every minute, filtered server-side to `SC 13G`/`SC 13G/A` only, resolves
  the issuer's CIK to a ticker via EDGAR's own `company_tickers.json`
  (refreshed daily), and reads the exact ownership percentage from the
  filing's structured `primary_doc.xml` (every 2023+ filing ships one — no
  HTML/PDF scraping). Surfaces as a normal news card, e.g. *"Baillie Gifford &
  Co discloses 2.3% stake in Atlassian Corp (Schedule 13G/A)"*, source "SEC
  EDGAR", `via='sec13g'`, id `sec13g<accession>`. Free and public (gated only
  by SEC's fair-access User-Agent policy), so this needs no key and is never
  optional — no way to disable it short of removing the source.
- **SEC EDGAR all-filings watcher (scanner symbols)**: everything else EDGAR
  publishes — 8-Ks, offerings, insider Form 4s, 13Ds, delistings — but only
  for symbols the user's scans have actually surfaced today
  (`scan.Scanner.ActiveSyms`: currently matched ∪ entered earlier), so the
  volume stays sane while missing nothing on names being watched. Polls the
  same public feed every 30s with no form filter, resolves CIK→ticker via
  the shared daily map, skips 13G/13G-A (the specialized watcher above owns
  those), and dedups by accession. Headlines carry a plain-English label —
  *"SEC filing: 8-K (material event) — Acme Corp (ACME)"* — and
  dilution-class forms (424B\*, S-1, S-3, F-1, F-3) are tinted negative via
  the existing sentiment styling, so an offering on a runner jumps out.
  **The pending pen** closes the timing hole the adversarial review proved
  real: a filing usually *precedes* the move that surfaces the symbol, so a
  resolved filing for a not-yet-surfaced name is held (bounded FIFO, 1024)
  and emitted with its true filing timestamp the moment the symbol enters a
  scan — not permanently lost to the dedup ring. `SCANNER_EDGAR_ALL=0`
  disables. Loss past a full 100-entry page in one 30s poll is permanent and
  logged (the feed only moves forward); the cadence bounds it.
- **English-only news (default)**: ~53% of the Benzinga WebSocket stream is
  translated editions (ja/ko/it/zh/es/de/fr/pt — measured against the real
  archive), and no server-side language filter exists in Benzinga's or
  Massive's APIs. Two layers: the WS decoder drops `benzinga_wire_*`-typed
  frames outright (the undocumented per-frame `type` marker), and
  `internal/lang.Foreign` gates EVERY source at the poller's single merge
  door — Unicode-script share for CJK/Cyrillic/etc., per-language stopword
  duels for Latin-script languages, with a case-as-evidence rule (in a
  mixed-case headline only fully-lowercase tokens count as foreign
  stopwords) that the adversarial sweep forced: without it, ticker lists
  ("Movers: DE, LA…"), "Del Taco", "Las Vegas Sands", and ALL-CAPS
  Latin-American EDGAR filers (SABESP's 6-Ks) were all wrongly dropped.
  `SCANNER_NEWS_FOREIGN=1` keeps foreign articles.
- **News archive**: every accepted article (push, poll, or chase — tagged in
  the `via` column) is persisted to SQLite on first arrival, with both its
  publish and receive timestamps, plus every scan entry (`scan_entries`).
  This makes three things work: news history beyond the ~800-item in-memory
  cache (Summarize falls back to the archive after eviction instead of
  failing), per-session feed-latency analysis (`received_ms - published_ms`),
  and news-vs-entry timing (join `scan_entries` to `news_article_tickers`).
  Retention 30 days by default (`SCANNER_NEWS_KEEP_DAYS` overrides); ~10-20
  MB/day. Writes are async off the feed path through a single writer.
- **Benzinga push WebSocket (optional, off by default)**: Benzinga's own
  `wss://api.benzinga.com/api/v1/news/stream` delivers articles ~25ms from
  publication — no REST index lag at all. It is a **separate product** from the
  Massive Benzinga add-on and needs a Benzinga-issued token (contact Benzinga;
  the Massive key does not open it). Provide the token via
  `SCANNER_BZ_WS_TOKEN` or a one-line `~/.marketscanner/bztoken` file (same
  conventions as the Massive `apikey` file) and restart:
  `internal/bzstream` connects with capped backoff, replays the
  server's 100-message cache on every (re)connect, and feeds the same
  dedupe/cache as the REST poll (same `bz<id>` scheme — the two sources can
  never double-emit; the poll stays on as fallback and gap repair). Without
  the env var nothing changes.
- **News**: two REST sources merged + deduped (no WS news channel exists):
  **Benzinga** `/benzinga/v2/news` (real-time, full article body; needs the
  Benzinga expansion on the Massive plan — auto-detected, re-probed every 10 min
  if not entitled) and the generic `/v2/reference/news` (all plans, hourly,
  headline+description). Both sources' headlines/teasers arrive HTML-escaped and
  are entity-decoded on the way in (bodies stay raw HTML — the summarizer strips
  and unescapes those itself). Verify the live feed any time with
  `SCANNER_LIVE_FEED_TEST=1 go test ./internal/feed -run TestLiveBenzinga -v`
  (REST only — safe to run while the scanner is live). Headlines are indexed per
  ticker and stamped onto scan rows (`news` fresh ≤30 min, `newsAge` seconds —
  both filterable), and shown in a panel with a ticker filter + "Scan"
  (active-scan-symbols-only) toggle.
- **News summaries — local by default, Claude on request** (user: "We use the
  Claude API to summarize the news. Can we have a build in engine so we dont
  need to use it?"). Two engines, mirroring the tape reader's own Local/Claude
  toggle: **`internal/summarize.LocalSummarizer`** is a free, offline,
  extractive/rule-based summarizer — lead-sentence extraction, dollar/share/
  percentage figures pulled out verbatim, and a keyword-lexicon "Signal:" line
  (dilution/offering, guidance, earnings, FDA/regulatory, M&A, analyst action,
  halt/risk — with a negation guard so "does not plan an offering" is never
  flagged as dilutive, and a bounded word-gap matcher so "cuts full-year
  guidance" still matches even with words in between). It deliberately **never
  emits a confident directional call** — no fabricated "Stance:" line the way
  Claude's does, and mixed news ("beats estimates but cuts guidance") surfaces
  both signals rather than silently picking one side. The real
  **`summarize.Service`** (Claude, unchanged) is still there: the ✨ button
  becomes **✨ Ask Claude** once a local summary is showing, is never hidden,
  and calling it always reaches Claude and overwrites the cache with the real
  read — every summary is tagged **LOCAL** or **CLAUDE** so you always know
  which you're looking at. Source text and caching (SQLite `news_summary`,
  keyed by article ID) are shared unchanged between both engines. Every
  Claude request (summaries, tape reads, setup reads) goes to **Claude Opus 5
  at medium effort** (`SCANNER_CLAUDE_MODEL` and `SCANNER_CLAUDE_EFFORT`
  override either; an unknown effort falls back to medium), with a
  16,000-token cap on thinking plus reply. If Opus 5's safety classifiers
  decline a request, a server-side fallback re-serves it on Opus 4.8 inside
  the same API call.
- **Auto-summaries**: when a symbol newly enters a scan, the day's news for it
  (since 16:00 ET the prior day — after-hours filings included) is fetched via
  the Benzinga per-ticker endpoint and the newest articles are summarized
  automatically and broadcast, so summaries are waiting before any click.
  `SCANNER_AUTOSUM_ENGINE=local|claude` picks which engine runs this
  background path (default `local` — this is the part that used to spend
  Claude API tokens on every scan entry with zero clicks; it now spends
  nothing). The old cost-containment guardrails (10-min per-symbol cooldown,
  `SCANNER_AUTOSUM_MAX`/`SCANNER_AUTOSUM_DAILY`) still apply exactly as before
  when set to `claude`; with `local` they're widened to a generous
  self-protection floor rather than a spend cap, since the local engine has no
  cost to contain. `SCANNER_AUTOSUM=0` disables auto-summaries entirely either
  way. The manual ✨ button (any headline, on demand) is unaffected by this
  setting — it always asks Claude, real API cost, exactly as before.
- **Scan visibility**: every tab shows its **live match count** (from the 1 Hz
  status frame, `StatusMsg.Counts`) so an empty grid reads as "this scan
  matches nothing right now", not "the feed is broken". The Activity Log pools
  hits from **all** scans — rows are labeled with the scan *name*, hits from
  non-active scans render dimmed, and a "This scan only" toggle filters them.
  The grid's empty state names the scan and restates its criteria (`newsAge`
  shown as a duration). Scan tabs are sorted by name (server-side), not map
  order.
- **Workspace layout** (Scanz-style): left = scan grid + Activity Log (every
  scan entry with time/price/count/**News** — whether the row already had a
  fresh (≤30 min) headline the moment it entered, a static dot vs. the grid's
  live pulsing one, since this is a historical fact about the entry, not a
  live state; click selects); right = docked symbol panel
  (stats header + full chart with timeframes/indicators/levels) + news panel
  that follows the selected symbol. News cards published within the last
  **5 minutes render as a brighter accent-tinted box** (a 30s panel clock dims them
  as they age past the boundary — decay is real, not just applied-on-arrival);
  each card shows relative age stacked over the actual publish time, with the
  full date on hover. Boxes and grid/log columns are all
  drag-resizable and persist to the browser's local storage. Metric column
  headers also **drag to reorder** (order lives in the scan def, so it persists
  server-side per scan) and **click to sort** — including boolean columns like
  `news`, which sort has-news-first. **Shift+click sets a secondary sort** that
  breaks ties on the primary (shown as `▼₂`); the scan editor exposes the same
  as a "then by" row. Changing the primary keeps the tiebreak, and promoting the
  tiebreak to primary drops it (a field can't break its own tie). The header
  gestures coexist: click sorts, shift+click sets the tiebreak, a drag past the
  move threshold reorders, and the resize handle suppresses drag so widening
  never turns into a reorder.
- **UX batch (2026-09-13, user: "the scanner is great however the user
  experience is not that great")**: a pass of usability fixes across the grid,
  scan editor, status bar and pop-out windows.
  - **Field registry** (`web/src/fields.ts` + `formatField` in `format.ts`):
    every scan field now has a `FieldDef` — short `label` ("5m %", "PM % ADV"),
    full `name`, `unit`, `kind`, `group` and an optional `hint` — keyed to the
    same `core.Field*` wire constants. The grid header shows the label with
    the wire key + hint in its tooltip (`fieldTitle`); `formatField` renders
    every cell by `kind`: signed percents ("+3.42%", toned pos/neg), prices at
    2dp (4dp under $1), compact volumes ("6.8M"), ratios ("2.58x"), durations
    ("5m"), `flagState` as none/coiling/breakout, and `shortAvail` floored at
    ">10M" when the row is capped. The scan editor's field dropdowns
    (`filterableFields()`) and its column chooser (`columnableFields()`, now
    rendered grouped by `FIELD_GROUPS` via `fieldsInGroup`) read the same
    registry, as does the grid's empty-state criteria line (`LiveGrid`'s
    `describe()`, via `fieldLabel`).
  - **Hover-freeze** (`web/src/gridHold.ts` + `LiveGrid.tsx`): while the mouse
    sits over a scan's list, `mergeHeldOrder` holds the row ORDER (the frozen
    order first, genuine new arrivals appended at the bottom) while cell
    values keep painting live; a row that left the scan renders dimmed from a
    last-seen cache instead of vanishing, and the grid title appends "· held".
    Released on `mouseleave` or after 10s (`HOLD_IDLE_MS`) with no mouse
    movement, whichever comes first.
  - **Scan editor alerts** (`ScanEditor.tsx`): the alert section is now a real
    editor instead of a fixed "on enter" — `alertOn` switches between
    `AlertOnEnter` and a `cross` trigger (field + above/below + numeric
    level), the config the seeded "Intraday Flags"/"Daily Flags" scans have
    always needed (`flagState`/`iflagState` crossing above 1.5, the
    coiling→breakout flip); before this fix, `save()` unconditionally wrote
    `on: "enter"`, so saving any scan — even just to tweak a column — silently
    destroyed a cross trigger already in place. `save()` now also refuses a
    cross alert with a blank/non-numeric level (would otherwise arm at `NaN`
    server-side and never fire), plus cooldown seconds, "require re-entry",
    and a ▶ Preview button that plays the chosen sound. The scan editor and
    the API-key prompt now share `Modal.tsx` for dialog chrome — Escape
    closes, Tab is trapped inside the panel, and focus is restored to
    whatever had it before the dialog opened. Delete requires two clicks (the
    button reads "Confirm delete" on the first).
  - **Status bar** (`StatusBar.tsx` + `marketClock.ts`): an ET clock ticking
    once a second, subtitled with a countdown to the next session boundary
    (`nextBoundary`/`countdown`; plain Mon–Fri 04:00/09:30/16:00/20:00
    schedule, no holiday calendar); a TWS link pill (linked/waiting/off) for
    `StatusMsg.TwsLink` — the value was already on the wire, never shown; and
    a sources pill fed by the new `StatusMsg.Sources` map (Massive feed,
    Benzinga push, RTPR wire, RTPR impact scores, SEC EDGAR — `off` means not
    configured and is excluded from the health count, a source counts as
    failing only 60s after it started (`FAILING_GRACE_MS`), and the tooltip
    lists every source's state and last error). Backed by `internal/health.Tracker`
    (`SourceOK`/`SourceFail`/`Off`) implementing the new `core.HealthReporter`
    interface (`internal/core/wsmsg.go`), wired into `bzstream`, `rtpr` (both
    the wire poller and the impact-score alerts socket) and `edgar` from
    `cmd/scanner/main.go`'s `run()`, plus the `status` closure deriving
    Massive's own entry straight from `ing.State()` (no reporter threaded
    through `internal/ingest`). Also new: a symbol search box (`/` or Ctrl+K
    focuses it from anywhere, Enter calls the same `openChart` a grid-row
    click does, Shift+Enter also spawns a `?pane=tape` window) hidden on the
    charts/review pop-outs; the settings button is relabeled **⚙ API key**.
  - **Toasts** (`Toasts.tsx`): auto-dismiss after 6s (`TOAST_MS`) or on click;
    a repeat of the newest message replaces it (fresh id, so its dismiss
    timer restarts) instead of stacking a duplicate; capped at 4 visible.
  - **Charts monitor cells** (`MonitorChart.tsx` + `store.chartErrBySym`): a
    chart request that errors, or never comes back, now shows "Couldn't load
    SYM — reason" with a Retry button instead of "Loading…" forever.
    `internal/server/hub.go`'s `chart.get` (and `fundamentals.get`) handlers
    now stamp the failure with the symbol (`core.ErrorMsg.Sym`) so a
    multi-cell monitor grid blames only the right cell — `ws.ts` falls back to
    blaming every still-pending cell if an older server sends no `sym`. A
    15s client-side watchdog (`CHART_TIMEOUT_MS`) covers a reply that never
    arrives at all.
  - **Cross-window follow** (`SymbolPanel.tsx`, `store.ts`): the main
    window's symbol panel gained a **🔗 Follow** toggle (`followOthers`,
    persisted to `localStorage` as `mktscanner.mainFollow.v1`) that adopts
    whatever symbol was last selected in any window, via the same
    `storage`-event mechanism the tape pop-out has always used — previously
    the main window was the one place that never followed (see the corrected
    claim in the Morning review bullet below).
  - **Pop-out header stats stay live** (`SymbolPanel.tsx`,
    `TapeReaderView.tsx`): when the selected symbol isn't in any of a
    window's tiled scans (a tape pop-out, or a symbol picked via cross-window
    follow), the header stats now re-request `row.get` every 2s instead of
    showing whatever the one-off snapshot said at selection time.
  - **Tape window layout** (`styles.css`, the `.tape-reader*` rules): the
    Level 2 ladder and time & sales columns now actually scroll inside their
    own height instead of sizing to content — a wrapping flex container had
    let `.tape-reader-scroll` grow to its full content height (measured
    2909px in a 1060px window) and paint over the CVD/tape-reads strip below;
    fixed with `flex-wrap: nowrap` down the
    `.tape-reader → .tape-reader-cols → .tape-reader-col` chain, and narrow
    windows now stack the two columns via an explicit `max-width: 640px`
    media query instead of relying on wrap.
- **AfterBell palette (2026-09-14, user: "I`m developing AfterBell with its
  own colour palette. I want the same colour palette for the scanner.")**:
  the scanner adopted AfterBell's own six-theme colour system (warm / paper /
  slate / midnight / forest / plum) in place of its old fixed indigo
  palette — kept in sync by hand via a documented resync recipe rather than
  by sharing code or storage with that separate app.
  - **Theme tokens** (`web/src/afterbell-themes.css`): a straight copy of
    AfterBell's six `[data-theme]` token blocks (`D:\Blackthorn Live\src\app.css`)
    — warm ("AfterBell Dark", the default, bound to `:root`), paper
    ("AfterBell Bright", the only light theme), slate ("AfterBell Slate"),
    midnight, forest, plum — with every token renamed `--ab-*` so it can
    never collide with the scanner's own semantic names. The file's header
    comment carries the resync recipe: paste AfterBell's six blocks in
    again, run `sed -E 's/--([a-z])/--ab-\1/g'`, then reapply the
    scanner-only additions the sed won't produce. Every block carries those
    same additions: `--ab-shadow-rgb`/`--ab-shadow-k` (a colour+strength
    pair for drop shadows — pure black at full strength on the five dark
    themes, a soft warm-grey at 15% strength on paper so shadows don't read
    as heavy black smudges on a light background), `--ab-on-accent`/
    `--ab-accent-text`/`--ab-warn-text` (WCAG-legible stand-ins for
    text-on-solid-accent and for accent-/warn-coloured text on the normal
    background — on AfterBell Bright the plain accent/warn colour is only
    2.9:1 / 2.35:1 against cream, both failing WCAG AA, so these substitute
    near-black ink and a 70/30 accent/ink blend for 4.7–5.4:1 there, while on
    the five dark themes they just equal the existing stock tokens), and a
    per-block `color-scheme` (AfterBell sets it once globally; here every
    theme sets its own so native controls and scrollbars render correctly on
    paper too).
  - **Semantic tokens** (`web/src/styles.css`): no longer contain a single
    literal colour — `--bg`, `--fg`, `--accent`, `--pos`, `--neg`, … all
    derive from the active theme's `--ab-*` triplets, so flipping
    `data-theme` on `<html>` re-colours the whole app for free. Fresh news
    cards, active scan tabs, chips and focus rings all switch from the old
    hardcoded indigo to the theme's own accent (gold by default). Layout,
    type (Plus Jakarta Sans) and spacing are untouched — only colour was
    adopted from AfterBell, not its fonts.
  - **Theme selection** (`web/src/theme.ts` + `useTheme.ts`): the six ids,
    names, descriptions and swatches are copied straight from AfterBell's own
    settings page (`D:\Blackthorn Live\src\routes\settings\+page.svelte`'s
    `THEMES` list). Applying a theme sets
    `document.documentElement.dataset.theme` and writes
    `localStorage['mktscanner.theme.v1']`; every other open window (main +
    every pop-out) follows via the same `storage`-event mechanism the tape
    pop-out's follow mode already used. The scanner can't read AfterBell's
    own cookie-based theme setting (different origin, partitioned storage),
    so it keeps its own independent setting, defaulting to the same theme
    (`warm`) AfterBell itself ships with. `web/index.html`'s inline pre-paint
    script applies the stored theme before React boots so there's no flash
    of the default palette, and the status bar's new swatch-disc
    `ThemePicker` (`StatusBar.tsx` + `theme-picker.css`) opens a menu of all
    six themes — Escape or an outside click closes it, arrow keys move
    between rows.
  - **Charts** (`web/src/chartTheme.ts`): maps the tokens into the chart
    engine's colours exactly like AfterBell's own `blackthornChartColors` —
    background, grid, axes, gain/loss candles and volume, crosshair, legend
    — and `MonitorChart.tsx`/`CandleChart.tsx` re-apply the mapping live on
    theme change via `applyOptions({ colors })` rather than rebuilding the
    chart. The prev-close line deliberately keeps using the theme's warn
    colour (not gain/loss) so it stays visually distinct from the candles
    even in Forest, whose accent is a sage close to the gain colour; the
    inline per-row chart (`CandleChart.tsx`) keeps a transparent background
    so the row's own card shows through. Session shading (`US_EQUITIES`) and
    indicator line colours are left at the chart engine's defaults, the same
    thing AfterBell does.
  - **Native shell** (`cmd/scannerwin/overlay.go`): the borderless window's
    drag pill reads the same `--ab-*` tokens, with the original literal RGB
    values kept as each `var()`'s fallback for pages that don't define them
    at all (Edge's connection-error page when the backend is down being the
    whole reason the pill exists).
- **AfterBell chart engine (2026-09-14, user: "I want to use the AfterBell
  Charting engine. That engine is updated.")**: the scanner's charts no
  longer use the August bundle built from `D:\TradingView`
  (`web/src/vendor/blackthorn-charts.mjs` + a hand-written `.d.ts`, both
  deleted) — they now compile AfterBell's engine TypeScript source directly
  from `web/src/vendor/blackthorn-charts/`. AfterBell's own copy has been
  the engine's source of truth since 2026-09-12 and already contained the
  scanner's earlier engine additions (live `update()`, manual price scale,
  price lines, legend toggle), so real types now come from the source
  instead of a hand-maintained stub — the scanner's calls are type-checked
  against the actual engine. Bundle size grew about 12.5% (the engine ships
  drawings, indicators and gesture code the scanner doesn't use yet).
  - **Sync** (`web/scripts/sync-chart-engine.mjs`, run as `npm run
    sync:charts` from `web/`): mirrors the engine's `.ts` files from
    AfterBell (default path `D:\Blackthorn Live\src\lib\vendor\blackthorn-charts`,
    overridable by a CLI arg or `AFTERBELL_CHARTS_DIR`), never writes back
    to AfterBell, and rewrites the vendored `PROVENANCE.md` with the
    AfterBell commit, whether uncommitted engine changes were swept in, and
    the sync timestamp. Rule: never hand-edit the engine inside the
    scanner — change it in AfterBell and re-sync; `run.bat` picks up a sync
    automatically since `web\src` is already in its freshness check. The
    first sync (2026-09-14) swept in AfterBell's own uncommitted font work
    (`render/fonts.ts` plus ten related engine files); AfterBell's own
    engine smoke test passed on that state.
  - **Charts window cell indicators** (`MonitorChart.tsx`): every cell draws
    the engine's built-in VWAP and a **9 EMA** (`EMA_PERIOD`, engine default
    style: amber, 2 px). Both are engine indicators, so they compute on the
    candles the cell shows and follow its 1m / 5m / 15m switch.
  - **Charts window cells** (`MonitorChart.tsx`): a symbol or timeframe
    switch now opens the engine's default view — `timescale.ts`'s
    `resetView()`, default zoom anchored to the latest bar — instead of the
    old `fitContent()`, which squeezed a whole two sessions' worth of bars
    into one small monitor cell. The previous day is still loaded; it just
    sits off-screen to the left, reachable by dragging or zooming out. The
    chart follows new bars whenever the latest bar is in view, even after a
    pan or zoom (user: "The charts are live but they dont scroll
    automatically"): the engine alone stops following at the first gesture,
    and a click with a pixel of mouse wobble counts, so `MonitorChart`
    re-anchors with `timeScale().scrollToLatest()`, at the current zoom,
    whenever a new bar would push the live edge off screen. Scroll back into
    history and the chart stays put; AfterBell's round » go-to-latest button
    (bottom-right, shown only while the latest bar is out of view) jumps back
    and resumes following. A reconnect back-fill (same symbol/timeframe)
    keeps the trader's zoom: the engine's `setData` resets a chart that is
    merely following to the default zoom, so the component first marks the
    view as user-set with `scrollBy(0)`, then returns to the live edge if it
    was there. New engine
    behaviour visible here for free: a dashed last-price line with an axis
    chip, and TradingView-style gestures — wheel/trackpad zoom, time-axis
    drag-to-zoom, axis double-click to reset, kinetic panning. Canvas text
    now renders in the app's own fonts via `chartTheme.ts`'s `chartFonts()`
    (numbers monospace, words in the app typeface), re-applied once web
    fonts finish loading. The manual price-scale pin behaves exactly as
    before.
  - **Inline row chart** (`CandleChart.tsx`): same engine,
    `showLastPrice: false` (a last-price chip is clutter at the row's
    116px height), still `fitContent()`s its whole short window.
  - **Theme bridge** (`chartTheme.ts`): now imports the engine's own
    `ChartColors`/`ChartFonts` types (type-only, so this module can never
    drift from what the engine actually expects) instead of hand-rolled
    ones, and maps the engine's new `accent` field to `--ab-accent`.
- **Live chart**: clicking a symbol loads today's 1-minute bars over REST, then
  keeps them updating — the ingest layer tees the per-second `A.*` aggregates for
  the selected symbol (tapped *before* the universe filter, so it works even
  before the universe loads) and the client folds each second into the forming
  minute candle. Uses the aggregate stream every plan already has, so the chart
  is live even without the real-time trades the tape needs. Rendering goes
  through the engine's incremental `update()` rather than `setData`, so a chart
  you've zoomed into stays put while it ticks; it auto-follows the right edge
  until you pan/zoom, and only a symbol/timeframe switch resets the view.
- **Tape reader** (time & sales / order flow): for every *currently selected*
  symbol — one per open browser window, see **Multi-window** below — the app
  subscribes `T.<sym>`+`Q.<sym>` for each on the SAME WebSocket connection as
  the aggregates (Massive allows mixing channels; a second *connection* would
  disconnect the first, but many channels on one connection is fine) and
  classifies every print buyer- vs seller-initiated using the NBBO (Lee-Ready:
  at/above ask = buy, at/below bid = sell, midpoint → tick rule), falling back
  to the pure uptick/downtick rule when quotes aren't on the plan. From that it
  computes volume delta (1m/5m/10s/cumulative + delta as a share of that
  window's own volume), % of volume hitting the ask vs the bid, trade rate
  (1s/60s baseline), median print size, a session-long cumulative volume delta
  (CVD) series, and notable prints (≥20× rolling avg size or ≥$25k notional,
  plus a separate block-print threshold = `max(500, 8× median size)`) per
  symbol, published as a ~1 Hz digest that drives each window's own
  buyers/sellers-in-control gauge, CVD chart and Level 2 ladder (real depth
  from IBKR TWS — see below; none of the analytics below ever needed true
  depth to begin with, only the raw ladder display does). Needs real-time
  trades on the Massive plan (Stocks Advanced for trades+quotes; Developer is
  trades-only → tick-rule mode); a rejected tape subscription is reported
  inline and never disturbs the
  whole-market aggregate feed. This whole engine (originally a separate
  standalone Python app the user ran alongside the scanner) was fully ported
  into Go so both apps stop competing for the ~1-connection-per-product cap.
- **Regime detection + free local verdict**: alongside the digest, every
  watched symbol runs six independent shift detectors on a shared 20s cooldown
  (first to fire wins, one per symbol so two windows on different names never
  block each other): a large refill-and-absorb sequence at the inside quote
  (*level absorption*), a single print ≥ the block threshold (*block print*),
  5 same-side prints in ≤3s with the price net-progressing (*sweep*, deduped
  until direction flips), an opposite-sign flip between the last two 5s CVD
  windows (*CVD flip*), a flat-price/one-sided 10s window (*absorption*), and a
  1s print rate ≥3× the 60s baseline (*speed spike*). Independently, a free
  5-tier priority cascade (absorption-at-a-level → liquidity-pulled →
  sweep-momentum → price/flow divergence → plain 60s imbalance) produces a
  "who's in control" verdict every tick at zero API cost — this is what
  drives the gauge and the regime timeline by default with nothing configured.
  Level-1-quote transitions (no true depth needed) are classified into
  absorb/add/pull/up/down per side to feed both of the above.
- **Structured Claude "who's in control" reads**: separate from the on-demand
  flat-text tape summary below, an event-driven card system fires per symbol —
  instantly and for free from the local verdict the moment any detector above
  trips, then (if enabled for that symbol) upgraded to a fuller Claude read via
  the Anthropic SDK's native structured-output (`output_config`/JSON schema,
  not tool-use) so the reply is always a typed
  `{control, confidence, whats_happening, evidence, watch_for}` object, not
  prose to parse. A per-symbol toggle switches between the free local verdict
  and Claude; a per-symbol commentary interval (15–600s, 0 = off) fires a
  periodic Claude narration even with no regime shift, reusing the same
  cooldown bookkeeping. Every completed read (local or Claude) is archived to
  SQLite. A window-local pause mutes alerts/voice/cards without ever touching
  ingestion — CVD, quotes and book stats keep updating through a pause exactly
  like before it. Voice narration (Web Speech API, fully client-side) speaks
  the instant local alert immediately and the fuller Claude read once it
  lands, with dedup logic so the same event is never spoken twice.
- **Real Level 2 depth (IBKR TWS)**: `reqMktDepth`, single-target the same way
  the display-group push and live borrow line already are — draws from the
  same scarce market-data-line pool (confirmed from IB's own docs, not
  assumed), so exactly one depth line is ever held, following whichever
  symbol was most recently selected across every open window. Every other
  watched symbol's ladder explains it's following that symbol instead of
  showing a generic "not entitled" message. `numRows=10, isSmartDepth=true`
  (matching the original standalone Python app's own IBKR call);
  `internal/twslink` handles both `MARKET_DEPTH` (single-market-maker venues
  like ARCA) and `MARKET_DEPTH_L2` (multi-market-maker venues like NASDAQ,
  distinct field layout). `SCANNER_TWS_LIVE_DEPTH=0` disables it. Whether a
  given symbol/venue actually has a paid Level 2 subscription can only be
  learned live — verify with
  `SCANNER_LIVE_TWS_DEPTH_TEST=1 go test ./internal/twslink -run TestLiveTWSDepth -v`.
- **Multi-window** (user: "run multiple scans at the same time and have the
  tape open for a selected stock"): the main window tiles any number of scans
  side by side (click a tab to add/focus it as a tile, ⧉ pops a tile out into
  its own real browser window, ✕ closes it — layout persists across reloads).
  Each `SymbolPanel` also has a ⧉ to pop its chart+tape out into its own
  window. **Tape is genuinely independent per window**, not one shared
  selection: `internal/tape.Engine` tracks a `map[string]*symState` instead of
  one global symbol (adding a new watch never resets another window's already-
  accumulated history), `internal/feed.Client.WatchTapeSet` reconciles the
  Massive trade/quote subscription to the union of every open window's symbol
  in one batched control frame, and `internal/ingest.SetChartSyms` does the
  same for the live per-second chart tap. The server tracks each connection's
  own watched symbol (mirroring the existing per-client scan `view` filter)
  and recomputes the union through a single coalescing goroutine whenever any
  window's selection changes or a window closes — avoiding a read-diff-write
  race between concurrent windows without adding a new lock. A pop-out window
  is a genuinely separate page load (its own store, its own WebSocket
  connection), so independent selection falls out for free with zero
  cross-window messaging; **TWS and the live IBKR borrow check stay
  single-target** ("last window selected wins") since there's only one real
  TWS instance and IBKR market-data lines are a scarce, shared resource unlike
  Massive's aggregate connection. A tape pop-out defaults to **🔗 Following**:
  it mirrors whichever symbol was most recently selected in *any* window
  (client-side only, via `localStorage` + the `storage` event — which the
  browser only fires in *other* tabs, so it can't feed back into a loop),
  making it a standing "keep this open, walk it around my scans" window
  rather than a one-off snapshot. Click the badge to **📌 Pin** it to its
  current symbol for side-by-side comparisons instead; click again to resume
  following (immediately re-syncing, not waiting for the next click
  elsewhere).
- **Charts window Auto: calm top gainers** (2026-10-08, user: "I want on that chart the top 8 gainers automatically plotted. Make it user friendly so its not a disco."): ⚡ Auto in the charts toolbar fills the un-pinned cells from a scan's gainers (Day % or 5m %), `web/src/autoFill.ts` (pure `autoStep`, 17 tests). Hysteresis: never reorders; a challenger needs 30 s continuously in the top N; an incumbent is replaceable only after 60 s worse than N+3 (or gone); ≤1 swap / 15 s; nothing changes while the mouse is over the grid; 📌 pins a cell (any manual assignment while Auto is on pins it); NEW badge for 30 s; settings + pins persist in the layout blob. Verified: 50 random reshuffles over 25 s → 1 layout; a real change (new #1, one name gone) → exactly one swap at +62 s into the departed name's own cell.
- **Send to TWS on alert** (2026-10-08, user: "If there is a Green DIldo alert send it straight to TWS"): per-scan `AlertConfig.SendToTWS` (scan editor checkbox next to Speak), on for Green Dildo (`dildo-1m`, migration key `seeded.dildo.tws.v1`). The server (`internal/server/twsauto.go`, off the broadcast loop) calls `tws.Show(sym)` — display-group only, never orders, no L2 depth or window-selection change — unless the user picked a DIFFERENT symbol in the last 20 s or another auto-send happened <5 s ago; every decision goes out as `tws.auto` → a toast in the main window. Note a window reconnect re-sends tape.watch and so also starts the 20 s guard (harmless, the toast says why).
- **1-minute candle gain + RVOL** (2026-10-07, user: "scan for 5% gain + Rvol on a 1 minute candle compared to the last 9 candles"): fields `c1mChg` (1m candle %: candle close vs the previous traded candle's close) and `c1mRvol` (candle volume ÷ mean volume of the 9 clock minutes before it; untraded minutes = 0; Null until 9 fully-heard minutes) from a per-ticker tracker in `internal/engine/candle1m.go` (~210 B/ticker, never reset at 04:00 or session boundaries). Live: the forming candle is shown, but the candle that just closed stays displayed through the next minute while its % is higher, and both fields always describe the same candle. Candles close lazily on the clock minute (`view(baseSec)`). Seeded scan **1m Candle Spike** (`seeded.c1m.v1`, id `c1m-spike`): c1mChg ≥ 5 AND c1mRvol ≥ 3, ping on enter.
- **Flag-scan signal history** (2026-10-06, user: "On the signal page flag scan i want an activity list from stocks that made a signal so i can view it back."): `internal/flaglog` records every event the market-wide 9EMA flag scan's engine emits (FLAT, 9 EMA NEAR/READY, LONG, ADD, EARLY BREAK, EXIT, FLAG FAILED, SLAM, BREAKOUT) per ET day, keyed (sym, bar ts, kind), via `flagscan.Config.OnEval` (non-blocking: memory + a buffered writer to the `flag_events` table, 30 days kept). Wire: `flagevents.get {date}` → `flagevents.data {date, dates, events}`, live `flagevents.add`. A restart's replay of the morning is recognised (seen-set loaded from the DB before the scan starts). The Signal window shows it as **Signals** under the live flag list (`FlagEventList.tsx`, helpers in `flagEvents.ts`): newest first, time · ticker · badge · price, All/Trades filter (Trades = LONG, ADD, EXIT, FAILED and the SLAM alert), ‹ date › back through 30 days, new rows flash, click loads the symbol on the active chart, rows drag onto charts. BREAKOUT is recorded but hidden in the list (every candidate makes one). Off with SCANNER_E9FLAGS=0.
- **Dildo + climax signals in the history** (2026-10-08, after missing SBFM's 04:24 green dildo - the Green Dildo scan lists a name for 4 minutes only): the Signals list also records every **GREEN DILDO** (badge DILDO), **9 EMA PULLBACK** (9EMA PB), **VOLUME CLIMAX** (CLIMAX) and **BOTTOM CONFIRMED** (BOTTOM), plus the dead ends **PB CANCELLED** (PB FAIL) and **CLIMAX FAILED** (CLX FAIL) as dimmed low-key rows, each with its price and a one-line note under the row (body %, volume multiple, drop %, ...). Sources: the engine's closed-bar judge hands every alert to `Engine.SignalHits` (`engine.Options.SignalLog`, non-blocking, drops counted and logged by `flaglog.RunSignals` as they rise - those rows are missing for good) -> `flaglog.RunSignals`; flagscan's new `Config.OnDildo` hands the dildo rule's events of every tracked symbol (hot-only names included) -> `flaglog.OnDildo`. Same identity (sym, bar ts, kind) as the chart rules, so the engine's dildo and the chart's on one bar are one row; one pullback verdict per dildo (a resync that moves or flips it adds no row - except that a 9 EMA PULLBACK the rule calls in place of a recorded PB CANCELLED, after a rewritten bar, gets its row next to the cancel's: it is alert 2 itself; a restart's guard covers every dildo row before a verdict, the engine-only ones included); a Trades view of a day with only dildo / climax rows says how many are under All; restarts load today from the DB. `flag_events` gained a `note` column (migrated in place). Shown under All; Trades stays the flag setup's. Each signal's kill switch (SCANNER_DILDO / SCANNER_CLIMAX) stops its rows; the Signal window's Dildo / Climax alert toggles mute sound only. The recorder now runs when any of SCANNER_E9FLAGS / SCANNER_DILDO / SCANNER_CLIMAX is on.
- **Compact header** (2026-10-05, user: "Can we clean up this. It starts to take a lot of screen space." — ~6 lines with 25 scans): one status line + one tab line (~75px). Status bar: no conn pill (the brand dot is the connection), session folded into the clock (REG 13:17:44 ET), tracked+rate merged, RVOL only while building, low-priority pills hide under 1180px, sound alerts are a 🔇/🔊 toggle (no banner, in every pane), API key is a ⚙ icon. Tabs: one non-wrapping row; scans that don't fit go in **More ▾** (counts, tiled/active hints, + New scan; double-click a row to edit). Row order = tiled scans first, then scan order (`orderTabs` in `tabFit.ts`), so the scans in use are never hidden and the order only changes when the user tiles/closes one. The cut-off is measured with a fixed "000" badge on every pill (`fitCount`) so live counts can never push a tab into More — a few px of slack traded for a layout that never moves by itself. Window launchers (◧ Details, ⧉ Tape/Charts/Review/Signal) sit compactly at the row's right end.
- **Tile-area auto-sizing — tried and reverted** (2026-09-22). A first
  attempt collapsed a row of tiled scans to a strip while every scan in it
  was empty, handing the height to the Activity Log; a second layer let a
  splitter drag override that and a double-click restore it. The user asked
  for the old system back ("I want to revert to the old system") — it made
  the divider feel dead while scans were empty, and a layout that resizes
  itself is not what they want from a trading screen. So: `.tile-grid` is
  the plain CSS grid it always was (`auto-fit, minmax(360px, 1fr)`, equal
  `1fr` rows), the tile area is exactly `pane.gridH` tall and ONLY the
  splitter changes that. Do not reintroduce automatic pane sizing here.
- **Main-window declutter** (2026-09-22, user: "Make this layout more user
  friendly"): four things the screenshot showed, each fixed in place.
  (1) A tiled scan said its name twice — once in the tile header, again in
  `LiveGrid`'s own 17px title row — and that title row was ~40px per tile;
  the grid's title row is gone (`.grid-title` deleted) and the match count
  now sits beside the name in the frame's header (`MatchCount`, exported
  from `LiveGrid.tsx`, used by both `TileFrame` and the `?pane=scan`
  window; the hover-freeze "· held" state is reported up via an
  `onHeldChange` prop). (2) An empty scan still rendered its column-header
  row, which is wider than a tiled cell and so dragged a horizontal scrollbar
  into a state with nothing to scroll — the header is hidden while
  `order.length === 0` and the virtualizer's zero-height spacer no longer
  forces the row width either. The empty-state text is "No matches right
  now." plus the criteria on one ellipsized line (full text in the tooltip).
  (3) With 16 scans, ten "0" badges were most of the tab bar's ink: a scan
  matching nothing renders no badge and a fainter pill (`.tab.idle`), so
  only live counts stand out; tiled (`.open`) and focused (`.active`)
  styling still win, and the bar is a little tighter. (4) The details column
  (symbol panel + news) is 40% of the width even when nothing is selected:
  **◧ Details** in the tab row (and a **⇥** on the panel itself) collapses
  it and the grid column takes the full width, persisted with the layout
  (`pane.rightHidden`). It is hidden with `display:none`, NOT unmounted,
  because `SymbolPanel`'s effects are what send `tape.watch` (the TWS /
  L2 / borrow link) for a row click and run the cross-window follow
  listener — collapsing the column must not silently break TWS following.
- **The tape window is chart-free and shows the actual tape** ("on this
  window i dont need the chart. Remove the chart and add the actual raw tape
  and level 2"): the **⧉ Tape button** in the scan-tab row (or the ⧉ in the
  symbol panel) opens it; `?pane=tape` renders `TapeReaderView` instead of the
  chart-bearing `SymbolPanel` — stats header, the verdict/controls/metrics
  strip, then the **Level 2 ladder and a raw time & sales feed side by
  side** as the main columns, with CVD + tape reads in a slim strip below.
  The raw prints ride a new `tape.prints` wire frame: the tape engine
  batches every aggressor-classified trade on a 250 ms flush (per-batch cap
  500, oldest dropped and *counted* — the feed renders "… N prints dropped"
  rather than pretending it saw everything), sends non-blocking so a
  500-trades/sec tape can never stall the shared single-writer engine, and
  the server routes each batch **only to windows watching that symbol**
  (per-client filter, unlike the broadcast tape stats). The client keeps a
  400-print ring, newest at top, block-sized prints highlighted, cleared on
  every symbol switch/reconnect so a late batch for the previous symbol can
  never render under the new one. Both watchers re-arm their tape watch
  after a WebSocket reconnect (previously the watch died silently with the
  old connection, freezing the band until a symbol change). The main
  window's symbol panel has since been slimmed to **stats + fundamentals +
  the setup card, with news filling the rest of the column** ("I dont need
  the tape or chart. Just the fundamentals and news") — the chart lives in
  the ⧉ Charts monitor window and the tape in ⧉ Tape. The panel still sends
  its tape watch on selection, deliberately: that is what drives the TWS
  window link, live L2 depth targeting, and the IBKR borrow subscription.
- **Monitor window** ("a layout for stocks that i want to monitor during the
  day… on my third monitor… a watchlist so i can easily assign these stocks
  to the different charts"): the **⧉ Charts button** at the right end of the
  scan-tab row (or `?pane=charts` directly) opens a full separate window with
  a **grid of live candlestick charts** and a collapsible **watchlist
  sidebar**. Layout presets come in two families, both up to the 4×4 the
  layout blob supports: 2×2/3×2/3×3 for a wide monitor, and 1×2/1×3/1×4/2×3/2×4
  (user: "make the layout more usable for my current portrait layout") for a
  tall one — one or two wide columns with more rows, so each cell stays a
  legible landscape shape instead of the tall, narrow squeeze a 2×2 forces
  on a portrait window. Switching between them preserves every cell's
  assignment (both are the same flat, row-major cell list, just reshaped),
  so going from 2×2 to 1×4 turns today's four assigned charts into a stack
  with no reassignment. Type a symbol to add it; assign by dragging a chip onto a cell,
  picking from a cell's dropdown, or typing straight into an empty cell.
  Each cell has its own 1m/5m/15m timeframe, VWAP, volume, and a prev-close
  line, seeded from history and updated live off the same per-second stream
  the symbol panel chart uses. The watchlist (SQLite `watchlist` table) and
  the grid layout (a `chartgrid.v1` blob in `schema_meta`) live
  **server-side**, so the window survives restarts and edits sync instantly
  to every open window. Wiring note: the live-chart tap is now driven by ONE
  server-side recompute producing two unions — tape symbols (which also
  drive TWS/L2 targeting, unchanged) and tape ∪ chart-grid symbols (charts
  only) — so a tape recompute can never silently kill the monitor charts or
  vice versa, and grid symbols never move the TWS window or cost trade/quote
  subscriptions. A closed monitor window drops its symbols from the union
  automatically.
- **Monitor window: previous-day continuity + manual price-scale** (user:
  "i want the previous day data for the stocks i'm watching… auto scale with
  a function to manually scale"). Each cell's chart now requests **the
  previous trading day plus today** in one fetch (`feed.Client.
  IntradayWithPrevDay`, weekday-aware — a Monday's "previous day" is the
  prior Friday; no holiday calendar yet, same documented gap as elsewhere) —
  so opening the monitor window first thing loads yesterday's session for
  context (the gap vs. yesterday's close) — now loaded off-screen to the
  left of the chart engine's default right-anchored view rather than fit
  into view alongside today, reachable by dragging or zooming out.
  This is a genuinely separate code path from the pre-market/session-stat
  backfill (`internal/prefill`), which still reads only today's bars — the
  two were kept apart on purpose so a wider chart fetch can never double-
  count a prior day's volume into a frozen pre-market/gap-fade stat. The
  **price (Y) axis auto-fits by default**, and now supports a manual
  override: drag or scroll-wheel on the price axis (the right-edge scale) to
  pin a custom range — an **Auto/Manual** toggle in each cell's header
  reflects and controls the mode. A pinned range survives every live tick
  (it's never silently reset out from under you) but automatically clears
  back to auto the moment a cell is reassigned to a different symbol or
  timeframe, so a manual zoom never leaks onto an unrelated series.
- **Morning review — build today's watchlist from last night** (user: "I
  want a function so i can see what happened in the session the day
  before… wake up in the morning and want to make the watch list from the
  previous day after hour gainers"). The **⧉ Review button** (or
  `?pane=review`) opens a pop-out defaulting to **yesterday's after-hours
  session, sorted by gain**: date and session (pre/day/AH) pickers, a
  min-volume filter, and every row draggable straight into the charts
  window's watchlist (or click to broadcast the selection for any following
  window to load — the tape pop-out always follows; the main window only if
  its **🔗 Follow** toggle is on, off by default). Underneath: the engine
  already visits every ticker once at each
  session boundary (that's how the frozen pre-market fields work) — it now
  also persists each session's raw stats (hi/lo/close/vwap/vol/prevClose) to
  SQLite at 09:30, 16:00, **and 20:00** (the after-hours close, which
  previously just vanished at day rollover), keeping the last 10 trading
  days. Gains are computed the right way per session: after-hours gain is
  **that same day's regular-session close vs. its 4pm close**, never the
  previous day's close and never the after-hours session's own first
  print — the classic wrong-baseline trap this feature is built to avoid.
  History starts accruing from the first boundary after this shipped —
  there's no way to retroactively recover sessions from before the feature
  existed. (Note: the frontend now has its first real test harness — vitest,
  `cd web && npm test` — seeded by this feature's gain-derivation coverage.)
- **Per-window, per-scan sound mute** (user: "The sound alert needs to be
  configured PER scan window. Not a general one. I want to choose from which
  scanner i get a sound."): each scan's tile (main window) and each scan
  pop-out window carries its own 🔊/🔇 toggle, muting that scan's alert
  *sound* only in that one window — the activity log, row flashing, and every
  other alert effect are unaffected, and the same scan can be muted in one
  window while staying audible in another (e.g. a pop-out you keep on a
  second monitor specifically to hear). The mute flag lives in plain
  in-memory state, deliberately never `localStorage` — this app's windows are
  separate processes sharing one WebView2 profile, so `localStorage` is
  already deliberately shared across them (that's how the tape follow-mode
  works); a mute setting leaking the same way would defeat the whole point.
  It resets to audible on that window's own reload — a deliberate trade for
  guaranteed no cross-window bleed over persistence. Fixed alongside this: a
  **popped-out scan window could never play alert sound at all** — the
  "🔊 Enable sound alerts" browser-audio-unlock gesture existed only in the
  main window, so a pop-out's `unlock()` was simply never called. Every
  pop-out now has its own unlock button.
- **Average daily volume + premarket participation** (user: "a stock scanner
  that scans the following; Average Daily volume, Percentage of that volume
  traded in the premarket and the gap %"): `avgVol` is the trailing
  20-session mean of REGULAR-SESSION (09:30-16:00) volume only — the
  conventional ADV, deliberately excluding pre/post-market so the next field
  isn't comparing today's pre-market volume against a baseline that itself
  partly consists of past pre-market volume. `preVolPctAdv` is pre-market
  volume so far as a % of `avgVol` — unlike the existing `preVol` (frozen at
  09:30, reads 0 the entire time pre-market is actually happening), this one
  is **live throughout pre-market itself**, then holds its final value once
  09:30 passes; both are derived from the SAME per-bucket cumulative RVOL
  baseline already tracked, so no new data collection was needed. "Gap %"
  here is `chgDay` (live, vs previous close) — not `preGap` (frozen at 09:30,
  Null the entire session this scan is meant to be watched in). Seeded scan
  **"Premarket Volume Leaders"** (`premkt-vol-leaders`: `avgVol >= 500000`
  AND `preVolPctAdv >= 10`, sorted by `preVolPctAdv` desc, alert off — a
  browsing list, not a trigger).
- **On-demand Claude tape read** (separate from the structured cards above):
  "✨ Read tape" asks Claude to interpret the current order flow — who's in
  control, sweeps/absorption, delta disagreeing with price — in ≤70 words
  ending in a `Read: Buyers|Sellers|Balanced — <what invalidates it>` line.
  Claude sees the computed metric digest plus price levels (VWAP/HOD/LOD) and
  the day's news summary, never raw ticks. One read fires automatically ~45s
  after selecting a symbol (needs ≥40 prints), with a 10-min per-symbol
  cooldown; `SCANNER_TAPE_AUTOREAD=0` disables it.
- **Bull-flag / consolidation scanner** (daily): seven filterable, sortable
  fields describing a "ran up, then paused tight" setup — `flagScore` (0–100
  composite), `flagState` (0 none / 1 coiling / 2 breaking out), `flagPole`
  (size of the advance), `flagRange` (base tightness), `flagRetrace` (% of the
  pole given back — the main discriminator), `flagVolDry` (base volume ÷ pole
  volume; below 1 = supply drying up), and `flagBars`. Because the state is
  numeric, "show me coiling names" is a plain `flagState == 1` filter and
  "alert me on the breakout" is a plain cross-above on `flagState` — both use
  the alert engine that already existed.
  **Nightly geometry, live breakout**: `internal/pattern` (pure, no I/O, every
  threshold a documented const) runs once nightly over the daily bars, in the
  same pass as the trend classification, and stores each base's high. The
  scanner overlay then compares the live price against that stored high on
  every snapshot — an O(1) float compare — so the coiling→breakout flip is
  real-time with no recompute. Nothing runs in the engine's hot path.
  Symbols with no pattern are `Null` in all seven fields, never `0`, so an
  unknown never satisfies `flagScore > 70` *or* `flagState == 0`.
  This required widening the nightly daily-bars pipeline to full OHLCV — it
  was fetching o/h/l/v from Massive and decoding only the close, so there was
  no high/low for range tightness and no volume for the dry-up check.
- **Intraday flag scanner** (the same detector, today's session): seven more
  fields — `iflagScore`, `iflagState`, `iflagPole`, `iflagRange`,
  `iflagRetrace`, `iflagVolDry`, `iflagBars` — computed over the current
  session at 5-minute resolution, so a daily-base scan and an
  intraday-coil scan run side by side. The engine keeps a session-long
  per-ticker OHLC record in 192 five-minute buckets (04:00–20:00 ET, true
  agg-fed extremes, ~23 MB at 10k tickers beside the RVOL curve's ~15 MB;
  the hot path gains only a couple of compares); a worker re-runs the SAME
  `internal/pattern` geometry every ~60s over a gated subset (≥20 observed
  buckets ≈ 100 minutes, ≥50k shares session volume) with
  `pattern.IntradayParams()` — identical thresholds except the minimum pole
  drops 20%→10%, since every other measurement (retrace as % of the pole,
  range as % of the base's mid, the volume ratio) is scale-free by
  construction. Breakout promotion is live per tick against the intraday
  base high, independent of the daily fields' own promotion. The worker
  always publishes (even empty), so stale intraday flags are wiped within
  one cycle of the 04:00 session reset. The adversarial pass caught two
  real data-honesty bugs before ship: a fully forward-filled (dead-quiet,
  zero-volume, identical-price) base scored 76 as a "perfect coil" — now
  rejected by a flat-base gate keyed on price, not volume — and a reconnect
  re-seed could step the cumulative volume curve down, leaking negative
  per-bucket volume into the pattern math — now clamped at the read path.
- **Claude setup reads** (the "AI" half of the pattern scanner): when a symbol
  enters a scan that actually filters on the flag fields, `internal/flagread`
  asks Claude to score the setup 1–5 and explain it — verdict, what's
  happening, risks, and what would invalidate it — rendered in the symbol
  panel and archived to SQLite. The deterministic geometry does the
  whole-market filtering for free, so Claude only ever sees the shortlist.
  **Cost controls are treated as a correctness requirement**, mirroring the
  news auto-summarizer: the gate is ordered cheapest-first (scan-filters-on-
  flags → non-null score above `MinScore` → cooldown → budget), the cache key
  is `(symbol, nightly classification date)` so a re-entry the same day is
  free and survives a restart, a failed call refunds its budget, and the
  cache **fails closed** — a cache lookup that errors skips the read rather
  than paying again, because a broken archive also fails to save, which would
  otherwise re-buy every read every day indefinitely. Defaults: 2 reads per
  symbol per day, 40 globally, 30-minute cooldown, minimum score 60 — all
  env-tunable (`SCANNER_FLAGREAD_MAX`, `_DAILY`, `_MIN_SCORE`),
  `SCANNER_FLAGREAD=0` disables. The prompt explicitly requires Claude to say
  when a setup is weak and forbids inventing facts it wasn't given (no news,
  earnings, or price targets) — a scorer that rates everything 4/5 would be
  worse than none, because it would be trusted.
- **Past-session review**: ten fields that survive the session boundary —
  `preChg`/`preHigh`/`preLow`/`preVol`/`preVwap` freeze each ticker's
  pre-market stats when pre-market ends at 09:30, and the `rth*` set does the
  same for the regular session at 16:00 — so "what moved in the pre-market"
  stays filterable and sortable all day (and the day session stays
  reviewable after hours). `preChg`/`rthChg` are **% vs the previous close at
  the moment the session ended** (the number traders actually mean by "up
  40% in the pre-market"), not vs the session's own first print; the
  high/low/vol/vwap are the session's own scoped values. The freeze happens
  in the same once-per-boundary pass that already resets the live session
  metrics, keyed on the session that *ended* — an app started mid-day
  freezes nothing rather than fabricating a pre-market. Frozen slots are
  memory-only — but see the Massive backfill below: an app that missed the
  boundary reconstructs the top ~300 movers a few minutes later. A pre-built
  **"Pre-Market Movers"** scan
  (`preChg >= 5 AND preVol >= 100000`, sorted by move size, alert
  deliberately off) seeds once per database and fills at 09:30.
- **Gap & fade fields**: four more frozen fields on top of the past-session
  set — `preGap`/`rthGap` are the session **high** vs the previous close
  (the peak of the gap, so a gapper that faded still shows the gap it made),
  and `preFade`/`rthFade` are how far the last price had fallen off that
  session high when the session ended, as a % of the high, clamped at 0 for
  anything that closed the session at its peak. Both are computed inside the
  same freeze pass from values already at hand — nothing new per tick. Note
  the high is the *print* high, so a single spike wick inflates gap and fade
  together (the field hints say so). A pre-built **"PM Gap & Fade"** scan
  (`last between 25–250 AND preGap >= 5 AND preFade >= 30 AND
  preVol >= 100000`, sorted by fade depth then gap size, alert off) seeds
  once per database — the user's "gapped in the pre-market and faded for the
  rest of it" review, ready at 09:30.
- **Session backfill from Massive** (`internal/prefill`): fixes "the frozen
  stats only exist if the app was running at the boundary". A 10,000-symbol
  backfill is categorically impossible under the REST budget (no bulk
  minute-bar endpoint; ~42 minutes of paced calls would also starve the news
  poller), so the honest version runs **once per ET day, only when the app
  missed a boundary**, over a **capped candidate set**: the top ~300 movers
  ranked from the startup snapshot (|change %| + |official-open gap %| +
  log-volume) unioned with the scanner's own active symbols — both halves
  capped, so one run costs at most 1 snapshot + 2×300 minute-bar calls,
  paced 250 ms with 4 in flight, aborting the whole run on the first 429
  (the house rule) with no same-day retry. Started mid-premarket → one run
  at 09:32 rebuilding the pre slot; after 16:05 → pre + rth ~90 s after
  boot; running since before 04:05 → never (the live freeze is
  tick-accurate and strictly better). Stats are raw window aggregates
  (vwap is the close-weighted Σ(C×V)/ΣV approximation — minute bars carry
  no true VWAP); the engine derives chg/gap/fade itself **through the same
  helper the live freeze uses**, so the two paths provably cannot drift —
  one test table drives both and compares field-for-field.
  `SCANNER_PREFILL_CAP` resizes the candidate cap (0 disables). Symbols
  outside the top movers stay empty for the day — documented, bounded, and
  irrelevant to how these review fields are used.
- **Security-type filter**: an optional `secType` filter/column matching
  Common Stock (`CS`), ETF, or ADR (`ADRC`) — the only three types the
  scanner's universe tracks at all (`internal/universe.allowed`; warrants/
  units/rights/preferreds aren't fetched or tracked today, a separate future
  change if ever wanted). The type comes from the ticker-reference fetch
  Massive already provides, bridged through the engine the same way the
  weekly float reference is (an atomic-pointer hot-swap, `Engine.SetSecTypes`,
  refreshed alongside the universe). In the scan editor it renders as
  Common Stock / ETF / ADR toggle chips (an `in` filter under the hood, e.g.
  "exclude ETFs from a momentum scan" = check Common Stock + ADR) rather than
  a free-text condition.
- **Short availability (IBKR)**: three optional columns/filters — `shortable`
  (bool), `shortAvail` (shares available to borrow) and `shortFee` (annual
  borrow cost %). Sourced from IBKR's public availability file, refreshed every
  15 minutes: `ftp://shortstock@ftp2.interactivebrokers.com/usa.txt` (blank
  password), ~20k US tickers in one ~1.8 MB download. That's deliberately *not*
  the TWS API — per-symbol shortability ticks would consume a market-data line
  each, and IB caps concurrent lines far below a scanner universe. Symbols
  absent from IBKR's file stay null ("unknown"), never 0, so `shortAvail > 0`
  can't silently include unborrowable names. The file caps deep names at
  `>10000000`; that's kept as a numeric floor for sorting/filtering and rendered
  as `>10M`. `internal/shortable` implements the small slice of FTP needed for
  one passive download (EPSV with PASV fallback) rather than adding a
  dependency. Verify with
  `SCANNER_LIVE_SHORT_TEST=1 go test ./internal/shortable -run TestLiveShortable -v`.
- **Live borrow (selected symbol)**: the symbol panel shows a `BORROW (live)`
  stat — exact, real-time IBKR availability from TWS generic tick 236
  ("Shortable"), as opposed to the 15-minute bulk file behind the `shortAvail`
  column. Renders IB's documented bands (`>2.5` = 1000+ shares available,
  `>1.5` = locatable, else none) plus the exact share count when TWS reports it
  (build 974+). It holds **exactly one** IBKR market-data line: the previous
  subscription is cancelled on every symbol switch, so it can't eat into the
  100-line default allowance (which is shared with the TWS GUI). Needs a Level 1
  market data subscription for the instrument. `SCANNER_TWS_LIVE_SHORT=0`
  disables it while leaving the symbol link on.
- **Gainers/decliners lists in the charts window** (2026-09-22, user: "On
  the left on the watchlist, i want to make a simple gainers/decliners list.
  Like a simplistic scanner. Still room for the watchlist but under i want
  to configure this. I want at least two boxes there. Only ticker, price,
  volume and gain is required"): `SideBoxes.tsx`, under the watchlist
  chips. Each box is fed by one of the scanner's OWN scans — the server
  already evaluates those over every tracked ticker and streams their rows,
  so there is no second scanning engine or mini filter language — and shows
  that scan's top N (10/20/50) by Day % or 5m %, as Gainers (rows actually
  up, highest first) or Decliners (rows actually down, lowest first), in
  four columns: ticker · price · volume · gain. Rows drag onto a chart cell
  or up into the watchlist exactly like chips, and a click sends the symbol
  to TWS like a chart click. ⚙ opens the settings (scan, gainers/decliners,
  metric, rows), ✕ removes a box, "+ add list" adds one; two empty boxes
  are the default. The list lives in the persisted charts-window layout
  blob (`chartGrid.boxes`, validated by `normalizeBoxes` on the way in,
  kept across preset changes), so it survives restarts and syncs to other
  charts windows. `ChartsPane` narrows the window's `view.set` to the
  scans the boxes use and resyncs each (a tick is a delta; a scan bound
  after connect has no baseline until resynced) — note an empty view set
  means "every scan" to the server, which is what a charts window was
  already receiving; it plays and speaks only the alerts of the boxes'
  scans, though (none while no box picks a scan). **PM× column (2026-10-05,
  user: "add the Pm Vol vs 10x day average there as well"):** a fifth column, pre-market volume as a
  multiple of the 10-day average daily volume (`pmVolX`), shown compactly
  (3.05x, 31.1x, 143x) so a news runner fits. Every scan's tick rows now
  carry the box fields (last, vol, chgDay, chg5m, pmVolX:
  `scan.sideBoxFields`) whatever the scan's own columns, so a box never
  depends on its scan's column choice; the grids still render only the
  scan's columns, and alerts still carry only those. The charts window's
  sidebar is 310px to fit the five columns (the signal window's, which has
  no boxes, stays 260px).
- **TWS link**: clicking a ticker also switches Interactive Brokers TWS to that
  symbol, via IB's officially documented **Display Groups** API — the same
  colored chain-link groups TWS uses to sync its own windows. Since
  2026-09-22 (user: "If i click on the chart i want the ticker of that chart
  open in TWS") a click also works from the **⧉ Charts** monitor window: the
  chart cells were the one place a click did nothing, because being merely
  ASSIGNED to a cell deliberately never moves TWS (only the ingest chart tap
  — see "Monitor window" above). Clicking a cell's chart now calls the same
  `openChart`/`watchTape` path a scan-grid row click already uses
  (`ChartsPane.tsx`), so it escalates that one symbol to the shared
  TWS / live L2 depth / live-borrow target and the cell gets a small **TWS**
  badge plus an accent border while it's the linked one — the header
  controls (symbol dropdown, timeframe, Auto/Manual) and the go-to-latest
  button are unaffected, only the chart area itself is the click target.
  Fixed 2026-09-24 (user: "Appears the Click to TWS function is broken"):
  switching charts sends tape.watch "" then tape.watch NEW back-to-back, and
  the server coalescer (buffered-1 chan string) dropped the second signal
  WITH its symbol, so TWS was never told. The channel is now chan struct{}
  and the latest non-empty selection rides in pendingSel ("" never
  overwrites it) — TestTapeChangeCoalescingKeepsLatestSelection. twslink.Show
  had the same flaw (buffer 8, newest dropped when full, e.g. while TWS
  reconnects); it is now latest-wins, buffer 1 — TestShowKeepsOnlyLatest.
  Re-clicking the already-linked chart (or a gainers/decliners row) now
  re-sends tape.watch via ws.ts selectSymbol, since chartSym did not change.
  (`subscribeToGroupEvents` then `updateDisplayGroup`, over the TWS socket API
  on 127.0.0.1:7496). No window automation and **no focus stealing**, which
  matters with TWS on a second monitor. Requires "Enable ActiveX and Socket
  Clients" in TWS (Global Configuration → API → Settings) and at least one TWS
  window chained to the matching link-group colour. Tickers are resolved to IB
  contract ids via `reqMatchingSymbols` (exact US-listed stock match, cached).
  Env: `SCANNER_TWS=0` disables, `SCANNER_TWS_ADDR` (default 127.0.0.1:7496 —
  use 7497 for paper), `SCANNER_TWS_GROUP` (1-7, default 1),
  `SCANNER_TWS_CLIENT_ID` (default 77 — must not clash with another API client).
  The `internal/twslink` package implements only the handshake, symbol lookup
  and the four display-group messages; it has no order encoder, so it
  structurally cannot place a trade. Verify against a running TWS with
  `SCANNER_LIVE_TWS_TEST=1 go test ./internal/twslink -run TestLiveTWS -v`.

  Two protocol rules the socket API does not document, both of which present as
  an unexplained `EOF` right after a successful connect — if you touch
  `internal/twslink`, do not break either (each has a regression test):
  1. **Send nothing until `nextValidId` arrives.** TWS closes the socket without
     an error frame if a request lands before the `startApi` handshake has
     finished. `awaitReady` blocks on it.
  2. **Use one `bufio.Reader` for the whole session.** TWS packs the handshake
     reply and the frames that follow into a single TCP segment. A second reader
     over the same socket discards whatever the first had buffered, resyncing
     the stream mid-message.
- **Daily-chart trend** (`dtrend` / `chg20d`): every symbol is classified
  nightly from grouped daily bars (whole market in ~1 call/day; ~25-call
  backfill on first boot) as **↗ uptrend / → flat / ↘ downtrend** using the
  20-day SMA (close above a rising SMA = up; below a falling one = down;
  10-day fallback for recent listings, unknown under 15 sessions — unknown is
  Null, never "flat"). Motivation: news pops in daily up/flat contexts tended
  to follow through; pops in daily downtrends faded. Surfaces: a colored
  arrow column (`dtrend`), a `chg20d` %-column, scan filters ("dtrend >= 0"
  hides daily downtrends — Null is conservatively excluded like every numeric
  filter), and a verdict chip next to the symbol in the panel ("DAILY ↗
  UPTREND · +34.5%/20d", tooltip shows vs-SMA distance and session count).
  History lives in `daily_close_history` (45-day retention), classification
  recomputed after each close and hot-swapped like the RVOL baseline.
- **Fundamentals**: Revenue, Cash on hand, Cash Burn, and Float shown in the
  symbol panel, fetched on selection (not tracked for the whole universe — SEC
  filings only change ~quarterly). Revenue/Cash/Burn come from Massive's
  `/stocks/financials/v1/{income-statements,balance-sheets,cash-flow-statements}`
  (needs the Financials & Ratios add-on; shows a clear "needs the add-on"
  message instead of an error when not entitled). Cash Burn = net cash used in
  operating activities last quarter — deliberately excluding financing activity
  so a dilutive offering can't shrink the reported burn — with Runway (months)
  derived from Cash ÷ (Burn/3). Cached in SQLite per ticker (`fundamentals_cache`,
  24h TTL) so repeat clicks on the same symbol don't re-hit the API. Float is
  the existing weekly-refreshed reference (`float_ref`), just surfaced here.

- **Pullback signal window (short and long)** (`?pane=signal[&sym=X]`, the
  **⧉ Signal** pop-out — user: "I would like to get a entry signal on the
  short side so i can wait a bit longer and get a better entry", built from a
  real ATYR execution: too-early shorts averaged up into a still-rising spike;
  later: "I want to build the same for the long side"). One pure,
  deterministic engine (`web/src/signals/shortFade.ts`) walks a symbol's own
  candles through **idle → spike → crack → bounce** and then reads the
  pullback's two possible outcomes off the same swings — an **ABCD** shape:
  A = the high of day (HOD), B = the pullback low, C = the bounce high off B
  (a *lower* high, C < A, is what arms the short), D = a second swing low.
  A **lower high** at C is the short; a **higher low** at D — above B and
  still holding VWAP — is the long. The window is a **grid of signal charts**
  (presets 1×1 / 2×1 / 2×2, plus 1×2 / 1×3 / 1×4 stacked for a portrait
  monitor; the saved layout accepts up to 4×4), every cell running the engine
  on its own symbol with its own **1m/5m** toggle and a compact strip
  underneath (state badge + the engine's headline on the left, entry/stop
  numbers on the right); the detail panel reads the **active** cell — click
  a chart or its strip to make it active (that click also links the symbol
  to TWS / depth / borrow, exactly like the Charts monitor's click). The
  **☰ Watchlist** sidebar drags onto any cell; a cell's own dropdown and the
  toolbar's symbol box (Enter assigns to the active cell) work too. Follow/pin
  as in the Charts/Tape pop-outs, aimed at a grid: **Following** (default)
  drops every symbol you select in another window into the ACTIVE cell;
  typing, dropping or picking a symbol assigns to *that* cell and **pins**
  (📌) until you click it again. Each cell also has a **setup** selector,
  **9EMA flag | Pullback** (default flag — see the next bullet; this bullet
  describes Pullback). The layout (symbols, timeframe, setup and side per
  cell, the active cell) is kept in `localStorage`
  (`mktscanner.signal.layout.v1`), which every window of the app shares — so
  **one Signal window is the supported setup**: a second one starts from the
  same layout and then drifts, there is no sync message. The pop-out opens at
  1600×900.

  *The short side.* `idle → spike` arms once the run off its
  base — the lowest low in the previous **60** bars (`runLookbackBars`) —
  has gained **15%** (`minSpikePct`) and the high-of-day sits at least **8%**
  above VWAP (`minVwapExtPct`), OR the HOD is **15%** (`minGapPct`) above the
  previous close (or its overnight fallback — see below) for gap-and-go
  names with no real intraday run. The `spike` state exists only to say "do
  not short into strength" — no entry rule is active in it, which is the
  exact mistake this feature exists to stop. `crack` is the first close
  below the prior bar's low or a **5%** pullback off the high
  (`minPullbackPct`); `bounce` needs a confirmed swing low (2 bars either
  side, `swingStrength`); a confirmed swing high *below* the HOD — a real
  lower high, the objective proof sellers showed up before buyers finished —
  **arms and enters the short in the same step**: the primary entry is that
  lower high's own confirming close (`entryLH`, param `lhEntry`, on by
  default), with the stop **1%** above the lower high (`stopBufferPct`); a
  lingering `armed` state only shows with `lhEntry` off. A later break of the
  pullback low (**0.3%**, `breakBufferPct`) fires as `breakdown` —
  confirmation once already short via a lower high — or as the sole `entry`
  when `lhEntry` is off. A VWAP loss is a **target** (T1), not an entry, and
  fires once per short; `vwapLossEntry` (off by default) turns it into an
  entry too, but only once a lower high already exists, with the stop above
  THAT high, never above the raw HOD. Below VWAP a fresh lower high is tracked
  **silently** (its own price under VWAP at the confirming bar — the level
  and stop still move, but nothing is printed or sounded) because after a
  fade every small counter-bounce confirms a technical lower high; the
  visible re-entry below VWAP is instead `vwapReject` (a VWAP retest that
  fails; `SHORT (VWAP REJECT)` if not already short). VWAP LOST/RECLAIMED use
  a **0.5%** hysteresis band (`vwapBandPct`) rather than a bare cross,
  because a close a few tenths of a percent from VWAP was flipping
  target/reclaim on alternating bars. The stop **only ever moves up**;
  `stopByClose` (on by default) requires a CLOSE back above the stop to
  cover, `stopByClose:false` is a hard intrabar stop (any HIGH at or above
  it, also checked on the forming bar). A fresh, *higher* HOD invalidates
  everything past `spike`, including a live short ("cover, the top wasn't
  in"), because every level is measured off the HOD. **Adds are capped**
  (`maxAdds`, default **2**, per episode per side): an `ADD (LH)` needs no
  first target printed yet and its own risk to the stop at most
  `maxAddRiskPct` (default **12%**), and a `VWAP REJECT` / `VWAP HOLD` only
  re-arms after a close 2× the VWAP band away from VWAP — a refused `ADD (LH)`
  / `ADD (HL)` loses only its marker (the `lowerHigh` / `higherLow` still
  prints list-only), while a refused `VWAP REJECT` / `VWAP HOLD` add prints
  nothing at all, so an hour of 3-bar wiggles cannot scale you in on every
  one.

  *The long side* (the same swings, mirrored). C is the bounce high — the
  swing high that forms AFTER the current pullback low B, so a fresh low that
  undercuts B retires the old C (no higher low is measured against it, and
  the chart and panel stop showing it, unless a long is already open on it)
  until a new bounce high prints; once C exists, a confirmed swing low
  **above B** whose own low is at/above VWAP
  (`hlAboveVwap`, on) is the **higher low**, and the close that confirms it
  is the primary **LONG** entry (`entryHL`, param `hlEntry`, on by default —
  `LONG (HL)`), stop **1%** under the higher low, **fixed once set** (a long
  never trails on 1-minute noise). A trade through C (+0.3%) is the
  `BREAKOUT` confirmation (or the `LONG` entry itself with `hlEntry` off); a
  dip that touches VWAP and closes back above it is `VWAP HOLD` — an add
  while long, an entry (`LONG (VWAP HOLD)`) while armed. Targets are **T1 the
  bounce high C** and **T2 the HOD**; a new HOD is `NEW HOD`, the take-profit
  (the long's episode closes cleanly there). Losing VWAP while long is only a
  warning (`VWAP LOST`, list-only) — the long is closed by its own stop
  (`STOP (LONG)`, by close or by low per `stopByClose`) — and higher lows
  below VWAP never count. Both sides can be live for a few bars (a short
  from C still open, a long from D just taken): the market hasn't decided,
  the engine reports both honestly (`TWO-SIDED — …`), and the breakout above
  C stops the short by its own rule, the break below D the long by its own.

  *Which side a chart shows.* Each cell's head has a three-button selector
  **S | L | S+L** (short = lower high, long = higher low, both; default
  both, persisted with the layout). It is **display-only**: the engine always
  computes both, and the selector decides what that cell draws, lists and
  sounds — a `short` cell shows the short and the shared spike/crack story,
  its strip reads the short's own headline and none of the long's lines are
  drawn; a `long` cell is the mirror (a short-cover marker on a shared new-HOD
  is dropped so it can't be misread as a long entry). The panel follows the
  active cell: on S+L a **Short** and a **Long** card (each with its own read
  and plan grid — entry, stop, risk %, T1, T2), preceded by the engine's
  combined headline only when it adds something (the `TWO-SIDED` summary, or
  the shared spike-top story) rather than repeating one side's card; on a
  single side, that side's read and plan. Its event log lists only what the
  cell shows, each side's rows tagged S / L; the shared spike, crack and NEW
  HOD rows are untagged, except a NEW HOD that covers an open short, which is
  tagged S wherever the short side is shown.

  Structure and entries are evaluated off closed bars only; monotonic checks
  (new-HOD-by-high, entry-by-low, breakout-by-high, targets, base-target, and
  the stop-by-high/low when `stopByClose` is off) also run against the
  still-forming bar so a live cross doesn't wait a full extra candle — and
  what a forming bar shows is never taken back by the same bar closing: the
  intrabar long checks read the bounce high as it stood going into the bar, a
  forming bar arms a brand-new `SPIKE` only on the gates that cannot un-happen
  while it prints (the gap gate; the run gate when the VWAP-extension gate is
  off — the extension itself is decided at the close) and never demotes a
  spike already running, and with `stopByClose` off the closed bar replays its
  intrabar checks before it confirms any structure, so a resting stop that
  already traded is not cancelled. The overnight reference for the gap gate and the day-% header falls back from
  the scanner's own `prevClose` (when it is a real, positive number) to the
  previous ET day's own last candle close — because a live-scanner `prevClose`
  of exactly 0 (a separate, already-tracked bug) must not leave a real gapper
  permanently unarmable; `evidence.refClose` / `refSource` name which one was
  actually used, and each cell's day-% tooltip says "vs prev close" or "vs
  yesterday's AH close" accordingly. The tuning drawer in the panel edits
  every param above (number boxes and checkboxes, Reset-to-defaults
  included), applies to every cell at once and persists to
  `mktscanner.signal.params.v1`. The chart draws prev close / HOD / short
  trigger and stop / long trigger, higher low and stop / the pre-spike base
  (each only while it exists; a dotted `LH` until a short stop exists), and
  markers: ▼ at every short entry / add / breakdown / VWAP reject, ▲ at
  every short target / cover / stop, and the mirror for the long (▲ entries
  and adds, ▼ targets and stops) — spike / crack / lower-high / higher-low /
  VWAP-reclaim / VWAP-lost events (and a setup reset before any position) are
  list-only. Everything is computed client-side over the chart's own candles,
  so yesterday's and this morning's signals appear on load.

  *Sound and voice.* Every trade signal names the stock, because the window
  sits on a spare monitor. Tones are synthesised (no assets): a short entry
  (`SHORT (LH)`, `SHORT`, `SHORT (VWAP)`, `SHORT (VWAP REJECT)`) plays two
  **descending** notes (880 → 587 Hz), a long entry (`LONG (HL)`, `LONG`,
  `LONG (VWAP HOLD)`) the same two pitches **ascending** (587 → 880 Hz) — the
  direction is in the contour — and any add or confirmation (`ADD (LH)`,
  `BREAKDOWN`, `VWAP REJECT`, `ADD (HL)`, `BREAKOUT`, `VWAP HOLD`) one
  587 Hz note; stops and a covered short ring a bell, while the long's
  targets (T1 bounce high, T2 HOD, the new-HOD take-profit) and a lower high
  are a soft ping; the short's own targets (T1 VWAP, BASE) are silent.
  The toolbar's **🗣 Voice** toggle (default **on**, `mktscanner.signal.voice.v1`)
  speaks each one, symbol first — "ATYR: short. Lower high. 0.6030. Stop
  0.6737.", "ATYR: add, lower high, …", "ATYR: breakdown, …", "ATYR: VWAP
  rejection, short, …", "ATYR: stop, …", "ATYR: new high, cover." and, for
  the long, "ATYR: long. Higher low. … Stop …", "ATYR: add, higher low, …",
  "ATYR: breakout, …", "ATYR: VWAP hold, long, …", "ATYR: long stop, …",
  "ATYR: new high, take profit." — and is independent of the tape reader's
  shared voice preference (that one defaults off and is about tape
  narration; the Signal window asks the speech layer to bypass just that
  on/off gate, keeping the chosen voice and rate). Targets (except the
  long's new-HOD take-profit), lower highs and higher lows are never spoken.
  Each cell's side selector gates sound and voice strictly: a short-only cell
  never sounds a long event and vice versa. Only recent events sound: the last
  10 minutes, or the last (`swingStrength` + 3) bars if that is longer (25 min
  on a 5m cell with the defaults), because a lower high or higher low is
  stamped with its swing bar, which is already several bars old when it
  confirms; never on a symbol's first evaluation (or after a timeframe, side
  or setup change, or a tuning change — which re-primes only the cells running
  the edited engine, the other engine's cells keep diffing) so a reconnect
  back-fill or a freshly opened window
  doesn't replay the morning; a tone requested while the window is still
  locked plays on the unlock click only if it is under ~10 s old (the queue
  covers the click race, not hours of backlog); the
  toolbar's 🔊/🔇 mutes the tones for this window; and, like every
  sound-bearing window, the tones stay silent until you click the pop-out's
  own "🔊 Enable sound" button once (a browser gesture rule); speech needs no
  click.

  A real walk-through from 2026-09-24's ATYR (the numbers the tests
  assert): the gap gate arms `spike` at 06:07 (HOD 0.4398 vs yesterday's
  0.3751 close, +17.25%; the run gate would have needed 06:08), the true top
  0.7320 cracks at 06:18, and the lower high 0.667 confirmed on the 06:23
  close fires `SHORT (LH)` at 0.6030 with the stop 0.6737 and the
  `BREAKDOWN` (0.5763) on the same bar. With the default caps only ONE add
  prints, `ADD (LH)` at 06:27 (0.6324, 6.5% risk): 06:32's would sit 15.3%
  from a stop already moved up to 0.6868 (over the 12% cap) and 07:05's comes
  after T1, so both stay list-only; T1 VWAP fires at 06:34 (0.5963), the low is
  0.4146 at 06:41, and `VWAP REJECT` at 07:03 (0.5494) counts as the second
  add. Raise `maxAdds`/`maxAddRiskPct` (5 / 100) and the 06:27 + 06:32 pair
  returns. VWAP rejections share the add cap, so once it is reached the Short
  card says "Adds used N/N — no more adds this trade"; only a NEW trade after a
  stop can open on a rejection (`SHORT (VWAP REJECT)` is an entry, not an add). The
  **long side stayed silent all morning** — no `LONG` markers, no long
  sounds — because the pullback never held: every swing low after the top
  either undercut the running pullback low or sat under VWAP, so there was no
  higher low to buy; the long read never got beyond "waiting for a higher low
  above B". Known limits: it's read-only (no order routing —
  you still place the trade yourself), the gates are the fixed shape above,
  not an ML model — tunable per the drawer, not adaptive; the stop and VWAP
  rules use closes by default (`stopByClose` / the VWAP band are the
  opt-outs), so a wick through the stop that closes back under it does not
  stop the signal unless `stopByClose` is off; and, like the rest of the app,
  there is no holiday calendar — a holiday session is treated as a normal
  day.

- **9EMA flag signals (long only)** (the Signal window's default setup —
  user, 2026-10-05, from a BEAT 1-minute chart: "I want to get signals when a
  stock is breaking out and then in a more or less horizontal way is
  approaching the 9EMA. I want a signal or alert that the price is moving more
  or less horizontal on a stock."). A second pure engine,
  `web/src/signals/emaFlag.ts` (`evaluateFlag`), long side only. The shape: an
  impulse breakout, then the price drifts **sideways** (a flag) while the
  rising 9 EMA catches up underneath; the trade is the break of the flag high
  once the EMA is close, ridden with the 9 EMA. Each cell's head has a
  **setup** selector, **9EMA flag | Pullback**: flag is the default and what
  every new cell and every older saved layout becomes (a missing or unknown
  value reads as flag); Pullback is the engine above, unchanged. The S/L/S+L
  selector shows only on a Pullback cell. Exactly one engine runs per cell.
  Flag cells draw a blue 9 EMA (not VWAP), the flag boxes as locked rectangles
  (accent while active, green once broken, grey for an early break or a fade,
  red when failed), `Long > trigger` while flat/ready and the stop while long.
  The panel's Tuning drawer edits the flag params (`mktscanner.signal.flagParams.v1`,
  Reset included) when the active cell is a flag cell.

  *Rules and defaults* (all in `DEFAULT_FLAG_PARAMS`; the 9 EMA is the chart's
  own, run continuously over yesterday plus today). **Impulse** (`BREAKOUT`):
  a close at least **5%** (`minExtPct`) above the EMA on a bar whose high beats
  the highest high of the prior **30** bars of that ET day
  (`breakoutLookback`, needs at least 5 prior bars); the base low is the lowest
  low of the impulse bar and the **10** bars before it, same ET day
  (`impulseBaseBars`). **Flat** (the alert asked for): at least **3** closed
  bars (`minFlatBars`) at or after the top bar whose CLOSES span no more than
  **35%** of the move (top minus base low, `maxFlatOfMovePct`) and no more than
  **10%** of price (`maxFlatPct`), no close more than **1%** under the EMA
  (`emaBreakPct`), and the lowest low holding above the **50%** retrace of the
  move (`maxRetracePct`). The top bar counts toward the three bars (and its
  close must hold the EMA floor and sit inside the span) but it is the thrust
  the flag hangs from, not part of the flag: the retrace low, the flag's low
  (box and stop) and its running close range are measured on the bars after
  it. The flag high is the top it holds under; the trigger is that high plus
  **0.3%** (`breakBufferPct`). **9 EMA NEAR / READY**: the flag's close is within
  **4%** above the EMA (`readyDistPct`) — if already so when the flat is found,
  it is ready on the same bar with no separate 9 EMA NEAR. **Entry**
  (`LONG (FLAG)`): the high trades through the trigger while READY
  (`entryNeedsReady`, on); a break before that is only a list-only
  `EARLY BREAK` (chase risk), and the flag fades silently if its closes widen
  past the span caps. The stop is the higher of the flag low and the EMA of the
  last closed bar, **1%** under (`stopBufferPct`); then each closed bar —
  the entry bar's own close included — trails it up to EMA less 1%
  (`trailEma`, never lowered), and a CLOSE under the stop
  exits (`stopByClose`; off = any low at it, also on the forming bar). Later
  flag breaks while long are `ADD (FLAG)`, at most **2** (`maxAdds`). A close
  more than 1% under the EMA ends the setup (`FLAG FAILED` if a flag was
  open). Closed bars check the intrabar break/stop first, then the close
  rules, so a forming bar's event is never taken back when it closes.

  *A real walk-through, BEAT 2026-10-05, 1-minute (the numbers
  `emaFlag.test.ts` asserts).* `BREAKOUT` 08:31 (0.5412) — the chop after it
  never qualifies and a 08:40 close 1.6% under the EMA ends it silently — and
  `BREAKOUT` 08:55 (0.6252). Flag 1 is `FLAT` at 08:59: bars 08:57-08:59 at
  0.6055-0.6458, close 0.6247 only 3.3% over the EMA 0.6046, so it is ready on
  the same bar. `LONG (FLAG)` 09:00 at 0.6477 (0.6458 + 0.3%), stop 0.5994
  (7.5% risk; the 09:00 close then lifts the trail to 0.6075). The 09:01 bar
  runs to 0.86; flag 2 is `FLAT` at 09:03 (12.5% above the EMA, not ready; the
  box is 0.7531-0.8600, the thrust bar's own 0.6449 low is not the flag's), `9
  EMA NEAR` 09:07 (3.9%), and `ADD (FLAG)` 09:09 at 0.8626. Flag 3 is `FLAT`
  09:11 (0.9528 trigger) and `9 EMA NEAR` 09:12 (2.7%), and the 09:15 close
  0.8016, 3.0% under the EMA, is both the `EXIT` (EMA trail 0.8246: +23.8%
  from 0.6477) and `FLAG FAILED` for flag 3 (box 09:09-09:14, 0.8230-0.9500).
  A flag still open at an exit is marked `FLAG FAILED` only if that close is
  also more than `emaBreakPct` (1%) under the EMA — then it prints on the same
  bar as the `EXIT`, as here; otherwise the flag stays live after the exit. On
  5-minute bars only the 08:30 `BREAKOUT` fires (asserted in F10). Every 5m
  close stays 12-43% above the EMA, so the EMA is not what blocks a flag: the
  bars keep setting new tops (08:35, 08:50-09:05), the 08:35-08:45 window dips
  to 0.47, under the 50% retrace floor, and the 09:05-09:15 closes span 16.6%
  of price, over the 10% cap (found by relaxing one rule at a time; the test
  asserts only the events).

  *Sound and voice* (through `signals/view.ts`, same gating as the pullback
  cells: recent events only, never on a first evaluation). `FLAT` plays a new
  soft two-note tone (740 Hz, 2 x 120 ms) and says "BEAT: going sideways above
  the 9 EMA, 0.6055 to 0.6458, EMA 3.3% below." (the 08:59 flag 1 `FLAT`);
  `9 EMA NEAR` pings, "BEAT: 9 EMA caught up, 3.9% below. Long above 0.8626."
  (both distances are the EVENT bar's own, stamped on the event, so the line
  agrees with the events list even though a closed-bar event is first heard on
  the next bar's first tick; a close under the EMA is spoken as "above");
  `LONG (FLAG)` plays the long tone, "BEAT: long, flag breakout, 0.6477. Stop
  0.5994."; `ADD (FLAG)` the add note, "BEAT: add, flag breakout, 0.8626.";
  `EXIT` the bell, "BEAT: exit, closed below the 9 EMA, 0.8016, plus 23.8
  percent." (a close under the untrailed initial stop says "closed below the
  stop", a by-low touch with `stopByClose` off says "stop hit"); `FLAG FAILED` has no
  tone and speaks "BEAT: flag failed, lost the 9 EMA." only when not long and
  the same bar did not also SLAM (then only the slam line is spoken; see 9 EMA
  SLAM below). `SLAM (9 EMA)` plays the falling `slam` glide and says "BEAT:
  nine E M A slam". `BREAKOUT` and `EARLY BREAK` are silent. Tones that arrive
  together (a slammed long's EXIT and SLAM) play one after the other, in event
  order, never on top of each other. Prices are spoken as the chart
  prints them. Known limits: read-only, a decision aid and not an order
  router; the defaults are tuned on 1-minute and 5-minute penny momentum
  names (BEAT, SAIQ, ALEC, APUS, SDEV, QTEX fixtures), not an adaptive model;
  and, like the rest of the app, no holiday calendar.

  *Flag expiry (`maxFlagBars`, default 30; 0 = no limit).* A flag whose box
  spans more than 30 closed bars, counted from its top bar inclusive, without
  breaking ends silently with the box outcome `stale` (drawn like `faded`,
  dashed; no event, no sound). The state falls back to impulse (off the
  sideways/ready scans), and no new flag opens until a new top (a higher
  high); a span-`faded` flag may still re-form under the same top. Reason: a
  1-minute flag that has not resolved in about 30 minutes is a range, not a
  flag. Bars are bars, not minutes (a name with no trade prints no bar). Real
  example, PTC 2026-10-05 (a $192 stock, +34%, gapped at 04:13 and sideways all
  day): before, one flag was `active` 06:34-13:24 (373 bars) and READY all
  day; now the box ends `stale` at 07:16 (06:34 plus 31 bars) and the state is
  impulse at 13:24 ("RANGE - no flag: the top is over 30 bars old"). Sideways
  for six hours is a range. Both engines (`emaFlag.ts`, `internal/emaflag`)
  carry the rule, and the golden file pins it (including a `maxFlagBars=6`
  param set).

- **9EMA flag market scan (pre-market volume ≥ 1× the 10-day average)**
  (user, 2026-10-05: "Can we put in a function that this will scan the market
  for this setup. Like monitoring premarket stocks that traded at least 1x
  their 10x day average volume."). The Signal window's 9EMA flag, run by the
  server on every candidate instead of on the charts you have open.
  *Candidates* are the stocks whose `pmVolX` (pre-market volume ÷ 10-day
  average daily volume, the existing scan field) is at least **1**
  (`flagscan.MinPmVolX`), asked of the engine every **30 s**
  (`CandidateEvery`, `Engine.PmVolLeaders`), ranked by `pmVolX` and capped at
  **80** (`MaxCandidates`; the number beyond the cap is logged whenever it
  changes, never silent). A symbol that drops under 1x or out of the cap
  loses its state. *Data path = the Signal window's:* on admission the REST
  1-minute bars for the previous trading day plus today
  (`IntradayWithPrevDay`, 4 backfills at once, retried 5 s up to 2 min), then
  the per-second aggregates from a second tee on the existing ingest feed
  (no extra Massive socket) folded into the forming minute like
  `store.ts` `foldMinute`, and a REST resync every **5 min** per symbol
  (staggered) to heal reconnect gaps (a minute that ended while the request was
  in flight keeps the live close, the minute's last print, not REST's
  mid-minute snapshot of it). The tap is set before the backfill so no
  seconds are lost. The rules are `internal/emaflag`, a Go port of
  `emaFlag.ts` with the default params; it runs once a second on any candidate
  whose bars changed or whose bar just closed (3 s `CloseGrace`), so a verdict
  lands about **1-3 s after a minute closes**. A symbol with no bar on today's
  ET date gets no verdict (yesterday's final state is never published).
  *Parity:* `emaFlag.golden.test.ts` commits the TS engine's output over every
  fixture x 5 param sets x 2 clocks (`UPDATE_GOLDEN=1` regenerates);
  `internal/emaflag` and the whole service are tested against that same file,
  so the scan's state equals what the Signal window shows for the same bars.
  Synthetic `zz-tie-*` fixtures put a bar exactly on a rule boundary (a high on
  the trigger, a close on the stop, a close on the READY band) so a strict-vs-
  inclusive slip in either language fails a test, which no real capture does.
  *Columns* (group "9EMA flag (market scan)"): `e9State` (badge: IDLE,
  BREAKOUT, FLAT, READY, LONG), `e9Dist` (close vs the 9 EMA, %),
  `e9Trigger`, `e9Stop`, `e9FlagBars`, `e9Age` (seconds in the stage); all
  Null for a non-candidate. `e9Trigger` is Null while LONG with no add-flag
  (the reference carries no entry price). *Three seeded scans*
  (`seeded.e9flag.v1`, all `pmVolX >= 1`, limit 50, alert on enter, 300 s
  cooldown, with a new per-scan "Speak the symbol" option that says e.g.
  "SYM: nine E M A flag, sideways"): **9EMA Flag — sideways** (`e9State >= 2`,
  sound `flat`), **— ready** (`e9State == 3`, `ping`), **— breakout**
  (`e9State == 4` and `e9Age <= 300`, `long`). Because enter alerts fire once
  for each already-matching symbol on first sight, a mid-day restart speaks
  every currently matching name once. *Limits:* a name is a candidate only
  once `pmVolX` reaches 1, so thin early-pre-market names are not scanned at
  04:00; the REST budget is at most about 80 backfills plus 16 resyncs a minute
  (one evaluation pass costs about 4 ms for 80 symbols); read-only decision
  aid, not an order router. `SCANNER_E9FLAGS=0` turns the service off (the
  e9* columns stay Null). One status line a minute: candidates, evaluated,
  by state, backfill queue, tap drops.

  *Flag scan list in the Signal window's left bar.* Under the watchlist chips
  the sidebar shows a **FLAG SCAN · N** list (`FlagScanList`). Columns: ticker,
  stage (the same FLAT/READY/LONG/BREAKOUT badge as the grid), EMA (`e9Dist`,
  signed %, 1 dp) and age (`e9Age`, the grid's duration format); rows keep the
  scan's own server order, the tooltip reads "long above T · flag N bars · pm
  vol X×". Rows of the 9EMA Slam scan (below) are badged a red **SLAM**, their
  age is the time since the slam (`e9SlamAge`) and the tooltip reads "slammed
  X% under the 9 EMA · age ago · pm vol X×" (a row that also carries a stage
  shows the SLAM only while the slam is under 5 minutes old). A symbol already
  on a chart in that window gets a dot and accent text. Click a row to put it
  on the active cell (pinned, like typing it in the toolbar) and link the
  chart; rows also drag (text/plain) onto any cell. A picker in the head
  chooses the source scan (default 9EMA Flag - sideways, stage 2 and up; the
  four seeded e9 scans first - sideways, ready, breakout, then slam - then a
  "1-minute alerts" group - green dildo, dildo pullback, dildo touch, climax,
  bottom, badged DILDO / PB / TOUCH / CLIMAX / BOTTOM, whose rows show the
  dildo's body or the climax's drop and the age since the scan's own event -
  then your others), remembered in `mktscanner.signal.flagList.v1`. The 🔊/🔇 button
  is, for one of the nine seeded signal scans, that signal's switch in the
  toolbar's **Alerts** menu (the same state, see below); for a scan of your own
  it is this window's per-scan mute (default unmuted). A scan's alerts also
  play in any other window showing it, so mute one to avoid hearing it twice.
  The window narrows its scan view (`view.set` plus `scan.resync`, on feed up
  and on every change) to the chosen scan plus every signal switched on in the
  **Alerts** menu, and it hears the alerts of exactly those scans, minus the
  switched-off ones. The other windows that tile no scan of their
  own play and speak only the alerts of scans they view: the tape and review
  pop-outs none, a charts window those of its side boxes. Empty states:
  waiting for the scanner, scan switched off or missing (the seeded scans need
  a restart to appear), no rows.
  ChartsPane is unchanged. Needs a rebuilt and restarted scanner to appear.

  *9 EMA SLAM (bearish alert, 2026-10-07; user: "A candle after a stock has
  been up and trades more or less sideways SLAMS through the 9EMA.").* The same
  engine (`emaFlag.ts` and `internal/emaflag`, golden-pinned) emits `SLAM (9
  EMA)` on a CLOSED 1-minute bar when the state at the end of the previous bar
  was flat, ready or long (the run-up and its sideways flag, or the position
  out of it), the previous bar closed at/above the 9 EMA and this bar opened no
  more than `slamOpenTolPct` **0.5%** under it, and the bar closes at least
  `slamBelowPct` **3%** under its own EMA with a body (open − close) of at least
  `slamBodyAtr` **1** ATR (Wilder 14, previous bar; 0 = no ATR test) and
  `slamMinBodyPct` **2%** of the open, closing in the lowest `slamCloseInRange`
  **0.35** of its range, on at least `slamVolX` **1.3×** the mean volume of the
  up-to-10 earlier bars of the day (no volume test with fewer than 3). At most
  one per flag or position episode (the flag and the position open at the end
  of the previous bar are separate episodes, so a later flag still gets its
  slam while an older position that survived its own slam - possible only with
  edited stop/trail params - is still open; one slam marks both). It is listed
  after that bar's own events - usually FLAG FAILED / EXIT, or EARLY BREAK
  when the bar first broke a flag that was not yet ready (alone with no
  position; while long the same close also exits, so EARLY BREAK, EXIT, SLAM) -
  and it changes no state, level or trade event (`slam: false` turns it off).
  Reference: SPAI 2026-10-07, 08:15 ET (O 4.9107, L 4.50, C 4.566, 396,874
  shares) after a run to 5.10 and three sideways bars above the EMA: closed
  4.3% under the 9 EMA
  4.77, a 1.7 ATR body on 1.6× volume; 08:16 kept falling but opened under the
  EMA, so it is not a slam. The market scan publishes the day's latest slam
  per candidate as `e9SlamAge` (seconds since the slam bar began; it is known
  once the bar has closed, so it first shows at about 60 s) and `e9SlamPct`
  (close vs the EMA, negative), both Null with no slam today or for a
  non-candidate. Seeded scan **9EMA Slam** (`e9flag-slam`, its own key
  `seeded.e9slam.v1` so existing databases get it once and a deleted one stays
  deleted): `pmVolX >= 1` and `e9SlamAge <= 300`, freshest first, columns last,
  chgDay, pmVolX, e9SlamPct, e9SlamAge, e9Dist, news; alert on enter with its
  own falling `slam` tone, Speak on ("SPAI: nine E M A slam"), 300 s cooldown.
  The Signal window draws it as a red down marker on the bar and lists it in
  the events. A chart cell that sees a new slam plays the same `slam` tone and
  says "SPAI: nine E M A slam" itself (the window's mute and Voice toggles
  apply), and that line replaces the bar's FLAG FAILED line: on SPAI 08:15 only
  the slam is spoken. A long slammed out of its position plays the EXIT bell
  first and the slam's fall right after it, not on top of it. The window's flag
  list's picker offers the scan as "slam" (SLAM badge, age since the slam; see
  the flag scan list above); picking it makes the slam scan that window's
  listed scan, so its alerts are the slam's plus the dildo / climax alerts
  while their toggles are on. An alert only.

- **Green dildo -> 9 EMA pullback alerts (1-minute, whole market; 2026-10-07,
  user: "a lot of times when there is a green dildo candle i can enter on the
  9EMA for a scalp ... Can we have a signal scanner for the green dildo candles
  on the 1 minute as well?").** An ALERT, a "look now" prompt, not a system: on
  59 trading days of cached bars (2026-07-14..10-05 without Labor Day, the top
  150 names a day, 0.5% round-trip cost) no setting of the 26,244 tested beats the cost with any
  significance (the best estimates are positive but inside their noise, e.g.
  +0.64% with se 0.91); the chosen pullback entry nets -0.74% on a +3%/-2%
  bracket (se 0.18) with a 15-minute median of -0.77% gross, and waiting for
  the pullback only beats chasing the dildo close by about 1.1%. Two alerts in sequence. Alert 1, **Green Dildo**
  (`dildo-1m`, "SYM: green dildo", its own `dildo` tone): the engine judges
  every closed 1-minute candle of every ticker (built from the A.* per-second
  aggregates, judged once at the minute end + 3 s, so the alert comes about 3-4
  s after the close) with `internal/dildopb` (the Go port of
  `web/src/signals/dildoPb.ts`): body >= 8% of the open and >= 4x the mean body
  of the up to 10 previous traded bars (at least 3), volume >= 5x their mean, a
  close in the top 20% of the range, close >= $0.20 and candle dollar volume >=
  $100k. The row carries today's latest as `dildoAge` (seconds since the bar
  began, first seen at about 63 s), `dildoBody`, `dildoVolX`, `dildoDvol`. Alert
  2, **Dildo 9EMA Pullback** (`dildo-pb`, "SYM: nine E M A pullback", the `long`
  tone): the dildo admits the symbol to the 9EMA flag scan as a HOT name for 30
  minutes (at most 40, on top of the pmVolX cap; a REST backfill plus one resync
  20 s after the dildo bar), which looks on the chart's own bars and the chart's
  continuous 9 EMA for a closed bar within 20 minutes that touches EMA x 1.01,
  stays above the dildo's body midpoint and closes no more than 1% under the
  EMA: `dildoPbState` (0 none, 1 watching, 2 touching, 3 confirmed, 4 cancelled,
  5 expired), `dildoPbAge`, `dildoPbPx` (that bar's close, the entry the
  calibration measured), `dildoTouchAge`. **Dildo 9EMA Touch** (`dildo-touch`,
  state 2, the intrabar early cue) is seeded with its alert off. All three: seed
  key `seeded.dildo.v1`, alert on ENTER, 600 s cooldown, no pmVolX term.
  Calibration counts (per trading day): about 16.6 dildos a day in the cache
  (the shipped rule's acceptance re-run: 16.8), an estimated 18-29 market-wide;
  the shipped rule's pullback confirms on about 18% of them (176 of 990 in the
  re-run, 3.0 a day in the cache, roughly 4-5 market-wide; the calibration
  harness, which judged every dildo on its own, had 22% and 3.25 a day), a
  median 8 minutes after the dildo, and about 62% of dildos break their
  midpoint floor first. One pullback
  is watched at a time, for the LATEST dildo: a new dildo while one is still
  waiting replaces it, and the pullback is then judged against the new
  candle's midpoint floor and its own 20 minutes. So after two dildos in a row
  a pullback to the 9 EMA that holds the first candle's midpoint but breaks the
  second's is PB CANCELLED (floor) and alert 2 does not fire (WVVIP 2026-08-26:
  dildos at 04:01 and 04:02, cancelled at 04:09; the 04:01 dildo alone would
  have pulled back there at 7.9499). In the re-run 78 dildos were replaced
  inside their window, 34 of which would have pulled back judged on their own,
  which is why the shipped rule confirms less often than the harness. The
  scan's `last >= 0.20` and `dildoDvol >= 100000` terms restate the engine's
  floor so you can RAISE it; lowering them below it changes nothing. Limits:
  the scanner starts at about 03:30 ET, so the engine's prior bars are today's
  only (the chart's reach back into the previous evening), and after that daily
  cold start it misses about 12-13% of the chart's dildos (129 of 1,005 over the
  cached days): most in a name's first 3 traded bars of the day (mostly
  04:00-04:09), the rest at a name's 4th-10th traded bar of the day, where
  today-only priors give different means (e.g. CNET 09:30, NXL 08:34); it can
  also, rarely, fire on a bar the chart does not mark. A name the flag scan
  already tracks still gets alert 1 from the chart's own dildo; for any other
  name the missed dildo also means no alert 2 (the engine's hit is what admits
  it). There is no spread filter (A.* carries no quotes, so the price and
  dollar-volume floors are the only defence); alert 2 needs the 9EMA flag scan
  (with `SCANNER_E9FLAGS=0` it logs one line and only alert 1 works); a restart
  loses pending pullbacks; `SCANNER_DILDO=0` switches both alerts off (the
  engine's judge and the flag scan's own dildo pass over the names it tracks;
  `dildoPbState` then reads 0 on those). The flag scan's status line counts
  `hot`, `hotOnly`, `hotEvictions`, `dildoDrops`, `dildoParityMiss` (engine
  dildos the chart's bars do not show) and `dildoRewrites`.

- **Volume climax -> bottom confirmed alerts (the fourth signal, 1-minute, whole
  market; 2026-10-07, user: "I want a fourth signal. Climatic Volume on a bottom
  or sell off move.").** A "look now" prompt, not a system: the calibration (60
  days, 2026-07-13..10-05, the top 150 names a day, 0.5% round-trip cost) found
  no edge after costs: the confirmation entry's +15-minute median is -0.73% net
  (95% CI -1.18..+0.08). What it does find is bottoms: the climax low still
  holds 30 minutes later 49% of the time, against 24% for the same sell-off on
  any volume. Both alerts are the engine's (`internal/climax`, the Go port of
  `web/src/signals/climax.ts`; no REST): every closed traded 1-minute candle of
  every ticker is judged against the 10 previous traded bars of the SAME ET day
  (so never before the 11th bar of the day). Alert 1, **Volume Climax**
  (`climax-1m`, "SYM: volume climax", its own deep held `climax` tone, about 3-4
  s after the candle closes): a fresh low of those 10 bars at least 6% under
  their highest high, volume >= 8x their mean AND the biggest of them, a lower
  wick >= 25% of the range, a close under the 9 EMA (the engine's own 1-minute
  EMA, seeded from the day's first bars; it agrees with the chart's on 401 of
  the 402 calibrated candidates), close >= $0.50 and candle dollar volume >=
  $50k; bars starting 09:30-09:34 ET are muted (the worst slice). Alert 2,
  **Bottom Confirmed** (`climax-confirm`, "SYM: bottom confirmed", the `bottom`
  octave leap): within the next 5 traded bars of the day a close above the
  climax candle's midpoint before any low under the climax low (a new low first
  fails it, neither expires it), shown while the confirmation is under 4 minutes
  old and at most 20 minutes after the climax. A candle that confirms the
  climax AND is a new climax itself (a retest of the exact low on more volume
  that closes above the midpoint) still says "bottom confirmed": the row shows
  the confirmation until the next candle is judged, then the new climax takes
  over (the chart marks BOTTOM and CLIMAX on that candle). Row fields
  `climaxAge`, `climaxDropPct`, `climaxVolX`, `climaxWickPct`, `climaxDvol`, `climaxLow`,
  `climaxState` (1 watching, 2 confirmed, 3 failed, 4 expired), `climaxConfAge`,
  `climaxConfPx`. Seed key `seeded.climax.v1`, alert on ENTER, 600 s cooldown,
  no pmVolX term. Calibration counts: 6.1 climaxes a day in the cache (the
  shipped rule reproduces all 368 exactly), an estimated 18-23 a day
  market-wide; 64% confirm (a median 1 bar later), 27% fail and 9% do neither,
  so about 12-15 "bottom confirmed" a day market-wide; the median risk from the
  confirming close down to the climax low is 4.4%. The 10:00-15:59-only switch
  exists in the Signal window panel only (chart markers); the market scan always
  runs the defaults. The scan's `last >= 0.50` and `climaxDvol >= 50000` terms
  can only raise the floor. No spread filter. After a restart or a feed
  reconnect a name needs 10 traded bars before it can alert again (the minute
  the stream started or came back in was only partly seen: it is never judged
  and never a climax prior), and after a restart pending confirmations are lost.
  `SCANNER_CLIMAX=0` switches it off.

- **Bouncy ball short (1-minute, whole market; 2026-10-14, user: "It has a solid
  support and is making lower highs on each bounce of that support and after that
  will sell off sharply.").** An alert, a "look now" prompt, uncalibrated. The
  rule is `web/src/signals/bouncyShort.ts`, ported to Go as `internal/bbs` and
  pinned by golden vectors (`web/src/signals/fixtures/golden/bbs-golden.json`,
  regenerate with `UPDATE_GOLDEN=1 npx vitest run src/signals/bouncyShort.golden.test.ts`;
  the inputs are a synthetic AAOI-2026-10-14-shaped day - support 115.85 touched
  09:50 / 10:05 / 10:20 / 10:35, bounce highs 119.9 -> 117.8 -> 117.4 -> 116.8,
  break 10:40 - plus negative shapes and seeded random walks; no network). The
  engine keeps each ticker's last 90 traded 1-minute bars of the ET day: a
  support S is the lowest low of a base (walking back, a low more than 0.4%
  under the running low starts a new candidate base), touched >= 3 times (lows
  within 0.4% of S, >= 3 bars apart with a bounce to S+0.5% between), the
  trailing bounce highs each >= 0.2% lower than the last (>= 2 lower highs);
  a closed bar closing > 0.3% under S is **BOUNCY BALL SHORT** (the window
  then restarts after it). Scan **Bouncy Ball Short** (`bouncy-short`, seed key
  `seeded.bbs.v1`): `bbsAge <= 240`, `last >= 1`, `bbsDvol >= 100000`, alert
  on ENTER with the falling `bounce` sound, "SYM: bouncy ball short", 600 s
  cooldown. Row fields `bbsAge`, `bbsSupport`, `bbsState` (1 forming: base +
  lower highs with the close within 1% of S - make a scan on `bbsState = 1`
  for a forming list; 2 broke today), `bbsDvol`. The Signal window has its
  Alerts-menu switch and left-bar list entry (badge BBS), 1-minute flag cells
  draw a BBS marker at the break and a dotted support line, and the SIGNALS
  history records kind `bbs`. `SCANNER_BBS=0` switches it off.

- **Ignition (1-minute, whole market; 2026-10-14).** The small first candle
  out of a quiet base right before a dildo (example NCPL ~09:06 ET: flat
  1.75–1.80, a +5% candle on a volume spike, the dildo the next minute). An
  alert, a "look now" prompt, uncalibrated. The rule is
  `web/src/signals/ignition.ts`, ported to Go as `internal/ignition` and pinned
  by golden vectors (`web/src/signals/fixtures/golden/ignition-golden.json`,
  regenerate with `UPDATE_GOLDEN=1 npx vitest run src/signals/ignition.golden.test.ts`);
  the engine judges every closed 1-minute bar (`internal/engine/ignition.go`,
  zero allocations). A closed candle is an **IGNITION** when the 15 candles
  before it (at least 10, any day) span ≤ 8%, its volume is ≥ 5× the prior 10
  candles' mean, its green body is 2–8% (bigger is a dildo), it closes above
  the base high and its high is a new high of the ET day (from 04:00; the
  base/day-high part is `dildopb.JudgeEarly`, shared), with close ≥ $0.20 and
  ≥ $50k traded in it. One per base (re-armed once a later candle's base is
  not quiet, or a new day), at most one per 10 minutes. Scan **Ignition**
  (`ignition`, seed key `seeded.ignition.v1`): `ignAge <= 240`, alert on ENTER
  with the short sharp `ignite` sound, "SYM: ignition", 600 s cooldown. Row
  fields `ignAge`, `ignBodyPct`, `ignVolX`. The Signal window has its
  Alerts-menu switch (on by default) and left-bar list entry (badge IGN),
  1-minute flag cells draw an IGN ▲ marker, and the SIGNALS history records
  kind `ignition`. `SCANNER_IGNITION=0` switches it off.

- **Signal switches in the Signal window (2026-10-08).** The toolbar's
  **🔔 Alerts n/9** menu has one checkbox per signal: 9EMA Flag sideways,
  9EMA Flag ready, 9EMA Flag breakout, 9EMA Slam, Green Dildo, Dildo 9EMA
  Pullback, Dildo 9EMA Touch (off by default, the opt-in intrabar cue), Volume
  Climax and Bottom Confirmed - every other one on by default. Remembered per
  browser in `mktscanner.signal.switches.v2` (the old **Dildo alerts** /
  **Climax alerts** toggles it replaces, `mktscanner.signal.dildoAlerts.v1` /
  `mktscanner.signal.climaxAlerts.v1`, are carried over on first load: off
  there = both of that rule's alerts off). A switched-ON signal's scan is in
  the window's scan view, so it hears both alerts of each dildo / climax
  sequence whatever its flag list shows. A switched-OFF signal makes no tone
  and no voice in this window only (other windows are unaffected): its scan
  is not subscribed - unless the left-bar list shows it, whose rows still
  list while its alerts stay silent - and the matching chart-cell cue on your
  own chart stocks is silent too (a flag cell's FLAT = sideways, 9 EMA NEAR =
  ready, LONG / ADD / EXIT = breakout, SLAM = slam, FLAG FAILED while any
  stage is on; pullback cells are not one of the nine signals and are never
  gated). An event that arrived while its signal was off is not replayed when
  it is switched back on. The window's 🔊/🔇 and 🗣 Voice still cover
  everything, including a burst already queued: 🔇 cuts the tones still to
  play and the spoken lines still to come at once, and Voice off cancels the
  queued spoken lines (the tones play on).
  Alert tones that fire together (one minute's close can bring a dildo, a
  climax and a flag alert at once) play one after another, never as one chord,
  and a burst of spoken alert lines is spoken in full: the voice's 3-utterance
  cap only trims the tape reader's narration (a runaway past 12 queued alert
  lines skips the newest). On 1-minute flag cells the chart marks the rules,
  silently (the scans' alerts are the only sound): ▲ DILDO at the dildo's
  close, ▲ 9 EMA PB at the pullback bar's close, ▲ CLIMAX at the climax low,
  ▲ BOTTOM at the confirming close, joining a buy marker already on the bar
  ("LONG (FLAG) · PB"); the cell's events list adds them with their notes, plus
  PB CANCELLED and CLIMAX FAILED. The panel's **Green dildo** and **Volume
  climax** Tuning drawers (`mktscanner.signal.dildoParams.v1` /
  `mktscanner.signal.climaxParams.v1`, the master switches hide the markers)
  change the chart markers only; the market scans always run the defaults.

- **Today's symbol blacklist** (`internal/blacklist`): symbols whose scan
  alerts make no tone or speech today, in every window; the Day Log still
  records their alerts and flags them (⛔, "hide blacklisted"). Edited in the
  Signal window's right column (**Blacklist (today)**: type a symbol, Add, × to
  remove); wire `blacklist` {syms} (hello + every change), `blacklist.add` /
  `blacklist.remove` {sym}. Kept in `schema_meta` under `autotrade.blacklist.v1`
  (the key of the former auto-trader, so a list saved before its removal
  carries over) for one ET day; cleared at 00:00 ET.

- **The auto-trader was removed (2026-10-20).** The paper auto-trader
  (`internal/autotrade`, its IB Gateway paper connection on client 78, the AUTO
  strip, `auto.*` frames) no longer exists. Its data under `~/.marketscanner`
  (the `autotrade/` audit folder, `autotrade.settings.v1` in `schema_meta`) is
  left in place and simply not read. `github.com/scmhub/ibapi` v0.10.37 stays
  in `go.mod` only as the test oracle of `internal/orderticket`
  (`twsclient_ibapi_test.go`). The order ticket (client 80) and the TWS link
  (client 77) are unchanged.

See the architecture discussion for the full WS contract, scan model, and the
adversarial-review fixes that shaped the reconciled design.

## Layout

```
cmd/scanner/          main: wiring + lifecycle (TBD)
internal/
  core/               L0 domain types + interfaces (DONE) — every package depends on this
  config/ logx/       L0 config + slog setup (TBD)
  engine/             L1 hot path: shards, ring buffer, 1 Hz clock, merger, metrics (DONE, tested + race-clean)
                      (the merger lives inside engine — it needs shard internals — so there is no separate merge/ package)
  universe/           L1 regular-securities filter (TBD)
  feed/               L2 Massive WS+REST client implementing core.FeedClient (DONE, tested + race-clean)
  ingest/             L3 wires feed -> engine: parse, intern, route, coalesce (TBD)
  scan/               L2 scanner + predicate compile + alert hysteresis (DONE, tested + race-clean)
  news/               L3 Benzinga + reference news pollers, dedupe, article cache (DONE, tested)
  summarize/          L3 Claude summarizer: API (Go SDK) + Claude Code CLI backends (DONE, tested)
  autosum/            L3 auto-summarizer: scan entry -> day-news fetch -> Claude -> broadcast (DONE, tested)
  tape/               L2 time & sales: aggressor classification, volume delta, big prints, 1 Hz digest (DONE, tested)
  schedule/           L3 nightly RVOL recompute (self-built curves) + weekly float, ET-aware (DONE, tested + race-clean)
  rvolfill/           L3 RVOL baseline backfill from Massive minute bars, ranked + capped (DONE, tested + live-verified)
  persist/            L3 SQLite (modernc.org/sqlite) implementing the *Repo ports (DONE, tested)
  server/             L3 net/http + embed.FS + /stream WS hub (DONE, tested + race-clean)
web/                  React + Vite + TS app (DONE); built to dist/, embedded via //go:embed
                        src/        components, WS client, Zustand store, Web Audio alerts
                        dist/       build output (gitignored; run `npm run build` to regenerate)
spike/                Phase-0 spike: confirm WS wildcard 'A.*' (RUNNABLE, own module)
spike-flatfiles/      Phase-0b spike: confirm flat-file (S3) access (RUNNABLE, own module)
```

**Import law:** imports point downward only; `engine` (hot path) imports only
`core` + `logx` and never `server`/`scan`/`feed`/`persist`. `cmd/scanner` is the
only package allowed to import everything (it constructs and injects).

## Phase-0 spikes (run these first)

The whole design rests on two assumptions. Both are now backed by docs research,
but must be confirmed empirically against *your* account before building further.

1. **WS wildcard** — that `A.*` streams the whole market over one socket:
   ```powershell
   cd "spike"; $env:MASSIVE_API_KEY="YOUR_REALTIME_KEY"; go mod tidy; go run .
   ```
   Run near 09:30:05 ET — it also probes the `av` (accumulated daily volume)
   anchor that RVOL correctness depends on (04:00 vs 09:30).

2. **Flat-file (S3) access** — that the RVOL baseline can be bulk-seeded.
   Research confirms flat files are **included in all paid Massive plans**, bucket
   `flatfiles`, path `us_stocks_sip/minute_aggs_v1/YYYY/MM/YYYY-MM-DD.csv.gz`.
   The S3 credentials are **separate from your REST/WS API key** — generate an S3
   Access Key ID + Secret in the dashboard.
   ```powershell
   cd "spike-flatfiles"
   $env:MASSIVE_S3_KEY_ID="<S3 key id>"; $env:MASSIVE_S3_SECRET="<S3 secret>"
   go mod tidy; go run . 2026-06-22   # a recent weekday trading day
   ```
   The key line to watch: `PASS: extended-hours (pre-09:30 ET) data present` —
   this confirms the files carry the pre-market data the 04:00 RVOL anchor needs.

Success criteria are printed by each spike.

## Design docs

- [docs/ui-architecture.md](docs/ui-architecture.md) — React UI components, the
  flash-via-refs rendering model, Zustand+refs state tiers, Web Audio alerts, and
  the Vite→`//go:embed` single-binary pipeline.
