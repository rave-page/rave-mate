package webui

import (
	"fmt"
	"sync"
	"sync/atomic"
)

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
//   - positions the shared tooltip/chip-card primitives (__ttplace - view-agnostic, needs
//     live viewport measurement CSS can't do).
const runtimeJS = `(function(){
  function send(p){ try{ if(window.rave) window.rave(JSON.stringify(p)); }catch(e){} }
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
    send({act: el.getAttribute('data-act'), val: el.getAttribute('data-val')||'', id: el.id||''});
  });
  document.addEventListener('change', function(e){
    var el = e.target;
    if(!el || !el.getAttribute || !el.getAttribute('data-act')) return;
    var v = el.type==='checkbox' ? String(el.checked) : (el.value||'');
    send({act: el.getAttribute('data-act'), val: v, id: el.id||''});
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
  window.__patch = function(id, html){ var n=document.getElementById(id); if(n){ n.innerHTML = html; } };
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
        if(!vis(c)) continue;
        var tag=c.tagName.toLowerCase();
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
    function scan(sel){
      var els=document.querySelectorAll(sel);
      for(var i=0;i<els.length;i++){
        var t=(els[i].textContent||els[i].value||els[i].getAttribute('aria-label')||'').toLowerCase().replace(/\s+/g,' ').trim();
        if(t.indexOf(q)>=0 && vis(els[i])){
          if(els[i].tagName==='FORM'){
            if(els[i].requestSubmit) els[i].requestSubmit();
            else els[i].dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));
          } else els[i].click();
          return true;
        }
      }
      return false;
    }
    return scan('button,a,input[type=checkbox]') || scan('[data-act]');
  };
  window.__read = function(q){
    q=(q||'').toLowerCase();
    var els=document.querySelectorAll('[data-label]');
    for(var i=0;i<els.length;i++){
      if((els[i].getAttribute('data-label')||'').toLowerCase().indexOf(q)>=0){
        return els[i].getAttribute('data-value') || els[i].textContent.replace(/\s+/g,' ').trim();
      }
    }
    return null;
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
    return false;
  };
  window.__type = function(text){
    var f=document.activeElement;
    if(!f || !f.matches || !f.matches('input,textarea')) return false;
    f.value=(f.value||'')+text; f.dispatchEvent(new Event('input',{bubbles:true}));
    return true;
  };
  window.__tap = function(x,y){ var el=document.elementFromPoint(x,y); if(el){ el.click(); return true; } return false; };
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
    __pcur=el; try{ el.setPointerCapture(e.pointerId); }catch(_){}
    e.preventDefault();
    send({act: el.getAttribute('data-actpos'), val: 'down:'+__pfrac(el,e)});
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
  // ── edge-aware card placement for the tooltip (.tt, tooltip.go) + waveform-chip
  // (.wchip, library.css) primitives. CSS shows the card (hover/focus/checkbox pin);
  // this measures it against the viewport, then clamps horizontally, flips above the
  // anchor when there's more room there (.ttp-up), and caps height so tall cards
  // scroll internally - content is never trimmed, only scrolled.
  function __ttplace(host, retried){
    var card=host.querySelector('.tt-card,.wchip-card'); if(!card) return;
    var inner=card.querySelector('.tt-in')||card;
    card.style.left=''; card.style.right=''; inner.style.maxHeight='';
    host.classList.remove('ttp-up');
    var r=card.getBoundingClientRect();
    if(!r.width && !r.height){ // not (yet) shown - hover state can lag the event
      if(!retried) requestAnimationFrame(function(){ __ttplace(host, 1); });
      return;
    }
    var M=8, vw=window.innerWidth, vh=window.innerHeight;
    var a=host.getBoundingClientRect();
    // vertical: below by default, flip above when it clips and above has more room
    var below=vh-M-a.bottom, above=a.top-M;
    if(r.height>below && above>below) host.classList.add('ttp-up');
    var avail=Math.max(60, host.classList.contains('ttp-up')?above:below);
    if(r.height>avail) inner.style.maxHeight=(avail-8)+'px'; // 8 = bridge/gap
    // horizontal: shift so the card sits inside [M, vw-M]
    r=card.getBoundingClientRect();
    var w=Math.min(r.width, vw-2*M);
    var nl=Math.min(Math.max(r.left, M), vw-M-w);
    if(Math.abs(nl-r.left)>0.5){ card.style.left=(nl-a.left)+'px'; card.style.right='auto'; }
  }
  document.addEventListener('pointerover', function(e){
    var el=e.target.closest && e.target.closest('.tt,.wchip'); if(!el) return;
    var from=e.relatedTarget; if(from && el.contains(from)) return; // already inside
    __ttplace(el);
  }, true);
  document.addEventListener('focusin', function(e){
    var el=e.target.closest && e.target.closest('.tt,.wchip'); if(el) __ttplace(el);
  }, true);
  document.addEventListener('change', function(e){ // checkbox pin, incl. ctl __set
    var x=e.target;
    if(x && x.matches && x.matches('.tt-x,.wchip-x') && x.checked){
      var el=x.closest('.tt,.wchip'); if(el) __ttplace(el);
    }
  }, true);
  var __ttraf=0;
  function __ttrepin(){ // re-place pinned cards (viewport-relative room changed)
    if(__ttraf) return;
    __ttraf=requestAnimationFrame(function(){ __ttraf=0;
      var xs=document.querySelectorAll('.tt-x:checked,.wchip-x:checked');
      for(var i=0;i<xs.length;i++){ var el=xs[i].closest('.tt,.wchip'); if(el) __ttplace(el); }
    });
  }
  window.addEventListener('resize', __ttrepin);
  document.addEventListener('scroll', __ttrepin, true);
})();`
