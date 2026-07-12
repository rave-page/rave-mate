// Raw-WebGL animated point-cloud viewer for RMPC exports (Motion Studio, task #83).
// Self-contained: no three.js / framework. Go renders the modal shell (canvas + transport)
// and calls window.__pcv.open(metaJSON); this module fetches the .rmpc over the loopback
// media endpoint, ports the RMPC decoder (magic/ver/headerLen/header/colors/frames), keeps
// the quantized u16 frame stream in ONE ArrayBuffer, and dequantizes on the GPU via bounds
// uniforms (normalized UNSIGNED_SHORT attribute). Orbit MVP is computed in JS (tiny mat4
// helpers, no lib); a soft round-disc fragment shader makes dense clouds read as a solid
// surface. GL buffers/program are disposed on close or when a new file opens (bounded memory).
// LE assumption: RMPC is little-endian; every rave-mate target (x86/ARM) is LE, so a Uint16Array
// over the file bytes maps 1:1 to the GPU's LE attribute read - no byte-swap.
(function () {
  if (window.__pcv) return;

  var S = null; // active viewer instance (single; a new open() disposes the prior)

  // ── mat4 (column-major, gl-matrix layout) ──
  function mPerspective(fovy, aspect, near, far) {
    var f = 1 / Math.tan(fovy / 2), nf = 1 / (near - far);
    return [f / aspect, 0, 0, 0, 0, f, 0, 0, 0, 0, (far + near) * nf, -1, 0, 0, 2 * far * near * nf, 0];
  }
  function mLookAt(eye, ctr, up) {
    var zx = eye[0] - ctr[0], zy = eye[1] - ctr[1], zz = eye[2] - ctr[2];
    var zl = Math.hypot(zx, zy, zz) || 1; zx /= zl; zy /= zl; zz /= zl;
    var xx = up[1] * zz - up[2] * zy, xy = up[2] * zx - up[0] * zz, xz = up[0] * zy - up[1] * zx;
    var xl = Math.hypot(xx, xy, xz) || 1; xx /= xl; xy /= xl; xz /= xl;
    var yx = zy * xz - zz * xy, yy = zz * xx - zx * xz, yz = zx * xy - zy * xx;
    return [xx, yx, zx, 0, xy, yy, zy, 0, xz, yz, zz, 0,
      -(xx * eye[0] + xy * eye[1] + xz * eye[2]),
      -(yx * eye[0] + yy * eye[1] + yz * eye[2]),
      -(zx * eye[0] + zy * eye[1] + zz * eye[2]), 1];
  }
  function mMul(a, b) { // a*b
    var o = new Array(16);
    for (var c = 0; c < 4; c++) for (var r = 0; r < 4; r++) {
      o[c * 4 + r] = a[r] * b[c * 4] + a[4 + r] * b[c * 4 + 1] + a[8 + r] * b[c * 4 + 2] + a[12 + r] * b[c * 4 + 3];
    }
    return o;
  }

  // ── shaders (GLSL ES 3.00 for WebGL2, 1.00 for WebGL1 fallback) ──
  function shaderSrc(gl2) {
    var head = gl2 ? '#version 300 es\n' : '';
    var inp = gl2 ? 'in' : 'attribute';       // vertex attribute
    var outp = gl2 ? 'out' : 'varying';       // vertex → fragment varying (out side)
    var finp = gl2 ? 'in' : 'varying';        // fragment varying (in side)
    var vs = head +
      inp + ' vec3 aPos;\n' + inp + ' vec3 aCol;\n' +
      'uniform mat4 uMVP; uniform vec3 uMin; uniform vec3 uExt;\n' +
      'uniform float uPointScale; uniform float uUseColor;\n' +
      outp + ' vec3 vCol;\n' +
      'void main(){\n' +
      '  vec3 p = uMin + aPos * uExt;\n' +
      '  vec4 clip = uMVP * vec4(p,1.0);\n' +
      '  gl_Position = clip;\n' +
      '  gl_PointSize = clamp(uPointScale / max(clip.w, 0.0001), 1.0, 48.0);\n' +
      '  vCol = mix(vec3(0.031,0.969,0.608), aCol, uUseColor);\n' + // mint fallback
      '}';
    var fragOut = gl2 ? 'outColor' : 'gl_FragColor';
    var fs = head + 'precision mediump float;\n' +
      (gl2 ? 'layout(location = 0) out vec4 outColor;\n' : '') + finp + ' vec3 vCol;\n' +
      'void main(){\n' +
      '  vec2 d = gl_PointCoord - vec2(0.5);\n' +
      '  float r2 = dot(d,d);\n' +
      '  if (r2 > 0.25) discard;\n' +               // round disc
      '  float a = smoothstep(0.25, 0.14, r2);\n' + // soft edge (AA)
      '  ' + fragOut + ' = vec4(vCol, a);\n' +
      '}';
    return { vs: vs, fs: fs };
  }
  function compile(gl, type, src) {
    var s = gl.createShader(type);
    gl.shaderSource(s, src); gl.compileShader(s);
    if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) {
      var log = gl.getShaderInfoLog(s); gl.deleteShader(s);
      throw new Error('shader: ' + log);
    }
    return s;
  }
  function makeProgram(gl, gl2) {
    var src = shaderSrc(gl2);
    var vs = compile(gl, gl.VERTEX_SHADER, src.vs), fs = compile(gl, gl.FRAGMENT_SHADER, src.fs);
    var p = gl.createProgram();
    gl.attachShader(p, vs); gl.attachShader(p, fs); gl.linkProgram(p);
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) {
      var log = gl.getProgramInfoLog(p); throw new Error('link: ' + log);
    }
    gl.deleteShader(vs); gl.deleteShader(fs);
    return p;
  }

  // ── RMPC parse (port of internal/pointcloud/format.go) ──
  function parseRMPC(buf) {
    var dv = new DataView(buf);
    if (dv.getUint8(0) !== 82 || dv.getUint8(1) !== 77 || dv.getUint8(2) !== 80 || dv.getUint8(3) !== 67) {
      throw new Error('not an RMPC file');
    }
    var ver = dv.getUint16(4, true);
    if (ver !== 1) throw new Error('unsupported RMPC version ' + ver);
    var hlen = dv.getUint32(6, true);
    var hdr = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 10, hlen)));
    var pc = hdr.point_count | 0, fc = hdr.frame_count | 0;
    if (pc <= 0) throw new Error('empty point cloud');
    var bmin = hdr.bounds.min, bmax = hdr.bounds.max;
    var ext = [bmax[0] - bmin[0], bmax[1] - bmin[1], bmax[2] - bmin[2]];
    var off = 10 + hlen, colors = null;
    if (hdr.has_color) { colors = new Uint8Array(buf.slice(off, off + pc * 3)); off += pc * 3; }
    var stride = pc * 3 * 2; // bytes/frame
    var avail = Math.floor((buf.byteLength - off) / stride);
    if (avail < fc) fc = avail;                       // tolerate a truncated tail
    var framesBuf = buf.slice(off, off + fc * stride); // aligned ArrayBuffer (offset 0, even stride)
    return {
      pc: pc, fc: Math.max(fc, 1), fps: hdr.fps > 0 ? hdr.fps : 30,
      min: bmin, ext: ext, center: [bmin[0] + ext[0] / 2, bmin[1] + ext[1] / 2, bmin[2] + ext[2] / 2],
      diag: Math.hypot(ext[0], ext[1], ext[2]) || 1,
      colors: colors, framesBuf: framesBuf, stride: stride,
    };
  }

  function fmtTime(idx, fc, fps) { return idx + ' / ' + fc + '  (' + (idx / fps).toFixed(1) + 's)'; }

  function setInfo(txt) { var el = document.getElementById('pcv-info'); if (el) { el.textContent = txt; el.setAttribute('data-value', txt); } }
  function overlay(txt) {
    var st = document.getElementById('pcv-stage'); if (!st) return;
    var o = st.querySelector('.pcv-overlay');
    if (!o) { o = document.createElement('div'); o.className = 'pcv-overlay'; st.appendChild(o); }
    o.textContent = txt; o.style.display = txt ? 'flex' : 'none';
  }

  // ── GL setup + per-frame upload ──
  function initGL(inst) {
    var cv = inst.canvas;
    var opts = { antialias: true, depth: true, alpha: false, premultipliedAlpha: false, powerPreference: 'high-performance' };
    var gl = cv.getContext('webgl2', opts), gl2 = !!gl;
    if (!gl) gl = cv.getContext('webgl', opts) || cv.getContext('experimental-webgl', opts);
    if (!gl) return null; // GPU/software GL disabled (webview GPU off)
    inst.gl = gl; inst.gl2 = gl2;
    inst.prog = makeProgram(gl, gl2);
    inst.loc = {
      aPos: gl.getAttribLocation(inst.prog, 'aPos'),
      aCol: gl.getAttribLocation(inst.prog, 'aCol'),
      uMVP: gl.getUniformLocation(inst.prog, 'uMVP'),
      uMin: gl.getUniformLocation(inst.prog, 'uMin'),
      uExt: gl.getUniformLocation(inst.prog, 'uExt'),
      uPointScale: gl.getUniformLocation(inst.prog, 'uPointScale'),
      uUseColor: gl.getUniformLocation(inst.prog, 'uUseColor'),
    };
    inst.posBuf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, inst.posBuf);
    gl.bufferData(gl.ARRAY_BUFFER, inst.data.stride, gl.DYNAMIC_DRAW); // one frame's worth
    if (inst.data.colors) {
      inst.colBuf = gl.createBuffer();
      gl.bindBuffer(gl.ARRAY_BUFFER, inst.colBuf);
      gl.bufferData(gl.ARRAY_BUFFER, inst.data.colors, gl.STATIC_DRAW);
    }
    gl.clearColor(0.039, 0.039, 0.039, 1); // bg #0a0a0a
    gl.enable(gl.DEPTH_TEST); gl.depthFunc(gl.LEQUAL);
    gl.enable(gl.BLEND); gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
    return gl;
  }
  function uploadFrame(inst, idx) {
    var gl = inst.gl, d = inst.data;
    var view = new Uint16Array(d.framesBuf, idx * d.stride, d.pc * 3);
    gl.bindBuffer(gl.ARRAY_BUFFER, inst.posBuf);
    gl.bufferSubData(gl.ARRAY_BUFFER, 0, view);
  }
  function resize(inst) {
    var cv = inst.canvas, dpr = Math.min(window.devicePixelRatio || 1, 2);
    var w = Math.max(1, Math.round(cv.clientWidth * dpr)), h = Math.max(1, Math.round(cv.clientHeight * dpr));
    if (cv.width === w && cv.height === h && inst._sized) return;
    cv.width = w; cv.height = h; inst._sized = true;
    inst.gl.viewport(0, 0, w, h);
    inst.aspect = w / Math.max(1, h);
    inst.focalPx = 0.5 * h / Math.tan(inst.fovy / 2); // px per world unit at w=1
    inst.dirty = true;
  }
  function draw(inst) {
    var gl = inst.gl, d = inst.data, L = inst.loc;
    // orbit eye from yaw/pitch/dist around the framed center
    var cp = Math.cos(inst.pitch), sp = Math.sin(inst.pitch);
    var eye = [
      d.center[0] + inst.dist * cp * Math.sin(inst.yaw),
      d.center[1] + inst.dist * sp,
      d.center[2] + inst.dist * cp * Math.cos(inst.yaw),
    ];
    var proj = mPerspective(inst.fovy, inst.aspect, Math.max(0.01, inst.dist * 0.02), inst.dist * 8 + d.diag);
    var view = mLookAt(eye, d.center, [0, 1, 0]);
    var mvp = mMul(proj, view);
    // solid-surface point scale: mean spacing (world) * focal (px) * overlap factor / w  (in shader)
    var spacing = d.diag / Math.cbrt(d.pc);
    var scale = spacing * inst.focalPx * 2.4;

    gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
    gl.useProgram(inst.prog);
    gl.uniformMatrix4fv(L.uMVP, false, new Float32Array(mvp));
    gl.uniform3fv(L.uMin, new Float32Array(d.min));
    gl.uniform3fv(L.uExt, new Float32Array(d.ext));
    gl.uniform1f(L.uPointScale, scale);
    gl.uniform1f(L.uUseColor, d.colors ? 1 : 0);
    gl.bindBuffer(gl.ARRAY_BUFFER, inst.posBuf);
    gl.enableVertexAttribArray(L.aPos);
    gl.vertexAttribPointer(L.aPos, 3, gl.UNSIGNED_SHORT, true, 0, 0); // normalized → [0,1]
    if (d.colors && L.aCol >= 0) {
      gl.bindBuffer(gl.ARRAY_BUFFER, inst.colBuf);
      gl.enableVertexAttribArray(L.aCol);
      gl.vertexAttribPointer(L.aCol, 3, gl.UNSIGNED_BYTE, true, 0, 0);
    } else if (L.aCol >= 0) {
      gl.disableVertexAttribArray(L.aCol);
    }
    gl.drawArrays(gl.POINTS, 0, d.pc);
  }

  // ── transport + input ──
  function updateTransport(inst) {
    var t = document.getElementById('pcv-time');
    if (t) { var s = fmtTime(inst.curFrame, inst.data.fc, inst.data.fps); t.textContent = s; t.setAttribute('data-value', s); }
    var sc = document.getElementById('pcv-scrub'); if (sc && document.activeElement !== sc) sc.value = inst.curFrame;
  }
  function setPlaying(inst, on) {
    inst.playing = on && inst.data.fc > 1;
    inst.last = performance.now();
    var b = document.getElementById('pcv-play');
    if (b) b.textContent = inst.playing ? '⏸ Pause' : '▶ Play';
    inst.dirty = true;
  }
  function wire(inst) {
    var ac = new AbortController(), sig = ac.signal; inst.abort = ac;
    var cv = inst.canvas;
    cv.addEventListener('pointerdown', function (e) {
      inst.drag = { x: e.clientX, y: e.clientY, yaw: inst.yaw, pitch: inst.pitch };
      try { cv.setPointerCapture(e.pointerId); } catch (_) {}
      e.preventDefault();
    }, { signal: sig });
    cv.addEventListener('pointermove', function (e) {
      if (!inst.drag) return;
      inst.yaw = inst.drag.yaw - (e.clientX - inst.drag.x) * 0.01;
      inst.pitch = Math.max(-1.5, Math.min(1.5, inst.drag.pitch + (e.clientY - inst.drag.y) * 0.01));
      inst.dirty = true;
    }, { signal: sig });
    var endDrag = function () { inst.drag = null; };
    cv.addEventListener('pointerup', endDrag, { signal: sig });
    cv.addEventListener('pointercancel', endDrag, { signal: sig });
    cv.addEventListener('wheel', function (e) {
      e.preventDefault(); e.stopPropagation();
      inst.dist = Math.max(inst.data.diag * 0.05, Math.min(inst.data.diag * 12, inst.dist * Math.exp(e.deltaY * 0.001)));
      inst.dirty = true;
    }, { signal: sig, passive: false });

    var play = document.getElementById('pcv-play');
    if (play) play.addEventListener('click', function () { setPlaying(inst, !inst.playing); }, { signal: sig });
    var scrub = document.getElementById('pcv-scrub');
    if (scrub) scrub.addEventListener('input', function () {
      inst.frameF = parseFloat(scrub.value) || 0; inst.curFrame = -1; inst.dirty = true;
    }, { signal: sig });
    window.addEventListener('resize', function () { resize(inst); }, { signal: sig });
    // Esc closes through the Go pcv-close act (dispose + clear modal) so ctl stays in sync.
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && window.rave) window.rave(JSON.stringify({ act: 'pcv-close' }));
    }, { signal: sig });
    if (window.ResizeObserver) { inst.ro = new ResizeObserver(function () { resize(inst); }); inst.ro.observe(inst.canvas); }
  }
  function loop(inst, now) {
    if (S !== inst) return; // disposed / superseded
    inst.raf = requestAnimationFrame(function (t) { loop(inst, t); });
    var dt = (now - inst.last) / 1000; inst.last = now;
    var need = inst.dirty;
    if (inst.playing && inst.data.fc > 1) {
      inst.frameF += dt * inst.data.fps;
      if (inst.frameF >= inst.data.fc) inst.frameF %= inst.data.fc;
    }
    var idx = Math.min(inst.data.fc - 1, Math.max(0, Math.floor(inst.frameF)));
    if (idx !== inst.curFrame) { inst.curFrame = idx; uploadFrame(inst, idx); updateTransport(inst); need = true; }
    if (need) { draw(inst); inst.dirty = false; }
  }

  function dispose(inst) {
    if (!inst) return;
    if (inst.raf) cancelAnimationFrame(inst.raf);
    if (inst.abort) inst.abort.abort();
    if (inst.ro) try { inst.ro.disconnect(); } catch (_) {}
    var gl = inst.gl;
    if (gl) {
      if (inst.posBuf) gl.deleteBuffer(inst.posBuf);
      if (inst.colBuf) gl.deleteBuffer(inst.colBuf);
      if (inst.prog) gl.deleteProgram(inst.prog);
      var lc = gl.getExtension('WEBGL_lose_context'); if (lc) lc.loseContext();
    }
    inst.gl = null; inst.data = null;
  }

  window.__pcv = {
    open: function (metaJSON) {
      dispose(S); S = null;
      var meta; try { meta = typeof metaJSON === 'string' ? JSON.parse(metaJSON) : metaJSON; } catch (e) { return; }
      var canvas = document.getElementById('pcv-canvas'); if (!canvas) return;
      setInfo('Loading ' + (meta.name || '') + '…'); overlay('Loading…');
      fetch(meta.url).then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status); return r.arrayBuffer();
      }).then(function (buf) {
        var data = parseRMPC(buf);
        var inst = {
          canvas: canvas, data: data, name: meta.name || '',
          yaw: 0.6, pitch: 0.3, dist: data.diag * 1.5, fovy: 50 * Math.PI / 180,
          frameF: 0, curFrame: -1, playing: false, dirty: true, last: performance.now(),
        };
        if (!initGL(inst)) {
          overlay('WebGL is unavailable in this window. Enable "webview GPU" (features.ui.webviewGpu = true) and restart to use the point-cloud viewer.');
          setInfo('WebGL unavailable — enable webview GPU');
          return;
        }
        S = inst;
        resize(inst); wire(inst);
        overlay('');
        setInfo((inst.gl2 ? 'WebGL2' : 'WebGL1') + '  ·  ' + data.pc.toLocaleString() + ' pts  ·  ' +
          data.fc + ' frames  ·  ' + data.fps + ' fps' + (data.colors ? '  ·  colour' : ''));
        var scrub = document.getElementById('pcv-scrub');
        if (scrub) { scrub.max = data.fc - 1; scrub.value = 0; scrub.disabled = data.fc < 2; }
        uploadFrame(inst, 0); inst.curFrame = 0; updateTransport(inst);
        setPlaying(inst, data.fc > 1); // autoplay animated clouds
        loop(inst, performance.now());
      }).catch(function (err) {
        overlay('Could not load point cloud: ' + (err && err.message ? err.message : err));
        setInfo('Load failed');
      });
    },
    close: function () { dispose(S); S = null; },
  };
})();
