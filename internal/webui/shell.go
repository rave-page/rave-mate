package webui

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// webviewAllowGPU mirrors config UIFeature.AllowWebviewGPU(), set once in New before the shell is
// constructed. Default false = WebView2 GPU compositing OFF so rave-mate never contends with a live
// GPU encoder (OBS/NVENC). Read by the cgo shell when creating the WebView2 environment.
var webviewAllowGPU bool

// shell is the native webview window host. The cgo build provides a real WebView2/WebKit window;
// the nocgo build returns unavailable so the daemon falls back to Fyne. Go owns all rendering -
// the shell only loads a document, patches the DOM (eval), and forwards page events (onAction).
type shell interface {
	run(initialHTML string, startHidden bool) // create the window + block on its message loop
	setHTML(html string)                      // load a full document (thread-safe)
	eval(js string)                           // run JS on the page (thread-safe)
	resize(w, h int)                          // resize the window viewport
	show()                                    // bring the window to the foreground
	terminate()                               // request close (+ force-exit watchdog)
	hwnd() uintptr                            // native window handle (0 if none) for OS screenshots
	post(payload string) bool                 // enqueue an act payload on the serial act worker (Go-originated input)
}

// ── ctl eval round-trip ──
// webview Eval is fire-and-forget; to read a value back the page calls the bound __rave_evalResult
// with a request id. evalWaiters routes that result to the blocked caller (mirror of rave-app).

var (
	evalSeq     uint64
	evalWaiters sync.Map // string id -> chan string
)

func nextEvalID() string { return fmt.Sprintf("e%d", atomic.AddUint64(&evalSeq, 1)) }

// deliverEval is called (from the webview binding) with a completed eval result.
func deliverEval(id, result string) {
	if ch, ok := evalWaiters.Load(id); ok {
		select {
		case ch.(chan string) <- result:
		default:
		}
	}
}

