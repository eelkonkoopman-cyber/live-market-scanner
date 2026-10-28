# Alpaca market-data feed

Massive (`internal/feed`) stays the default; its code path is unchanged.
`SCANNER_FEED=alpaca` switches the scanner to `internal/alpaca`, which
implements the same method set (`core.FeedClient` + the REST helpers that
`cmd/scanner` wires, collected in the `marketFeed` interface in
`cmd/scanner/feedsel.go`).

## 1. Massive dependency map

| What | Massive endpoint / channel | Code | Consumers | Alpaca replacement |
|---|---|---|---|---|
| Live stream | WS `wss://socket.massive.com/stocks`, `A.*` (per-second aggregates, whole market) | `feed/ws.go`, `decode.go`, `fastdecode.go` | `ingest` -> engine (every row, alert, 1m/5m candle), chart tap, 9EMA flag tap | WS `wss://stream.data.alpaca.markets/v2/sip`, `trades:["*"]`; per-second aggregates built from trades (`alpaca/secagg.go`) |
| Tape (time & sales) | same WS, `T.<sym>`, `Q.<sym>` on demand | `feed/ws.go` WatchTapeSet | `tape` engine | trades come from the wildcard; `quotes:[syms]` (un)subscribed on demand |
| Startup / reconnect seed | `GET /v2/snapshot/locale/us/markets/stocks/tickers` (whole market) | `feed/snapshot.go` | `ingest.seed`, prefill, prevah, rvolfill (`Seeds`) | `/v2/assets` list + chunked `GET /v2/stocks/snapshots?symbols=...&feed=sip` |
| Grouped daily | `GET /v2/aggs/grouped/locale/us/market/stocks/{date}?adjusted=true` | `feed/intraday.go` GroupedDaily | `schedule` nightly trend refresh -> `daily_close_history` (daily levels, adv10, avg volume, DTrend) | chunked multi-symbol `GET /v2/stocks/bars?timeframe=1Day&adjustment=split` over the asset list |
| 1m bars | `GET /v2/aggs/ticker/{sym}/range/1/minute/{from}/{to}?adjusted=true` | Intraday, IntradayOn, IntradayBetween, IntradayWithPrevDay | prefill (today), prevah (prev day AH high), rvolfill (RVOL curve backfill), chart.get, flag scan / monitor charts | `GET /v2/stocks/{sym}/bars?timeframe=1Min&adjustment=split&feed=sip`, paged by `next_page_token` |
| Daily range | `GET /v2/aggs/ticker/{sym}/range/1/day/...` | DailyRange | Episodic Pivot worker | same bars endpoint, `timeframe=1Day` |
| Previous close | `GET /v2/aggs/ticker/{sym}/prev` | PrevClose | chart.get prior-close line | `GET /v2/stocks/{sym}/snapshot` (prevDailyBar / stale dailyBar) |
| Ticker list | `GET /v3/reference/tickers?type=CS|ADRC|ETF` (paged) | `feed/reference.go` | `universe` (regular securities filter, sec types) | trading API `GET /v2/assets?status=active&asset_class=us_equity`, type by name heuristic |
| Shares outstanding | `GET /v3/reference/tickers/{sym}` | `feed/floats.go` | `schedule` float refresh | **none** - returns nothing; stored floats are kept but go stale |
| Fundamentals | `/stocks/financials/v1/{income,balance,cash-flow statements}` | `feed/fundamentals.go` | fundamentals.get panel | **none** - returns `core.ErrNotEntitled` |
| News (reference) | `GET /v2/reference/news` | `feed/news.go` | `news` poller | `GET /v1beta1/news` (Benzinga) without content |
| News (Benzinga) | `GET /benzinga/v2/news` | `feed/benzinga.go` | `news` poller, autosum | `GET /v1beta1/news?include_content=true`, IDs kept as `bz<id>` so they dedupe with bzstream |
| Splits | `GET /v3/reference/splits` | `cmd/backtest/splits.go` only | backtest | not ported (backtest stays on Massive / its cache) |
| API key | `MASSIVE_API_KEY` or `~/.marketscanner/apikey` (`config.Load`), settable from the UI | `config` | everything above | `~/.marketscanner/alpaca.json` or `ALPACA_KEY_ID` + `ALPACA_SECRET_KEY`; the UI key box is refused in Alpaca mode |

Not Massive (unchanged by the switch): Benzinga push (`bzstream`), RTPR, EDGAR,
TWS/order ticket.

`cmd/backtest` still uses `internal/feed` directly (grouped daily + minute bars
+ splits). It runs off its on-disk cache; refetching after the Massive plan
ends needs porting.

## 2. Settings

| Env | Default | Meaning |
|---|---|---|
| `SCANNER_FEED` | `massive` | `alpaca` selects the Alpaca provider |
| `SCANNER_ALPACA_FEED` | `sip` | `iex` = free plan (testing) |
| `SCANNER_ALPACA_SYMBOLS` | (empty = `*`) | fixed symbol list; needed on IEX (30 symbols, wildcard refused) |
| `ALPACA_KEY_ID` / `ALPACA_SECRET_KEY` | | override the json file |
| `ALPACA_TRADING_BASE` / `"tradingBase"` in the json | `https://api.alpaca.markets` | host for `/v2/assets` (paper keys: `https://paper-api.alpaca.markets`) |

`~/.marketscanner/alpaca.json`:

```json
{"keyId":"AK...","secret":"..."}
```

