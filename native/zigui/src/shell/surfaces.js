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

  // clip intersects the element rect with every scrolling ancestor and the viewport. The reported
  // rect is what is actually VISIBLE, which lets the child size content to it and skip
  // IDCompositionVisual::SetClip (see surfaces.zig resize()).
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
    return { x: L, y: T, w: Math.max(0, R - L), h: Math.max(0, B - T) };
  }

  function report() {
    var dpr = window.devicePixelRatio || 1, list = [], dropped = 0;
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!el.isConnected) continue;
      if (list.length >= CAP) { dropped++; continue; } // drop-NEWEST: DOM order = oldest first
      var r = clip(el), cs = getComputedStyle(el), io = seen.get(el);
      list.push({
        id: el.getAttribute('data-surface') || '',
        // Device px: the DComp visual tree and put_Bounds both live in raw window-client pixels.
        x: Math.round(r.x * dpr), y: Math.round(r.y * dpr),
        w: Math.round(r.w * dpr), h: Math.round(r.h * dpr),
        vis: r.w > 0.5 && r.h > 0.5 && cs.visibility !== 'hidden' && cs.display !== 'none' && io !== false,
        dpr: dpr,
        c: el.getAttribute('data-surface-color') || ''
      });
    }
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

  // ── ctl surface-test (P2 verification only) ──────────────────────────────────────────────
  // Injects a hole into the live page from the CHILD, so the surface path can be exercised with no
  // Go render change at all. The page paints an opaque body background, so the hole also needs the
  // canvas transparent - and then the app's own background has to come from somewhere: a full-
  // viewport surface carrying body's colour, which is exactly the arrangement a shipped surface
  // build would use anyway (webview transparent, chrome composited).
  function hex(c) {
    var m = /rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(c || '');
    if (!m) return '0a0a0a';
    return ((+m[1] << 16) | (+m[2] << 8) | +m[3]).toString(16).padStart(6, '0');
  }
  var bgcol = '';
  window.__sfTest = function (on) {
    var bg = document.getElementById('__sfbg'), hole = document.getElementById('__sfhole'),
      st = document.getElementById('__sfstyle');
    if (!on) {
      if (bg) bg.remove();
      if (hole) hole.remove();
      if (st) st.remove();
      schedule(true);
      return;
    }
    if (hole) return;
    // Sample body's colour BEFORE the override lands, and remember it - a second run reads the
    // already-transparent canvas and would repaint the app in the fallback grey.
    if (!bgcol) bgcol = hex(getComputedStyle(document.body).backgroundColor);
    // Create-if-missing, not create-always: a tab switch patches #main and takes the hole with it
    // (that IS the close path), while #__sfbg is a body child and survives. Turning the test back
    // on must not leave a second one behind.
    if (!st) {
      st = document.createElement('style');
      st.id = '__sfstyle';
      st.textContent = 'html,body{background:transparent !important;background-image:none !important}';
      document.head.appendChild(st);
    }
    if (!bg) {
      // The canvas is transparent while the test runs, so body's own colour has to come from a
      // native surface - which is also how a shipped surface build would paint app chrome.
      bg = document.createElement('div');
      bg.id = '__sfbg';
      bg.setAttribute('data-surface', '__sfbg');
      bg.setAttribute('data-surface-color', bgcol);
      bg.style.cssText = 'position:fixed;inset:0;z-index:-1;background:transparent;pointer-events:none';
      document.body.insertBefore(bg, document.body.firstChild);
    }
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
