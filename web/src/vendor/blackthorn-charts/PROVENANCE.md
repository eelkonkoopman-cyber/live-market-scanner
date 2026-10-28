# Blackthorn chart engine — vendored copy

These `.ts` files are copied verbatim from AfterBell's chart engine, which
has been the engine's source of truth since 2026-09-12 (see AfterBell's own
PROVENANCE.md next to the source below). `D:\TradingView` is NOT the
source of truth for this engine any more — do not sync from it.

- Source: D:\Blackthorn Live\src\lib\vendor\blackthorn-charts
- AfterBell commit: 2374843 2026-09-14 appdb: the app login must not bypass row-level security; grants automatic
- Uncommitted engine changes included: yes — chart.ts, core/types.ts, index.ts, render/crosshair.ts, render/drawings.ts, render/grid.ts, render/lastprice.ts, render/markers.ts, render/pricelines.ts, render/timeaxis.ts, render/fonts.ts
- Synced: 2026-09-14T07:27:30.763Z
- Files copied: 25

## Rules

- Do not hand-edit anything under this directory in the scanner — every file
  here is overwritten by the next sync.
- To change the engine, edit it in AfterBell (the source path above), then
  from `web/` run `npm run sync:charts` to pull the change into the scanner.
