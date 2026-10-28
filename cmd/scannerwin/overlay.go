package main

// overlayJS is injected on every document (go-webview2 Init →
// AddScriptToExecuteOnDocumentCreated, runs before window.onload) so the
// window stays movable/closable even on Edge's connection-error page when the
// scanner backend is down. It renders a small ≡ pill pinned top-center:
// semi-transparent until hovered, tiny (18px tall) so it never obstructs
// the app's own top bar, max z-index so nothing covers it. Mousedown on the
// grip calls the bound Go func winDrag(), which runs the cursor-polling
// dragLoop (see win32_windows.go for why the classic WM_NCLBUTTONDOWN trick
// fails under WebView2); the – and ✕ buttons appear on hover and call
// winMinimize() / winClose(). window.winDrag etc. are installed by go-webview2's Bind
// bootstrap, which is also an Init script registered earlier — registration
// order is preserved, so they exist by the time this runs.
//
// Pill colours read the page's own --ab-* theme tokens (rgb(var(--ab-x, R G
// B) / a)) so the pill follows whatever palette the scanner (or any other
// page it briefly shows, e.g. mid-navigation) is using, with the original
// literal RGB as the `var()` fallback for pages that don't define them at
// all — Edge's connection-error page when the backend is down being the
// whole reason this overlay exists in the first place.
const overlayJS = `(function () {
  if (window.__scannerwinOverlay) return;
  window.__scannerwinOverlay = true;
  /* Debug beacons: only when the shell appended swdbg=1 (SCANNERWIN_DEBUG).
     document.title is NOT readable from outside (the host never syncs it to
     the Win32 title), so diagnostics leave the process as HTTP hits on a
     local collector instead. Fire-and-forget, swallowed in production. */
  var SWDBG = /[?&]swdbg=1/.test(location.search);
  function beacon(msg) {
    if (!SWDBG) return;
    try { fetch('http://127.0.0.1:7999/sw?' + encodeURIComponent(msg), { mode: 'no-cors' }); } catch (_) {}
  }
  window.__swBeacon = beacon;
  function install() {
    if (document.getElementById('__sw_pill')) return;
    var css = document.createElement('style');
    css.textContent =
      '#__sw_pill{position:fixed;top:4px;left:50%;transform:translateX(-50%);' +
      'z-index:2147483647;display:flex;align-items:center;gap:4px;height:18px;' +
      'padding:0 8px;border-radius:9px;background:rgb(var(--ab-text-muted, 110 115 130) / 0.28);' +
      'opacity:0.55;transition:opacity 120ms,background 120ms;' +
      'font:600 12px/18px system-ui,sans-serif;color:rgb(var(--ab-text-secondary, 207 211 220));' +
      'user-select:none;-webkit-user-select:none;}' +
      '#__sw_pill:hover{opacity:1;background:rgb(var(--ab-bg-elevated, 32 34 42) / 0.92);}' +
      '#__sw_pill .__sw_grip{cursor:grab;padding:0 4px;}' +
      '#__sw_pill .__sw_btn{display:none;cursor:pointer;padding:0 4px;' +
      'border:0;background:none;color:rgb(var(--ab-text-secondary, 207 211 220));font:inherit;}' +
      '#__sw_pill:hover .__sw_btn{display:block;}' +
      '#__sw_pill .__sw_btn:hover{color:rgb(var(--ab-text-primary, 255 255 255));}' +
      '#__sw_pill .__sw_close:hover{color:rgb(var(--ab-loss, 255 107 107));}';
    document.head.appendChild(css);
    var pill = document.createElement('div');
    pill.id = '__sw_pill';
    var grip = document.createElement('span');
    grip.className = '__sw_grip';
    grip.textContent = '≡'; /* ≡ */
    grip.title = 'drag to move window';
    /* The WHOLE pill is the drag surface (the first cut bound only the ~20px
       grip glyph, which made center presses land on the hover-revealed
       buttons instead — effectively undraggable). The buttons stop
       propagation on mousedown so a – or ✕ press never starts a drag. */
    pill.addEventListener('mousedown', function (e) {
      if (e.button !== 0) return;
      e.preventDefault();
      beacon('dragcall');
      try {
        var r = window.winDrag();
        if (r && r.catch) r.catch(function (err) { beacon('dragreject: ' + err); });
      } catch (err) {
        beacon('dragthrow: ' + err);
      }
    });
    var min = document.createElement('button');
    min.className = '__sw_btn';
    min.textContent = '–'; /* – */
    min.title = 'minimize';
    min.addEventListener('mousedown', function (e) { e.stopPropagation(); });
    min.addEventListener('click', function () { window.winMinimize(); });
    var close = document.createElement('button');
    close.className = '__sw_btn __sw_close';
    close.textContent = '✕'; /* ✕ */
    close.title = 'close window';
    close.addEventListener('mousedown', function (e) { e.stopPropagation(); });
    close.addEventListener('click', function () { window.winClose(); });
    pill.appendChild(grip);
    pill.appendChild(min);
    pill.appendChild(close);
    document.body.appendChild(pill);
    /* Bind-presence probe: if the go-webview2 Bind bootstrap didn't install
       the bound functions, say so via the title so it's diagnosable from
       outside even with the message pipeline down. */
    beacon('ready winDrag=' + typeof window.winDrag);
    document.addEventListener('mousedown', function (e) {
      var t = e.target || {};
      beacon('down ' + e.clientX + ',' + e.clientY + ' ' + (t.id || t.className || t.tagName || '?'));
    }, true);
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', install);
  } else {
    install();
  }
})();`