Keys go only into request headers (`APCA-API-KEY-ID`, `APCA-API-SECRET-KEY`)
and the websocket auth frame; never into URLs, logs or error text (tests
check this).

## 3. How the stream works

* Handshake: `connected` -> `{"action":"auth","key","secret"}` ->
  `authenticated` -> `{"action":"subscribe","trades":["*"]}` -> subscription ack.
  JSON is used (msgpack is not); frames are arrays of batched messages.
* Errors: 401/402/404 -> `ErrAuth`; 406 connection limit -> `ErrConnLimit`
  (one stream per key: do not run feedcompare while the scanner runs on
  Alpaca); 405/409/410 -> `ErrSubscription`. All retry with backoff (>=30s
  for these), so a fixed key file recovers without restart (keys are read at
  start, so a changed file needs a restart).
* Reconnect: capped exponential backoff, reset after a healthy run; every
  (re)connect calls `onReset`, which re-seeds from snapshots, exactly as with
  Massive. Ping every 15s.
* Staleness: weekday 04:00-20:00 ET, no frame for 60s -> close and reconnect.
* Per-second aggregates (`secagg.go`): trades are grouped by symbol and
  exchange-timestamp second. A bucket is emitted when the symbol's next
  trade lands in a later second, or by a 250ms flusher once the second is
  1.5s in the past. Late prints fold into the open second, or forward into
  the next one (never backwards). Fields: OHLC from price-eligible trades,
  Vol = all volume-eligible trades, VWAP = bucket VWAP, AccVol / DayVWAP
  accumulated per ET day (seeded from the snapshot, never lowered), DayOpen
  = first price-eligible trade at/after 09:30 ET (or the snapshot's day open).

## 4. Known differences vs Massive

* **No native per-second aggregates.** They are rebuilt from trades. The
  equivalence test shows the two paths give identical engine rows for the
  same prints, but Massive's own condition rules are not public in full;
  our table (`noPrice`, `noVol` in `secagg.go`): price ignores
  B W 4 7 9 C G H I M N P Q R V Z; volume ignores M Q 9. Odd lots (I) add
  volume only. Form T (extended hours) counts. CTA and UTP letters are
  treated alike (a few letters differ in meaning per tape).
* **Emission delay.** A thin symbol's second is emitted up to ~1.75s after it
  ends (Massive: ~immediately). Busy symbols emit on the next trade.
* **Wildcard trade volume.** Whole-market SIP trades are far more messages
  than `A.*` (peaks in the tens of thousands per second at the open). The
  decoder is plain `encoding/json`; if feedcompare shows the reader falling
  behind (trade latency growing), add msgpack or a hand decoder.
* **DayOpen** is the first regular-session trade, not the listing exchange's
  official opening print (Massive `op`). Gap% can differ by a tick or so.
* **Snapshot dailyBar** before today's first trade is yesterday's bar; handled
  (treated as previous close). Whether Alpaca's daily bar volume includes
  pre-market is unverified: if it does not, the volume seeded on a restart
  during pre-market is too low (live accumulation is unaffected). Check
  with feedcompare / one morning restart.
* **Daily / minute volumes** may differ from Massive (Massive's grouped volumes
  are fractional; condition rules for bars differ by vendor). Compare with
  feedcompare before trusting RVOL baselines built across the switch.
* **Grouped daily** only covers currently active assets; symbols delisted
  since the date are missing from back-filled history.
* **Security types** are a name heuristic (Alpaca's asset list has none):
  ETF/ETN by name/brand, Warrants, Units, Rights, preferreds / notes, ADRs;
  the rest is CS; non-national-exchange listings are OTC and filtered out.
  Some funds/units will be mis-typed.
* **Symbol formats** for preferreds/classes may differ (e.g. `BRK.B` vs
  other notations); only matters for names carried across the switch.
* **Quote sizes** are multiplied by 100 (round lots) as for Massive;
  unverified for Alpaca - check the tape bid/ask sizes once live.
* **No floats, no fundamentals.** Float column keeps the last Massive values.
* **News**: Alpaca news is Benzinga; it overlaps the Benzinga push stream and
  dedupes on `bz<id>`. The "reference news" source (other publishers) is gone.
* **IEX**: only ~2-3% of volume and 30 symbols; fine for plumbing tests only.

## 5. Probe: cmd/feedcompare

Build outside the repo:

```
go build -o %TEMP%\feedcompare.exe ./cmd/feedcompare
```

Run (read-only):

```
feedcompare.exe -minutes 10                      # Alpaca: trade-built 1m bars vs Alpaca's own 1m bars, latency
feedcompare.exe -minutes 10 -syms AAPL,TSLA,NVDA # restrict
feedcompare.exe -minutes 10 -massive             # ALSO Massive - only while the scanner is NOT running
feedcompare.exe -feed iex -syms AAPL,MSFT        # free IEX keys
```

Report: coverage (symbols per source), latency p50/p95/p99, and per-pair
volume totals, high/low mismatches and the top-N symbols by volume
difference (newest, still-forming minute excluded).

## 6. Switching (Monday 2026-11-02)

1. Create `%USERPROFILE%\.marketscanner\alpaca.json` with the keys.
2. Optional, before the Massive plan ends and with the scanner stopped:
   `feedcompare.exe -minutes 15 -massive` during pre-market.
3. Start the scanner with `SCANNER_FEED=alpaca` (e.g. add
   `set SCANNER_FEED=alpaca` to run.bat before the scanner line). The status
   bar shows "Alpaca feed".
4. Roll back: remove the variable (Massive is the default).
