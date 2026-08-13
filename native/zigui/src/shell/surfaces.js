// Native-surface rect reporter (SDL_WEBVIEW_SURFACE_DESIGN §4.4) - the CHILD's runtime, not the
// daemon's. Embedded into boot_js AFTER the daemon's runtimeJS, so window.__patch already exists.
//
// It discovers surfaces from the DOM and reports them; open/close is never commanded from Go:
// a [data-surface] element appearing IS the open, disappearing IS the close (§4.3).
//
// P0 gotcha #3, honoured: put_Bounds reaches the renderer ASYNCHRONOUSLY, so the page's first
// layout can use a stale viewport with a `resize` event behind it. Every measurement here is
// therefore observer-driven (MutationObserver + ResizeObserver + IntersectionObserver + scroll/
// resize), never a one-shot measure at document-ready.
//
// Bounds: at most 8 surfaces on the wire (drop-newest, the count travels so the child can log it);
// one rAF-coalesced report per frame; identical consecutive reports are not sent.
(function () {
  'use strict';
  var CAP = 8;
  var els = [];            // tracked [data-surface] elements, document order
  var seen = new WeakMap(); // el -> IntersectionObserver verdict
  var last = '';           // last payload, for the identical-consecutive drop
  var raf = 0, needScan = true;

  // clip intersects the element rect with every scrolling ancestor and the viewport, and returns
  // BOTH rects: the visible one (the child sizes its swapchain to it, which is how
  // IDCompositionVisual::SetClip stays unused) and the FULL one.
  //
  // Both are load-bearing since P3. A producer's picture belongs to the full rect; the visible rect
  // only says how much of it survives the scroll. Reporting the visible rect alone - P2's state -
  // means a half-scrolled element squashes the picture into the strip that is left.
  function clip(el) {
    var r = el.getBoundingClientRect();
    var L = r.left, T = r.top, R = r.right, B = r.bottom;
    for (var n = el.parentElement; n; n = n.parentElement) {
      var cs = getComputedStyle(n);
      if (cs.overflowX !== 'visible' || cs.overflowY !== 'visible') {
        var q = n.getBoundingClientRect();
        if (q.left > L) L = q.left;
        if (q.top > T) T = q.top;
        if (q.right < R) R = q.right;
        if (q.bottom < B) B = q.bottom;
      }
    }
    if (L < 0) L = 0;
    if (T < 0) T = 0;
    if (R > window.innerWidth) R = window.innerWidth;
    if (B > window.innerHeight) B = window.innerHeight;
    return {
      x: L, y: T, w: Math.max(0, R - L), h: Math.max(0, B - T),
      fx: r.left, fy: r.top, fw: r.width, fh: r.height
    };
  }

  // clockRunning: a surface is slaved to a PLAYING element, so the clock has to keep flowing. The
  // reporter is otherwise purely event-driven (mutation/resize/scroll), and a playing video mutates
  // nothing - P3's finding #1 in its other form: a clock sampled only when the DOM changes freezes
  // the picture just as surely as a rect-driven present did.
  var clockRunning = false;

  function report() {
    var dpr = window.devicePixelRatio || 1, list = [], dropped = 0, content = 0;
    clockRunning = false;
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!el.isConnected) continue;
      if (list.length >= CAP) { dropped++; continue; } // drop-NEWEST: DOM order = oldest first
      var r = clip(el), cs = getComputedStyle(el), io = seen.get(el);
      if (el.id !== '__sfbg') content++;
      // Presentation clock (P4): the element named by data-surface-clock is the master, and the
      // child presents the frame whose PTS matches it. currentTime changes every frame while it
      // plays, so the identical-consecutive drop below turns into "report at rAF while playing,
      // never while paused" - which is exactly the cadence the present pump wants.
      var ck = el.getAttribute('data-surface-clock'), cel = ck ? document.getElementById(ck) : null;
      if (cel && !cel.paused) clockRunning = true;
      list.push({
        id: el.getAttribute('data-surface') || '',
        // Device px: the DComp visual tree and put_Bounds both live in raw window-client pixels.
        x: Math.round(r.x * dpr), y: Math.round(r.y * dpr),
        w: Math.round(r.w * dpr), h: Math.round(r.h * dpr),
        // Full (unclipped) rect: may be negative or past the viewport, which is the point.
        fx: Math.round(r.fx * dpr), fy: Math.round(r.fy * dpr),
        fw: Math.round(r.fw * dpr), fh: Math.round(r.fh * dpr),
        vis: r.w > 0.5 && r.h > 0.5 && cs.visibility !== 'hidden' && cs.display !== 'none' && io !== false,
        dpr: dpr,
        c: el.getAttribute('data-surface-color') || '',
        clk: cel ? cel.currentTime : -1,
        clkp: cel ? !!cel.paused : false
      });
    }
    canvas(content > 0);
    var s = JSON.stringify(list) + '|' + dropped;
    if (s === last) return;
    last = s;
    try { window.__ravesurf(list, dropped); } catch (e) { }
  }

  function rescan() {
    els = Array.prototype.slice.call(document.querySelectorAll('[data-surface]'));
    if (ro) { ro.disconnect(); for (var i = 0; i < els.length; i++) ro.observe(els[i]); }
    if (iob) { iob.disconnect(); for (var j = 0; j < els.length; j++) iob.observe(els[j]); }
  }

  function frame() {
    raf = 0;
    if (needScan) { needScan = false; rescan(); }
    report();
    // Keep the clock flowing while something plays - but on a TIMER, not rAF. A 60 Hz report stream
    // starves WM_TIMER (a low-priority message), and the present pump lives on that timer: measured,
    // the surface presented 12 fps of a 30 fps producer until this was throttled. The child
    // interpolates between reports, so 10 Hz is plenty; it stops the frame a surface pauses, which
    // is what freezes the picture with the playhead.
    if (clockRunning) setTimeout(function () { schedule(false); }, 100);
  }
  function schedule(withScan) {
    if (withScan) needScan = true;
    if (!raf) raf = requestAnimationFrame(frame);
  }

  var ro = window.ResizeObserver ? new ResizeObserver(function () { schedule(false); }) : null;
  var iob = window.IntersectionObserver ? new IntersectionObserver(function (es) {
    for (var i = 0; i < es.length; i++) seen.set(es[i].target, es[i].isIntersecting);
    schedule(false);
  }, { threshold: [0, 0.01] }) : null;

  // Same hook shape as __mstScan: every Go DOM update flows through __patch, so wrapping it is how
  // a patched-in hole opens and a patched-out one closes. Observe the DOCUMENT node - this runs at
  // document-start, documentElement is still null.
  var patch = window.__patch;
  if (typeof patch === 'function') {
    window.__patch = function (id, html) {
      patch(id, html);
      if (els.length || ('' + html).indexOf('data-surface') >= 0) schedule(true);
    };
  }
  new MutationObserver(function () { schedule(true); }).observe(document, { childList: true, subtree: true });
  window.addEventListener('resize', function () { schedule(false); });
  document.addEventListener('scroll', function () { schedule(false); }, true);
  document.addEventListener('DOMContentLoaded', function () { schedule(true); });
  window.addEventListener('load', function () { schedule(true); });
  schedule(true);

  // ── canvas transparency ──────────────────────────────────────────────────────────────────
  // P2 finding #2, now automatic: put_DefaultBackgroundColor(A=0) makes the WEBVIEW transparent,
  // but body's own background still paints over every hole. So the moment a content surface exists
  // the app's background has to move OFF the canvas - html/body go transparent and a full-viewport
  // [data-surface] carries body's colour instead. Reverted when the last content surface goes, so a
  // build with no surfaces (and every screenshot of one) is byte-identical to before.
  function hex(c) {
    var m = /rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(c || '');
    if (!m) return '0a0a0a';
    return ((+m[1] << 16) | (+m[2] << 8) | +m[3]).toString(16).padStart(6, '0');
  }
  var bgcol = '';
  function canvas(on) {
    var bg = document.getElementById('__sfbg'), st = document.getElementById('__sfstyle');
    if (!on) {
      if (bg) bg.remove();
      if (st) st.remove();
      return;
    }
    if (!document.body) return;
    // Sample body's colour BEFORE the override lands and remember it - reading the already
    // transparent canvas later would repaint the app in the fallback grey.
    if (!bgcol) bgcol = hex(getComputedStyle(document.body).backgroundColor);
    if (!st) {
      st = document.createElement('style');
      st.id = '__sfstyle';
      st.textContent = 'html,body{background:transparent !important;background-image:none !important}';
      document.head.appendChild(st);
    }
    if (!bg) {
      bg = document.createElement('div');
      bg.id = '__sfbg';
      bg.setAttribute('data-surface', '__sfbg');
      bg.setAttribute('data-surface-color', bgcol);
      bg.style.cssText = 'position:fixed;inset:0;z-index:-1;background:transparent;pointer-events:none';
      document.body.insertBefore(bg, document.body.firstChild);
    }
  }

  // ── ctl surface-test (P2 verification only) ──────────────────────────────────────────────
  // Injects a hole into the live page from the CHILD, so the surface path can be exercised with no
  // Go render change at all. The canvas arrangement it needs is the shipped one above, driven by
  // the hole simply existing.
  window.__sfTest = function (on) {
    var hole = document.getElementById('__sfhole');
    if (!on) {
      if (hole) hole.remove();
      schedule(true);
      return;
    }
    if (hole) return;
    var main = document.getElementById('main') || document.body;
    hole = document.createElement('div');
    hole.id = '__sfhole';
    hole.setAttribute('data-surface', 'test-hole');
    // 62vh, not a token height: the hole has to be big enough that a modal and a smart-select
    // panel actually LAND on it - "draws on top" is unprovable when nothing overlaps.
    hole.style.cssText = 'height:62vh;min-height:200px;margin:0 0 16px;border-radius:12px;background:transparent;' +
      'border:1px dashed rgba(255,255,255,.45);display:flex;align-items:center;justify-content:center;' +
      'font:12px ui-monospace,Consolas,monospace;color:#fff;text-shadow:0 1px 3px rgba(0,0,0,.9)';
    // Text drawn by the PAGE over the native colour: the z-order proof needs no extra scaffolding.
    hole.textContent = 'surface-test: native visual below this text';
    main.insertBefore(hole, main.firstChild);
    schedule(true);
  };
})();