// nativeShellJS is injected alongside overlayJS (same Init mechanism, runs on
// every document before window.onload). It marks the environment for the web
// app (window.__nativeShell — unused today, available for feature detection)
// and overrides window.open so the frontend's five existing call sites
// (ScanTabs.tsx x3, TileFrame.tsx, SymbolPanel.tsx) spawn NATIVE borderless
// windows with zero web/src changes.
//
// Flow: every window.open forwards its raw URL to the bound Go func
// openPaneURL(raw) (a Promise — go-webview2 Bind bootstrap, registered
// BEFORE this script so window.openPaneURL exists; Init order is
// registration order). Go parses it with ParsePaneURL:
//   - handled=true  → a scannerwin.exe child process was spawned; the
//     override returns null, which is within the window.open contract
//     (callers get null when a popup is suppressed) and all five frontend
//     call sites ignore the return value anyway (verified).
//   - handled=false (non-pane URL) or rejection (spawn failed) → fall
//     through to the ORIGINAL window.open. With no NewWindowRequested
//     handler registered (jchv/go-webview2 exposes only the raw vtbl slot),
//     WebView2's documented default applies: "the target content will be
//     opened on a popup window" — a plain WebView2 popup with an OS title
//     bar, not the default browser. No frontend code emits such URLs today.
//
// The fallback is asynchronous (Promise round-trip), which is fine because a
// popup opened outside the synchronous click handler is not gesture-gated in
// WebView2 popups the way browser popup-blockers gate it — and the primary
// path (pane URLs) never falls through.
const nativeShellJS = `(function () {
  if (window.__nativeShell) return;
  window.__nativeShell = true;
  var origOpen = window.open ? window.open.bind(window) : function () { return null; };
  window.open = function (url, target, features) {
    var raw = '';
    try { raw = url == null ? '' : String(url); } catch (e) {}
    var p = null;
    try { p = window.openPaneURL(raw); } catch (e) {}
    if (p && typeof p.then === 'function') {
      p.then(function (handled) {
        if (!handled) origOpen(url, target, features);
      }, function () { origOpen(url, target, features); });
      return null;
    }
    return origOpen(url, target, features);
  };
})();`