// runtimeJS is injected ONCE via webview Init. It is pure transport + dev introspection - NO
// business logic and NO per-view code. The entire UI is rendered in Go; this file only:
//   - forwards clicks/submits on [data-act] elements to the bound Go `rave(...)` function,
//   - exposes __patch(id,html) so Go can mutate the DOM like JS would,
//   - exposes __snapshot/__click/__read/__set/__type/__tap for the ctl control plane,
//   - shows/positions the shared tooltip/chip-card primitives (__ttshow/__ttplace -
//     view-agnostic; portals .tt cards to a body-level layer + needs live viewport
//     measurement CSS can't do).
const runtimeJS = `(function(){
  function send(p){ try{ if(window.rave) window.rave(JSON.stringify(p)); }catch(e){} }
  function mods(e){ return (e.shiftKey?'s':'')+((e.ctrlKey||e.metaKey)?'c':''); }
  // change events carry no modifier state - remember the last pointerdown's (checkbox flows)
  var lastMods='';
  document.addEventListener('pointerdown', function(e){ lastMods=mods(e); }, true);
  document.addEventListener('click', function(e){
    var t = e.target;
    if(t && (t.tagName==='INPUT'||t.tagName==='SELECT'||t.tagName==='TEXTAREA')) return; // 'change' handles form controls
    var el = t.closest && t.closest('[data-act]');
    if(!el) return;
    // a submit button (no own data-act) inside a form[data-act]: let the native submit fire -
    // preventDefault here would kill it and send the form's act with NO form payload.
    var b = t.closest && t.closest('button');
    if(el.tagName==='FORM' && b && !b.getAttribute('data-act')) return;
    e.preventDefault();
    send({act: el.getAttribute('data-act'), val: el.getAttribute('data-val')||'', id: el.id||'', mods: mods(e)});
  });
  document.addEventListener('change', function(e){
    var el = e.target;
    if(!el || !el.getAttribute || !el.getAttribute('data-act')) return;
    var v = el.type==='checkbox' ? String(el.checked) : (el.value||'');
    send({act: el.getAttribute('data-act'), val: v, id: el.id||'', mods: el.type==='checkbox'?lastMods:''});
  });
  // shift+click on a list row must never smear a text selection over the range
  document.addEventListener('selectstart', function(e){
    if(lastMods.indexOf('s')>=0 && e.target.closest && e.target.closest('.trk-table')) e.preventDefault();
  });
  // draggable pane splitters: [data-splitvar] handles write a :root CSS var (the grid
  // column width), persisted in localStorage - layouts survive re-renders + restarts.
  (function(){
    function apply(k,v){ document.documentElement.style.setProperty('--'+k, v+'px'); }
    try{ var st=JSON.parse(localStorage.getItem('rp-splits')||'{}'); for(var k in st){ apply(k,st[k]); } }catch(e){}
    var d=null;
    document.addEventListener('pointerdown', function(e){
      var h=e.target.closest && e.target.closest('[data-splitvar]');
      if(!h || e.button!==0) return;
      e.preventDefault();
      var v=h.getAttribute('data-splitvar');
      var cur=parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--'+v));
      if(!cur){ cur=parseFloat(h.getAttribute('data-splitdef'))||240; }
      d={v:v, x:e.clientX, w:cur, r:h.hasAttribute('data-splitdir')};
      h.classList.add('on');
      if(h.setPointerCapture) h.setPointerCapture(e.pointerId);
    }, true);
    document.addEventListener('pointermove', function(e){
      if(!d) return;
      var dx=e.clientX-d.x; if(d.r) dx=-dx;
      apply(d.v, Math.max(150, Math.min(640, d.w+dx)));
    }, true);
    document.addEventListener('pointerup', function(){
      if(!d) return;
      var el=document.querySelector('[data-splitvar="'+d.v+'"]'); if(el) el.classList.remove('on');
      try{ var st=JSON.parse(localStorage.getItem('rp-splits')||'{}');
        st[d.v]=parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--'+d.v))||d.w;
        localStorage.setItem('rp-splits', JSON.stringify(st)); }catch(e){}
      d=null;
    }, true);
  })();
  // browser-style history: mouse X1/X2 (back/forward, e.button 3/4) + Alt+←/→ → Go nav stack.
  // preventDefault on down/aux so WebView2's own (empty) history navigation never fires.
  document.addEventListener('mousedown', function(e){ if(e.button===3||e.button===4) e.preventDefault(); });
  document.addEventListener('auxclick', function(e){ if(e.button===3||e.button===4) e.preventDefault(); });
  document.addEventListener('mouseup', function(e){
    if(e.button===3){ e.preventDefault(); send({act:'nav-back'}); }
    else if(e.button===4){ e.preventDefault(); send({act:'nav-fwd'}); }
  });
  document.addEventListener('keydown', function(e){
    if(!e.altKey || e.ctrlKey || e.metaKey) return;
    if(e.key==='ArrowLeft'){ e.preventDefault(); send({act:'nav-back'}); }
    else if(e.key==='ArrowRight'){ e.preventDefault(); send({act:'nav-fwd'}); }
  });
  // scoped editing keys (library list nav + cue editor). Guards: window focused, a
  // key-scope stamped on <body> by Go for the current view, no editable element focused.
  document.addEventListener('keydown', function(e){
    if(e.metaKey||e.altKey) return;
    if(!document.hasFocus()) return;
    var scope=document.body.getAttribute('data-keyscope')||''; if(!scope) return;
    var a=document.activeElement;
    if(a&&a.matches&&a.matches('input,textarea,select,[contenteditable]')) return;
    var map={'ArrowUp':'up','ArrowDown':'down','ArrowLeft':'left','ArrowRight':'right','Enter':'enter','t':'t','T':'t',' ':'space','Delete':'del','Backspace':'del','z':'z','Z':'z'};
    var name=map[e.key]; if(!name) return;
    if(e.ctrlKey && name!=='left' && name!=='right' && name!=='z') return; // Ctrl: grid nudge + undo only
    if(name==='z' && !e.ctrlKey) return; // plain z is not bound
    // hold = one action: auto-repeat on one-shot keys hammered full-file tag rewrites (enter/t)
    if(e.repeat&&(name==='space'||name==='enter'||name==='t')){ e.preventDefault(); return; }
    e.preventDefault();
    send({act:'key:'+scope, val:(e.ctrlKey?'c':'')+(e.shiftKey?'s':'')+name});
  });
  document.addEventListener('keyup', function(e){
    if(e.key!==' ') return;
    var scope=document.body.getAttribute('data-keyscope')||''; if(!scope) return;
    var a=document.activeElement;
    if(a&&a.matches&&a.matches('input,textarea,select,[contenteditable]')) return;
    e.preventDefault();
    send({act:'key:'+scope, val:'spaceup'});
  });
  document.addEventListener('input', function(e){
    var el = e.target;
    if(!el || !el.getAttribute) return;
    var a = el.getAttribute('data-actinput'); if(!a) return;
    send({act: a, val: el.value||'', id: el.id||''});
  });
  document.addEventListener('submit', function(e){
    var f = e.target.closest && e.target.closest('form[data-act]'); if(!f) return; e.preventDefault();
    var d={}; new FormData(f).forEach(function(v,k){ d[k]=v; });
    send({act: f.getAttribute('data-act'), form: JSON.stringify(d), id: f.id||''});
  });
  window.__patch = function(id, html){ var n=document.getElementById(id); if(n){ n.innerHTML = html;
    if(html.indexOf('ss-panel')>=0) __ssplace(); } };
  window.__toast = function(msg){
    var t=document.getElementById('__toasts');
    if(!t){ t=document.createElement('div'); t.id='__toasts';
      t.style.cssText='position:fixed;right:16px;bottom:16px;display:flex;flex-direction:column;gap:8px;z-index:9999'; document.body.appendChild(t); }
    var e=document.createElement('div'); e.textContent=msg;
    e.style.cssText='background:rgba(20,10,15,.96);border:1px solid rgba(255,255,255,.12);border-radius:12px;padding:10px 14px;font:13px "JetBrains Mono",ui-monospace,Consolas,monospace;color:#fafafa;box-shadow:0 10px 30px -10px rgba(0,0,0,.8)';
    t.appendChild(e); setTimeout(function(){ e.remove(); }, 4200);
  };
  function vis(el){ var r=el.getBoundingClientRect(); return r.width>0 && r.height>0; }
  window.__snapshot = function(){
    var out=[];
    function walk(el, depth){
      if(depth>40) return;
      for(var i=0;i<el.children.length;i++){
        var c=el.children[i];
        if(c.id==='__toasts') continue;
        // #__modal / #__ttlayer wrappers have zero rect (children are position:fixed) -
        // never prune them, or open dialogs/pinned tooltips vanish from ctl snapshot.
        if(!vis(c) && c.id!=='__modal' && c.id!=='__ttlayer') continue;
        var tag=c.tagName.toLowerCase();
        // same-origin iframe (remote-library mirror): splice its own snapshot in, indented,
        // so ctl sees the mirrored peer UI as part of this page.
        if(tag==='iframe'){
          try{ var w=c.contentWindow;
            if(w && w.__snapshot){
              var pad=new Array(depth+1).join('  ');
              out.push(pad+'iframe [mirror]');
              var sub=w.__snapshot().split('\n');
              for(var k=0;k<sub.length;k++){ if(sub[k]) out.push(pad+'  '+sub[k]); }
            }
          }catch(e){}
          continue;
        }
        var role=c.getAttribute('data-role')||c.getAttribute('role')||'';
        var own='';
        for(var j=0;j<c.childNodes.length;j++){ if(c.childNodes[j].nodeType===3){ own+=c.childNodes[j].textContent; } }
        own=own.replace(/\s+/g,' ').trim();
        var over=(c.scrollWidth>c.clientWidth+2)||(c.scrollHeight>c.clientHeight+2);
        var act=c.hasAttribute('data-act');
        var interactive=tag==='button'||tag==='a'||tag==='input'||tag==='select'||tag==='textarea'||act;
        var kept=own||role||interactive;
        if(kept){
          var line=new Array(depth+1).join('  ')+tag;
          if(role) line+=' ['+role+']';
          if(act) line+=' {'+c.getAttribute('data-act')+(c.getAttribute('data-val')?('='+c.getAttribute('data-val')):'')+'}';
          if(own) line+=' "'+own.slice(0,80)+'"';
          if(over && tag!=='body' && tag!=='main') line+='  ⚠OVERFLOW';
          out.push(line);
        }
        walk(c, depth+(kept?1:0));
      }
    }
    walk(document.body,0);
    return out.join('\n');
  };
  window.__click = function(q){
    q=(q||'').toLowerCase();
    // two passes: real controls first, then [data-act] containers - a form[data-act]'s
    // textContent contains its submit button's label and precedes it in DOM order, so a
    // single pass would "click" the form (a no-op) instead of the button.
    // act mode: query matches data-act (the exact snapshot {act} token) - deterministic
    // where labels are ambiguous ("Playlist" facet vs "Playlists" tab). ':'/'=' queries
    // are act-only; plain queries match labels first, then fall back to acts.
    var actOnly=q.indexOf(':')>=0||q.indexOf('=')>=0;
    function fire(el){
      if(el.tagName==='FORM'){
        if(el.requestSubmit) el.requestSubmit();
        else el.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));
      } else el.click();
      return true;
    }
    // act token = data-act, or data-act=data-val (the exact snapshot form). Exact match
    // wins over substring so "...=50" can't land on "...=509".
    function scanAct(sel,exact){
      var els=document.querySelectorAll(sel);
      for(var i=0;i<els.length;i++){
        var a=(els[i].getAttribute&&els[i].getAttribute('data-act')||'').toLowerCase();
        if(!a) continue;
        var v=(els[i].getAttribute('data-val')||'').toLowerCase();
        var tok=v?(a+'='+v):a;
        var hit=exact?(tok===q||a===q):(tok.indexOf(q)>=0);
        if(hit && vis(els[i])) return fire(els[i]);
      }
      return false;
    }
    function scanText(sel){
      var els=document.querySelectorAll(sel);
      for(var i=0;i<els.length;i++){
        var t=(els[i].textContent||els[i].value||els[i].getAttribute('aria-label')||'').toLowerCase().replace(/\s+/g,' ').trim();
        if(t.indexOf(q)>=0 && vis(els[i])) return fire(els[i]);
      }
      return false;
    }
    // last resort: forward into same-origin iframes (remote-library mirror runs its own __click)
    function scanFrames(){
      var fs=document.querySelectorAll('iframe');
      for(var i=0;i<fs.length;i++){ try{ var w=fs[i].contentWindow; if(w&&w.__click&&w.__click(q)) return true; }catch(e){} }
      return false;
    }
    if(actOnly) return scanAct('button,a,input[type=checkbox],[data-act]',true) || scanAct('[data-act]',false) || scanFrames();
    return scanText('button,a,input[type=checkbox]') || scanText('[data-act]') || scanAct('[data-act]',true) || scanAct('[data-act]',false) || scanFrames();
  };
  // forward a ctl primitive into same-origin iframes when the main document misses
  function __frameCall(name,args,miss){
    var fs=document.querySelectorAll('iframe');
    for(var i=0;i<fs.length;i++){
      try{ var w=fs[i].contentWindow;
        if(w && w[name]){ var r=w[name].apply(null,args); if(r!==miss && r!==null && r!==false) return r; }
      }catch(e){}
    }
    return miss;
  }
  window.__read = function(q){
    q=(q||'').toLowerCase();
    var els=document.querySelectorAll('[data-label]');
    for(var i=0;i<els.length;i++){
      if((els[i].getAttribute('data-label')||'').toLowerCase().indexOf(q)>=0){
        return els[i].getAttribute('data-value') || els[i].textContent.replace(/\s+/g,' ').trim();
      }
    }
    return __frameCall('__read',[q],null);
  };
  window.__set = function(q,val){
    q=(q||'').toLowerCase();
    var els=document.querySelectorAll('[data-label]');
    for(var i=0;i<els.length;i++){
      if((els[i].getAttribute('data-label')||'').toLowerCase().indexOf(q)<0) continue;
      var f=els[i].matches('input,select,textarea')?els[i]:els[i].querySelector('input,select,textarea');
      if(!f) return false;
      if(f.type==='checkbox'){ f.checked=(val==='true'||val==='1'||val==='on'); }
      else { f.value=val; }
      f.dispatchEvent(new Event('input',{bubbles:true})); f.dispatchEvent(new Event('change',{bubbles:true}));
      return true;
    }
    return __frameCall('__set',[q,val],false);
  };
  window.__type = function(text){
    var f=document.activeElement;
    if(f && f.tagName==='IFRAME'){ // focus sits inside the mirror - type there
      try{ var w=f.contentWindow; if(w&&w.__type) return w.__type(text); }catch(e){}
      return false;
    }
    if(!f || !f.matches || !f.matches('input,textarea')) return false;
    f.value=(f.value||'')+text;
    f.dispatchEvent(new Event('input',{bubbles:true}));
    f.dispatchEvent(new Event('change',{bubbles:true})); // change-wired fields (search) apply too
    return true;
  };
  window.__tap = function(x,y){ var el=document.elementFromPoint(x,y);
    if(el && el.tagName==='IFRAME'){ // translate into the mirror's coordinate space
      try{ var w=el.contentWindow; if(w&&w.__tap){ var fr=el.getBoundingClientRect(); return w.__tap(x-fr.left,y-fr.top); } }catch(e){}
      return false;
    }
    if(el){
    el.click();
    // synthetic click() never focuses - focus editable targets so a following ctl type works
    if(el.matches && el.matches('input,textarea')) el.focus();
    return true; } return false; };
  // right-click context menu: an element with [data-ctx] forwards its act on contextmenu (Go opens
  // the menu modal). __ctx(x,y) is the ctl equivalent (TapSecondary) for verification.
  document.addEventListener('contextmenu', function(e){
    if(e.target.closest && e.target.closest('[data-actpos]')){ e.preventDefault(); return; } // right-click is a marker action there
    var el=e.target.closest && e.target.closest('[data-ctx]'); if(!el) return;
    e.preventDefault();
    send({act: el.getAttribute('data-ctx')});
  });
  window.__ctx = function(x,y){
    var el=document.elementFromPoint(x,y);
    el = el && el.closest ? el.closest('[data-ctx]') : null;
    if(el){ send({act: el.getAttribute('data-ctx')}); return true; }
    return false;
  };
  // pointer-position transport: [data-actpos] forwards down/move/up with fractional in-element
  // coords ("down:0.4321,0.5000"); [data-actwheel] forwards wheel steps ("in:fx,fy"/"out:fx,fy").
  // Pure transport (rAF-throttled) - Go interprets the fractions (trim handles, waveform pan/zoom).
  var __pcur=null, __ppend=null, __praf=0;
  function __pfrac(el,e){ var r=el.getBoundingClientRect();
    var x=(e.clientX-r.left)/Math.max(1,r.width), y=(e.clientY-r.top)/Math.max(1,r.height);
    return Math.min(1,Math.max(0,x)).toFixed(4)+','+Math.min(1,Math.max(0,y)).toFixed(4); }
  function __pflush(){ __praf=0; if(__ppend){ send(__ppend); __ppend=null; } }
  document.addEventListener('pointerdown', function(e){
    var el=e.target.closest && e.target.closest('[data-actpos]'); if(!el) return;
    if(e.button===2){ // right button: modifier-tagged one-shot, no drag capture
      e.preventDefault();
      var ph=e.ctrlKey?'crdown':(e.shiftKey?'srdown':'rdown');
      send({act: el.getAttribute('data-actpos'), val: ph+':'+__pfrac(el,e)});
      return;
    }
    __pcur=el; try{ el.setPointerCapture(e.pointerId); }catch(_){}
    e.preventDefault();
    // mods ride as a 3rd CSV field ("down:fx,fy,cs") - mpPos ignores it, ceSurf reads it
    send({act: el.getAttribute('data-actpos'), val: 'down:'+__pfrac(el,e)+','+mods(e)});
  });
  document.addEventListener('pointermove', function(e){
    if(!__pcur) return;
    __ppend={act: __pcur.getAttribute('data-actpos'), val: 'move:'+__pfrac(__pcur,e)};
    if(!__praf) __praf=requestAnimationFrame(__pflush);
  });
  document.addEventListener('pointerup', function(e){
    if(!__pcur) return; var el=__pcur; __pcur=null; __ppend=null;
    send({act: el.getAttribute('data-actpos'), val: 'up:'+__pfrac(el,e)});
  });
  document.addEventListener('wheel', function(e){
    var el=e.target.closest && e.target.closest('[data-actwheel]'); if(!el) return;
    e.preventDefault();
    send({act: el.getAttribute('data-actwheel'), val: (e.deltaY>0?'out:':'in:')+__pfrac(el,e)});
  }, {passive:false});
  // hover transport: [data-acthover] forwards throttled buttonless pointer positions
  // ("at:fx,fy") + "off" on leave. Pure transport - Go renders the hover readout.
  var __hlast=0;
  document.addEventListener('pointermove', function(e){
    if(e.buttons) return;
    var el=e.target.closest && e.target.closest('[data-acthover]'); if(!el) return;
    var now=Date.now(); if(now-__hlast<80) return; __hlast=now;
    send({act: el.getAttribute('data-acthover'), val: 'at:'+__pfrac(el,e)});
  }, true);
  document.addEventListener('pointerout', function(e){
    var el=e.target.closest && e.target.closest('[data-acthover]'); if(!el) return;
    var to=e.relatedTarget; if(to && el.contains(to)) return;
    send({act: el.getAttribute('data-acthover'), val: 'off'});
  }, true);
  // ── tooltip (.tt, tooltip.go) + waveform-chip (.wchip, library.css) card layer ──
  // .tt cards PORTAL: while shown they re-parent into one fixed body-level layer
  // (#__ttlayer, above modals) so no ancestor overflow container or stacking context
  // can clip or cover them - same fix as the web SmartSelect body-portal. JS owns
  // .tt show/hide (CSS sibling selectors can't reach a portaled card); a hidden card
  // returns to its trigger so renders/ctl stay consistent. .wchip cards stay absolute
  // in place (offset-parent-relative; CSS owns show + top/flip), JS only clamps left.
  var __ttlayer=null;
  function __ttL(){
    if(!__ttlayer || !document.body.contains(__ttlayer)){
      __ttlayer=document.createElement('div'); __ttlayer.id='__ttlayer';
      document.body.appendChild(__ttlayer);
    }
    return __ttlayer;
  }
  function __ttcardOf(host){ return host.__ttcard || host.querySelector('.tt-card,.wchip-card'); }
  function __ttpin(host){ var x=host.querySelector('.tt-x,.wchip-x'); return !!(x&&x.checked); }
  function __tthostOf(n){ // trigger for an event target: the .tt itself or its portaled card
    if(!n || !n.closest) return null;
    var h=n.closest('.tt'); if(h) return h;
    var c=n.closest('.tt-card'); return c ? (c.__tthost||null) : null;
  }
  function __ttinside(host, n){ // n within the trigger or its (portaled) card
    return !!(n && (host.contains(n) || (host.__ttcard && host.__ttcard.contains(n))));
  }
  function __ttshow(host){
    var card=__ttcardOf(host); if(!card) return;
    if(card.classList.contains('tt-card')){
      if(card.parentNode!==__ttL()){ host.__ttcard=card; card.__tthost=host; __ttL().appendChild(card); }
      card.classList.add('tt-open');
    }
    __ttplace(host);
  }
  function __tthide(host){
    var card=host.__ttcard;
    if(card){
      card.classList.remove('tt-open','ttp-up');
      card.style.left=''; card.style.top='';
      card.__tthost=null; host.__ttcard=null;
      host.appendChild(card); // un-portal: card travels with future host re-renders
    }
    host.classList.remove('ttp-up');
  }
  function __ttplace(host, retried){
    var card=__ttcardOf(host); if(!card) return;
    var inner=card.querySelector('.tt-in')||card;
    card.style.left=''; card.style.top=''; card.style.right=''; card.style.bottom='';
    inner.style.maxHeight='';
    host.classList.remove('ttp-up'); card.classList.remove('ttp-up');
    var r=card.getBoundingClientRect();
    if(!r.width && !r.height){ // not (yet) shown - hover state can lag the event
      if(!retried) requestAnimationFrame(function(){ __ttplace(host, 1); });
      return;
    }
    var M=8, vw=window.innerWidth, vh=window.innerHeight;
    var a=host.getBoundingClientRect(); // the ⓘ trigger, viewport coords
    var fixed=(getComputedStyle(card).position==='fixed');
    // vertical: below by default, flip above when it clips and above has more room
    var below=vh-M-a.bottom, above=a.top-M;
    var up=(r.height>below && above>below);
    if(up){ host.classList.add('ttp-up'); card.classList.add('ttp-up'); }
    // cap height to the chosen side's room AND never taller than the window (tall cards
    // scroll internally via .tt-in) - the vh-2M bound also guards a trigger scrolled off-screen
    var avail=Math.min(vh-2*M, Math.max(60, up?above:below));
    if(r.height>avail) inner.style.maxHeight=(avail-8)+'px'; // 8 = bridge/gap
    r=card.getBoundingClientRect(); // re-measure after flip class + height cap
    var w=Math.min(r.width, vw-2*M);
    if(fixed){ // viewport-positioned: clamp fully into the window from the trigger rect
      var left=Math.min(Math.max(a.left, M), vw-M-w);
      var top=up ? a.top-r.height : a.bottom;
      top=Math.min(Math.max(top, M), Math.max(M, vh-M-r.height)); // keep the whole card in [M, vh-M]
      card.style.left=left+'px'; card.style.top=top+'px'; card.style.right='auto';
    } else { // absolute (.wchip-card): shift left within the offset parent to stay in-viewport
      var nl=Math.min(Math.max(r.left, M), vw-M-w);
      if(Math.abs(nl-r.left)>0.5){ card.style.left=(nl-a.left)+'px'; card.style.right='auto'; }
    }
  }
  document.addEventListener('pointerover', function(e){
    var el=e.target.closest && e.target.closest('.tt,.wchip'); if(!el) return;
    var from=e.relatedTarget; if(from && el.contains(from)) return; // already inside
    __ttshow(el);
  }, true);
  document.addEventListener('pointerout', function(e){ // close unpinned .tt on true leave
    var host=__tthostOf(e.target); if(!host) return;
    if(__ttinside(host, e.relatedTarget)) return; // trigger↔card move, not a leave
    if(!__ttpin(host) && !host.matches(':focus-within')) __tthide(host);
  }, true);
  document.addEventListener('focusin', function(e){
    var el=e.target.closest && e.target.closest('.tt,.wchip'); if(el) __ttshow(el);
  }, true);
  document.addEventListener('focusout', function(e){
    var host=__tthostOf(e.target); if(!host) return;
    if(__ttinside(host, e.relatedTarget)) return;
    if(!__ttpin(host) && !host.matches(':hover') &&
       !(host.__ttcard && host.__ttcard.matches(':hover'))) __tthide(host);
  }, true);
  document.addEventListener('change', function(e){ // checkbox pin, incl. ctl __set
    var x=e.target;
    if(!x || !x.matches) return;
    if(x.matches('.tt-x')){
      var el=x.closest('.tt'); if(!el) return;
      if(x.checked){ __ttshow(el); }
      else if(!el.matches(':hover') && !(el.__ttcard && el.__ttcard.matches(':hover'))){ __tthide(el); }
    } else if(x.matches('.wchip-x') && x.checked){
      var el2=x.closest('.wchip'); if(el2) __ttplace(el2);
    }
  }, true);
  var __ttraf=0;
  function __ttrepin(){ // re-place shown cards (a fixed card must track its trigger on scroll/resize)
    if(__ttraf) return;
    __ttraf=requestAnimationFrame(function(){ __ttraf=0;
      var L=__ttlayer;
      if(L) for(var i=L.children.length-1;i>=0;i--){
        var card=L.children[i], host=card.__tthost;
        if(host && document.contains(host)){ __ttplace(host); continue; }
        // trigger re-rendered under an open card (live tick / __patch): re-pin the
        // replacement trigger (same data-label) if pinned, else close - never orphan.
        var lbl=host&&host.getAttribute ? host.getAttribute('data-label') : '';
        var pin=host ? __ttpin(host) : false;
        card.__tthost=null; if(host) host.__ttcard=null;
        card.remove();
        if(pin && lbl){
          var nh=document.querySelector('.tt[data-label="'+lbl+'"]');
          if(nh){ var nx=nh.querySelector('.tt-x'); if(nx) nx.checked=true; __ttshow(nh); }
        }
      }
      var xs=document.querySelectorAll('.wchip-x:checked,.wchip:hover,.wchip:focus-within');
      for(var j=0;j<xs.length;j++){ var el=xs[j].closest('.wchip')||xs[j]; if(el) __ttplace(el); }
    });
  }
  window.addEventListener('resize', __ttrepin);
  document.addEventListener('scroll', __ttrepin, true);
  // DOM patches (__patch / live-tick innerHTML) can detach an open card's trigger -
  // sweep the layer on any childList change outside it (rAF-debounced, no-op when empty).
  // Observe the Document node: this runs at document-start, documentElement is still null.
  new MutationObserver(function(ms){
    if(!__ttlayer || !__ttlayer.children.length) return;
    for(var i=0;i<ms.length;i++){
      var t=ms[i].target;
      if(t!==__ttlayer && !__ttlayer.contains(t)){ __ttrepin(); return; }
    }
  }).observe(document, {childList:true, subtree:true});
  // ── smartSelect panel placement (.ss-panel, smartselect.go) ──
  // Absolute panels clip against overflow ancestors (pane scroll containers). On open,
  // promote the panel to viewport-fixed anchored to its trigger + clamped into the
  // window (flip up when below lacks room). Panels under a transformed ancestor (modal
  // translate) are skipped: fixed would resolve against it, and those never clip anyway.
  var __ssraf=0;
  function __ssxf(el){ // any ancestor forming a fixed-position containing block?
    for(var n=el.parentElement; n; n=n.parentElement){
      var cs=getComputedStyle(n);
      if(cs.transform!=='none' || cs.perspective!=='none' || cs.filter!=='none') return true;
    }
    return false;
  }
  function __ssplace(){
    if(__ssraf) return;
    __ssraf=requestAnimationFrame(function(){ __ssraf=0;
      var ps=document.querySelectorAll('.ss-panel');
      for(var i=0;i<ps.length;i++){
        var p=ps[i], host=p.closest('.ss'); if(!host || __ssxf(p)) continue;
        var a=host.getBoundingClientRect(); if(!a.width && !a.height) continue;
        var M=8, vw=window.innerWidth, vh=window.innerHeight;
        var w=Math.min(Math.max(a.width, parseFloat(getComputedStyle(p).minWidth)||0), vw-2*M);
        p.style.position='fixed'; p.style.width=w+'px'; p.style.minWidth='0'; p.style.right='auto';
        var r=p.getBoundingClientRect();
        var below=vh-M-(a.bottom+4), above=a.top-4-M;
        var top=(r.height>below && above>below) ? Math.max(M, a.top-4-r.height) : a.bottom+4;
        p.style.left=Math.min(Math.max(a.left, M), vw-M-w)+'px';
        p.style.top=Math.min(top, Math.max(M, vh-M-r.height))+'px';
      }
    });
  }
  window.addEventListener('resize', __ssplace);
  document.addEventListener('scroll', __ssplace, true);
})();`
