//go:build linux && pipewire

/* shim.c - PipeWire video capture via runtime dlopen (design D8: no go.mod dep, degrade to no-op
 * when libpipewire-0.3 is absent). One capture = one pw_thread_loop + one input pw_stream connected
 * by node name (auto-reconnect). Frames land in a bounded single-slot (drop-oldest + counter); the
 * Go side pulls the latest via rpw_wait_frame.
 *
 * Two frame sources:
 *   - SHM (MemPtr/MemFd, mapped by PipeWire): converted to RGBA on the loop thread.
 *   - DMA-BUF (Spout2PW/libfunnel - dmabuf ONLY, AMD tiled modifiers): imported via EGL
 *     (EGL_EXT_image_dma_buf_import[_modifiers]) on the GPU that produced it, FBO + glReadPixels to
 *     RGBA. Modifiers are advertised from eglQueryDmaBufModifiersEXT; the PipeWire fixate/renegotiate
 *     dance is handled in on_param_changed. EGL/GLES/GBM are dlopen'd too, so no EGL => SHM-only.
 *
 * SPA POD builders/parsers are header-only inline; only the pw_, egl, gl and gbm symbols are
 * dlopen'd, so nothing is load-time linked. */
#include "shim.h"
#include "vaenc.h"

#include <dlfcn.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <time.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <pipewire/pipewire.h>
#include <spa/param/video/format-utils.h>
#include <spa/param/video/raw.h>
#include <spa/buffer/meta.h>

#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#include <gbm.h>
#include <libdrm/drm_fourcc.h>

#define RPW_MAX_MODS 64
#define RPW_MAX_PLANES 4

/* Lock-free SPSC AU ring (encode path): the PipeWire loop thread (producer) hands encoded AUs to the
 * Go consumer (rpw_wait_au) without a mutex on the per-frame path, so a jittery downstream (packetize/
 * pace/send) can be absorbed instead of forcing the single-slot newest-wins drop that caused the live
 * stutter. Bounded by COUNT (AU_RING_N) AND BYTES (AU_RING_MAXBYTES). Buffers are preallocated
 * (AU_RING_SLOT_INIT) and only realloc on a rare oversize AU - no steady-state malloc. Overflow policy:
 * the consumer is behind by more than the ring (a real stall, not jitter) => DROP-NEWEST (keep the
 * queued oldest frames the consumer will read next; a lock-free SPSC producer cannot advance the
 * consumer's read index to drop-oldest without a race) AND force an IDR on the next encoded frame +
 * count it, because dropping any AU breaks the inter-frame reference chain - never silently drop a
 * P-frame without a keyframe to resync. */
#define AU_RING_N 3
#define AU_RING_SLOT_INIT (4 * 1024 * 1024) /* 4 MB/slot preallocation (>> a 4K IDR) */
#define AU_RING_MAXBYTES (16 * 1024 * 1024) /* byte ceiling on queued AUs */

struct au_slot {
	unsigned char *buf;
	int cap;
	int len;
	int keyframe;
	int w, h; /* coded dims */
	long long pts;
};

static int egl_dbg = 0;
#define DBG(...)                            \
	do {                                    \
		if (egl_dbg)                        \
			fprintf(stderr, __VA_ARGS__);   \
	} while (0)

/* ---- dlopen'd libpipewire entry points ---------------------------------------------------- */
struct pw_api {
	void (*init)(int *, char ***);
	struct pw_thread_loop *(*tl_new)(const char *, const struct spa_dict *);
	void (*tl_destroy)(struct pw_thread_loop *);
	int (*tl_start)(struct pw_thread_loop *);
	void (*tl_stop)(struct pw_thread_loop *);
	void (*tl_lock)(struct pw_thread_loop *);
	void (*tl_unlock)(struct pw_thread_loop *);
	struct pw_loop *(*tl_get_loop)(struct pw_thread_loop *);
	struct pw_context *(*ctx_new)(struct pw_loop *, struct pw_properties *, size_t);
	void (*ctx_destroy)(struct pw_context *);
	struct pw_core *(*ctx_connect)(struct pw_context *, struct pw_properties *, size_t);
	int (*core_disconnect)(struct pw_core *);
	struct pw_properties *(*props_new)(const char *, ...);
	struct pw_stream *(*stream_new)(struct pw_core *, const char *, struct pw_properties *);
	void (*stream_destroy)(struct pw_stream *);
	void (*stream_add_listener)(struct pw_stream *, struct spa_hook *, const struct pw_stream_events *, void *);
	int (*stream_connect)(struct pw_stream *, enum pw_direction, uint32_t, enum pw_stream_flags, const struct spa_pod **, uint32_t);
	int (*stream_disconnect)(struct pw_stream *);
	struct pw_buffer *(*stream_dequeue)(struct pw_stream *);
	int (*stream_queue)(struct pw_stream *, struct pw_buffer *);
	int (*stream_update_params)(struct pw_stream *, const struct spa_pod **, uint32_t);
};
static struct pw_api PW;
static int pw_loaded = -1;
static pthread_mutex_t load_mu = PTHREAD_MUTEX_INITIALIZER;

static int resolve_pw(void *h) {
	struct { const char *n; void **s; } t[] = {
		{"pw_init", (void **)&PW.init},
		{"pw_thread_loop_new", (void **)&PW.tl_new},
		{"pw_thread_loop_destroy", (void **)&PW.tl_destroy},
		{"pw_thread_loop_start", (void **)&PW.tl_start},
		{"pw_thread_loop_stop", (void **)&PW.tl_stop},
		{"pw_thread_loop_lock", (void **)&PW.tl_lock},
		{"pw_thread_loop_unlock", (void **)&PW.tl_unlock},
		{"pw_thread_loop_get_loop", (void **)&PW.tl_get_loop},
		{"pw_context_new", (void **)&PW.ctx_new},
		{"pw_context_destroy", (void **)&PW.ctx_destroy},
		{"pw_context_connect", (void **)&PW.ctx_connect},
		{"pw_core_disconnect", (void **)&PW.core_disconnect},
		{"pw_properties_new", (void **)&PW.props_new},
		{"pw_stream_new", (void **)&PW.stream_new},
		{"pw_stream_destroy", (void **)&PW.stream_destroy},
		{"pw_stream_add_listener", (void **)&PW.stream_add_listener},
		{"pw_stream_connect", (void **)&PW.stream_connect},
		{"pw_stream_disconnect", (void **)&PW.stream_disconnect},
		{"pw_stream_dequeue_buffer", (void **)&PW.stream_dequeue},
		{"pw_stream_queue_buffer", (void **)&PW.stream_queue},
		{"pw_stream_update_params", (void **)&PW.stream_update_params},
	};
	for (size_t i = 0; i < sizeof(t) / sizeof(t[0]); i++) {
		void *s = dlsym(h, t[i].n);
		if (!s)
			return 0;
		*t[i].s = s;
	}
	return 1;
}

int rpw_available(void) {
	pthread_mutex_lock(&load_mu);
	if (pw_loaded == -1) {
		egl_dbg = getenv("RPW_EGL_DEBUG") != NULL;
		void *h = dlopen("libpipewire-0.3.so.0", RTLD_NOW | RTLD_GLOBAL);
		if (h && resolve_pw(h)) {
			int argc = 0;
			PW.init(&argc, NULL);
			pw_loaded = 1;
		} else {
			pw_loaded = 0;
		}
	}
	pthread_mutex_unlock(&load_mu);
	return pw_loaded == 1;
}

/* ---- dlopen'd EGL / GLES / GBM (optional: absent => SHM-only) ------------------------------ */
struct egl_api {
	int ok;
	/* core egl */
	void *(*GetProcAddress)(const char *);
	EGLint (*GetError)(void);
	EGLBoolean (*Initialize)(EGLDisplay, EGLint *, EGLint *);
	EGLBoolean (*Terminate)(EGLDisplay);
	EGLBoolean (*BindAPI)(EGLenum);
	EGLContext (*CreateContext)(EGLDisplay, EGLConfig, EGLContext, const EGLint *);
	EGLBoolean (*DestroyContext)(EGLDisplay, EGLContext);
	EGLBoolean (*MakeCurrent)(EGLDisplay, EGLSurface, EGLSurface, EGLContext);
	/* extensions via GetProcAddress */
	PFNEGLGETPLATFORMDISPLAYEXTPROC GetPlatformDisplayEXT;
	PFNEGLCREATEIMAGEKHRPROC CreateImageKHR;
	PFNEGLDESTROYIMAGEKHRPROC DestroyImageKHR;
	PFNEGLQUERYDMABUFMODIFIERSEXTPROC QueryDmaBufModifiersEXT;
	PFNGLEGLIMAGETARGETTEXTURE2DOESPROC ImageTargetTexture2DOES;
	/* gles core */
	void (*GenTextures)(GLsizei, GLuint *);
	void (*BindTexture)(GLenum, GLuint);
	void (*DeleteTextures)(GLsizei, const GLuint *);
	void (*TexParameteri)(GLenum, GLenum, GLint);
	void (*GenFramebuffers)(GLsizei, GLuint *);
	void (*BindFramebuffer)(GLenum, GLuint);
	void (*DeleteFramebuffers)(GLsizei, const GLuint *);
	void (*FramebufferTexture2D)(GLenum, GLenum, GLenum, GLuint, GLint);
	GLenum (*CheckFramebufferStatus)(GLenum);
	void (*ReadPixels)(GLint, GLint, GLsizei, GLsizei, GLenum, GLenum, void *);
	void (*Finish)(void);
	void (*PixelStorei)(GLenum, GLint);
	GLenum (*GlGetError)(void);
	/* gbm */
	struct gbm_device *(*gbm_create_device)(int);
	void (*gbm_device_destroy)(struct gbm_device *);
};
static struct egl_api E;
static int egl_loaded = -1;

static int egl_load(void) {
	if (egl_loaded != -1)
		return egl_loaded;
	egl_loaded = 0;
	void *le = dlopen("libEGL.so.1", RTLD_NOW | RTLD_GLOBAL);
	void *lg = dlopen("libGLESv2.so.2", RTLD_NOW | RTLD_GLOBAL);
	void *lb = dlopen("libgbm.so.1", RTLD_NOW | RTLD_GLOBAL);
	if (!le || !lg || !lb) {
		DBG("egl_load: dlopen failed (egl=%p gles=%p gbm=%p)\n", le, lg, lb);
		return 0;
	}
	E.GetProcAddress = dlsym(le, "eglGetProcAddress");
	E.GetError = dlsym(le, "eglGetError");
	E.Initialize = dlsym(le, "eglInitialize");
	E.Terminate = dlsym(le, "eglTerminate");
	E.BindAPI = dlsym(le, "eglBindAPI");
	E.CreateContext = dlsym(le, "eglCreateContext");
	E.DestroyContext = dlsym(le, "eglDestroyContext");
	E.MakeCurrent = dlsym(le, "eglMakeCurrent");
	E.GenTextures = dlsym(lg, "glGenTextures");
	E.BindTexture = dlsym(lg, "glBindTexture");
	E.DeleteTextures = dlsym(lg, "glDeleteTextures");
	E.TexParameteri = dlsym(lg, "glTexParameteri");
	E.GenFramebuffers = dlsym(lg, "glGenFramebuffers");
	E.BindFramebuffer = dlsym(lg, "glBindFramebuffer");
	E.DeleteFramebuffers = dlsym(lg, "glDeleteFramebuffers");
	E.FramebufferTexture2D = dlsym(lg, "glFramebufferTexture2D");
	E.CheckFramebufferStatus = dlsym(lg, "glCheckFramebufferStatus");
	E.ReadPixels = dlsym(lg, "glReadPixels");
	E.Finish = dlsym(lg, "glFinish");
	E.PixelStorei = dlsym(lg, "glPixelStorei");
	E.GlGetError = dlsym(lg, "glGetError");
	E.gbm_create_device = dlsym(lb, "gbm_create_device");
	E.gbm_device_destroy = dlsym(lb, "gbm_device_destroy");
	if (!E.GetProcAddress || !E.Initialize || !E.CreateContext || !E.MakeCurrent ||
	    !E.GenTextures || !E.ReadPixels || !E.gbm_create_device) {
		DBG("egl_load: core symbol missing\n");
		return 0;
	}
	E.GetPlatformDisplayEXT = (PFNEGLGETPLATFORMDISPLAYEXTPROC)E.GetProcAddress("eglGetPlatformDisplayEXT");
	E.CreateImageKHR = (PFNEGLCREATEIMAGEKHRPROC)E.GetProcAddress("eglCreateImageKHR");
	E.DestroyImageKHR = (PFNEGLDESTROYIMAGEKHRPROC)E.GetProcAddress("eglDestroyImageKHR");
	E.QueryDmaBufModifiersEXT = (PFNEGLQUERYDMABUFMODIFIERSEXTPROC)E.GetProcAddress("eglQueryDmaBufModifiersEXT");
	E.ImageTargetTexture2DOES = (PFNGLEGLIMAGETARGETTEXTURE2DOESPROC)E.GetProcAddress("glEGLImageTargetTexture2DOES");
	if (!E.GetPlatformDisplayEXT || !E.CreateImageKHR || !E.DestroyImageKHR || !E.ImageTargetTexture2DOES) {
		DBG("egl_load: dmabuf import extensions missing\n");
		return 0;
	}
	E.ok = 1;
	egl_loaded = 1;
	return 1;
}

/* SPA raw format -> DRM fourcc (byte-order mapped: SPA names are byte order, DRM are LE packed). */
static uint32_t spa_to_drm_fourcc(enum spa_video_format f) {
	switch (f) {
	case SPA_VIDEO_FORMAT_BGRA: return DRM_FORMAT_ARGB8888;
	case SPA_VIDEO_FORMAT_RGBA: return DRM_FORMAT_ABGR8888;
	case SPA_VIDEO_FORMAT_BGRx: return DRM_FORMAT_XRGB8888;
	case SPA_VIDEO_FORMAT_RGBx: return DRM_FORMAT_XBGR8888;
	default: return 0;
	}
}

/* the SPA formats we offer, preference order */
static const enum spa_video_format OFFER_FORMATS[] = {
	SPA_VIDEO_FORMAT_BGRA, SPA_VIDEO_FORMAT_RGBA, SPA_VIDEO_FORMAT_BGRx, SPA_VIDEO_FORMAT_RGBx};
#define N_OFFER (sizeof(OFFER_FORMATS) / sizeof(OFFER_FORMATS[0]))

/* ---- capture state -------------------------------------------------------------------------- */
struct rpw_capture {
	struct pw_thread_loop *loop;
	struct pw_context *context;
	struct pw_core *core;
	struct pw_stream *stream;
	struct spa_hook listener;

	pthread_mutex_t mu;
	pthread_cond_t cv;
	int width, height;
	enum spa_video_format fmt;
	uint64_t modifier; /* negotiated dmabuf modifier; DRM_FORMAT_MOD_INVALID = SHM */
	int is_dmabuf;

	unsigned char *slot;
	int slot_cap;
	int has_frame;
	int fatal;
	int need_reconnect; /* stream hit ERROR (producer restart) - re-arm the connection */
	int va_probed;      /* RPW_VA_PROBE: VA import probe already run (once) */
	int slot_len;       /* bytes valid in slot: 0 = w*h*4 (RGBA); >0 = encoded AU length */
	struct rva_enc *enc; /* combined VA capture+encode -> H.264/HEVC AU in slot */
	int enc_fail;
	long dropped;
	long dmabuf_skipped;
	long reconnects;

	/* ---- vaapi-native encode mode (production: rpw_set_encode; also RPW_VA_ENC env for the harness) */
	int enc_mode;        /* 0 = readback RGBA; 1 = fused dmabuf->VA encode (AU in slot) */
	int enc_codec;       /* 0 = H.264, 1 = HEVC */
	int enc_kbps;        /* target bitrate budget (CBR target, or the CQP ceiling in quality mode) */
	int enc_maxh;        /* coded-height cap (0 = native); VPP downscales taller sources */
	int enc_fps;
	int enc_quality_qp;  /* 0 = legacy CBR; >0 = constant-quality CQP at this target QP (ceiling=kbps) */
	char enc_node[96];   /* VA render node */
	int force_idr;       /* rpw_request_keyframe sets it; on_process consumes + clears (under mu) */
	int pending_kbps;    /* rpw_set_bitrate sets it; on_process applies live (under mu) */
	int pending_quality; /* rpw_set_quality sets it (target QP); on_process applies live (under mu) */
	int pending_maxh;    /* rpw_set_cap_height sets it (-1 = none); on_process re-creates the encoder at
	                      * the new coded size (nominal source geometry unchanged; receiver upscales) */
	int slot_keyframe;   /* keyframe flag of the AU in slot */
	int coded_w, coded_h; /* coded output dims (telemetry) */
	/* encode telemetry (read via getters; written on the loop thread) */
	long enc_aus;        /* AUs emitted */
	long enc_drops;      /* frames dropped on the encode path (newest-wins + encode errors) */
	long cap_frames;     /* dmabuf frames seen in encode mode */
	int fb_present;      /* adaptive-budget feedback snapshot (written on the loop thread, read by getter) */
	double fb_avg_qp;    /* mean QP over ~1s (exact in CQP) */
	int fb_rate_limited; /* 1 when the ceiling forced QP above the quality target */
	int import_ok;       /* 1 once a dmabuf imported+encoded at least once (va-dmabuf path live) */
	int flip_default;    /* RPW_FLIP_V: default vertical flip when the buffer carries no transform meta.
	                      * 0 = top-down (Spout2PW/DX + PipeWire convention); 1 = bottom-up (GL producer) */
	long long slot_pts;  /* capture-clock PTS (ns) of the AU/frame in slot (from SPA_META_Header) */

	/* lock-free SPSC AU ring (encode path only; the raw RGBA path keeps the single slot above). */
	struct au_slot ring[AU_RING_N];
	_Atomic unsigned ring_head; /* consumer-owned read index (monotonic; %N on access) */
	_Atomic unsigned ring_tail; /* producer-owned write index (monotonic) */
	_Atomic long ring_bytes;    /* queued AU bytes (producer +=, consumer -=) */
	int ring_hiwater;           /* max fill seen (producer-only) */
	long ring_overflow;         /* AUs dropped because the consumer fell behind the ring (producer-only) */

	/* EGL (dmabuf import) - display/context built in rpw_start (no current); made current lazily
	 * on the loop thread in on_process. */
	int egl_ready;        /* display+context+modifiers queried */
	int egl_current;      /* made current on the loop thread */
	int drm_fd;
	struct gbm_device *gbm;
	EGLDisplay dpy;
	EGLContext egl_ctx;
	GLuint fbo;
	unsigned char *rbscratch; /* glReadPixels target before vertical flip into slot */
	int rbscratch_cap;
	/* EGL-supported modifiers per offered format */
	uint64_t mods[N_OFFER][RPW_MAX_MODS];
	int n_mods[N_OFFER];
};

/* au_ring_init preallocates the ring slot buffers + resets indices (at encode engage). */
static void au_ring_init(struct rpw_capture *c) {
	for (int i = 0; i < AU_RING_N; i++) {
		if (!c->ring[i].buf) {
			c->ring[i].buf = malloc(AU_RING_SLOT_INIT);
			c->ring[i].cap = c->ring[i].buf ? AU_RING_SLOT_INIT : 0;
		}
		c->ring[i].len = 0;
	}
	atomic_store(&c->ring_head, 0);
	atomic_store(&c->ring_tail, 0);
	atomic_store(&c->ring_bytes, 0);
	c->ring_hiwater = 0;
}

/* au_ring_free releases the slot buffers (after the loop thread stopped). */
static void au_ring_free(struct rpw_capture *c) {
	for (int i = 0; i < AU_RING_N; i++) {
		free(c->ring[i].buf);
		c->ring[i].buf = NULL;
		c->ring[i].cap = 0;
	}
}

/* au_ring_push enqueues one encoded AU (producer = the PipeWire loop thread; lock-free: a relaxed
 * tail read, an acquire head read, a release tail publish - no mutex on the per-frame path). Returns 1
 * when the caller must force an IDR next (an overflow drop happened; see the ring doc). */
static int au_ring_push(struct rpw_capture *c, const unsigned char *au, int aulen, int kf, long long pts) {
	unsigned t = atomic_load_explicit(&c->ring_tail, memory_order_relaxed);
	unsigned h = atomic_load_explicit(&c->ring_head, memory_order_acquire);
	long qbytes = atomic_load_explicit(&c->ring_bytes, memory_order_relaxed);
	if ((t - h) >= AU_RING_N || qbytes + aulen > AU_RING_MAXBYTES) {
		c->ring_overflow++; /* consumer stalled > ring depth: drop THIS (newest) + force IDR + count */
		c->enc_drops++;
		c->dropped++;
		return 1;
	}
	struct au_slot *s = &c->ring[t % AU_RING_N];
	if (aulen > s->cap) { /* rare: an AU bigger than the preallocation; grow (not per-frame) */
		int ncap = aulen < AU_RING_SLOT_INIT ? AU_RING_SLOT_INIT : aulen;
		unsigned char *nb = realloc(s->buf, ncap);
		if (!nb) {
			c->ring_overflow++;
			c->enc_drops++;
			c->dropped++;
			return 1;
		}
		s->buf = nb;
		s->cap = ncap;
	}
	memcpy(s->buf, au, aulen);
	s->len = aulen;
	s->keyframe = kf;
	s->pts = pts;
	s->w = c->coded_w;
	s->h = c->coded_h;
	atomic_fetch_add_explicit(&c->ring_bytes, aulen, memory_order_relaxed);
	atomic_store_explicit(&c->ring_tail, t + 1, memory_order_release); /* publish */
	int depth = (int)(t + 1 - h);
	if (depth > c->ring_hiwater)
		c->ring_hiwater = depth;
	c->enc_aus++;
	return 0;
}

/* egl_setup_display: open the render node, GBM device, EGL display + GLES context (NOT current),
 * and query dmabuf modifiers per offered format. Runs on the caller thread in rpw_start. */
static void egl_setup_display(struct rpw_capture *c, const char *node) {
	if (!egl_load())
		return;
	c->drm_fd = open(node, O_RDWR | O_CLOEXEC);
	if (c->drm_fd < 0) {
		DBG("egl: open %s failed\n", node);
		return;
	}
	c->gbm = E.gbm_create_device(c->drm_fd);
	if (!c->gbm) {
		DBG("egl: gbm_create_device failed\n");
		return;
	}
	c->dpy = E.GetPlatformDisplayEXT(EGL_PLATFORM_GBM_KHR, c->gbm, NULL);
	if (c->dpy == EGL_NO_DISPLAY || !E.Initialize(c->dpy, NULL, NULL)) {
		DBG("egl: display/init failed\n");
		return;
	}
	E.BindAPI(EGL_OPENGL_ES_API);
	EGLint ctxattr[] = {EGL_CONTEXT_MAJOR_VERSION, 2, EGL_NONE};
	c->egl_ctx = E.CreateContext(c->dpy, (EGLConfig)0 /*EGL_NO_CONFIG_KHR*/, EGL_NO_CONTEXT, ctxattr);
	if (c->egl_ctx == EGL_NO_CONTEXT) {
		DBG("egl: CreateContext failed 0x%x\n", E.GetError());
		return;
	}
	/* modifiers per format. By default we DROP AMD DCC-enabled modifiers from the offered list so the
	 * producer's GBM allocator is forced to a single-plane tiled layout - the only one both VA's
	 * DRM_PRIME_2 import AND EGL import accept at 4K (the 3-plane DCC_RETILE layout the driver picks at
	 * 4K is rejected by both; nodcc is otherwise a producer-side AMD_DEBUG the consumer can't set, e.g.
	 * VRChat under Proton). AMD DCC = modifier bit 13 (AMD_FMT_MOD_DCC); LINEAR + non-AMD kept as-is.
	 * RPW_ALLOW_DCC keeps the full list (A/B testing / non-AMD GPUs where the heuristic shouldn't bite). */
	int allow_dcc = getenv("RPW_ALLOW_DCC") != NULL;
	if (E.QueryDmaBufModifiersEXT) {
		for (size_t i = 0; i < N_OFFER; i++) {
			uint32_t drm = spa_to_drm_fourcc(OFFER_FORMATS[i]);
			uint64_t tmp[RPW_MAX_MODS];
			EGLint num = 0;
			if (!E.QueryDmaBufModifiersEXT(c->dpy, drm, RPW_MAX_MODS, tmp, NULL, &num) || num <= 0)
				continue;
			int k = 0;
			for (int j = 0; j < num && k < RPW_MAX_MODS; j++) {
				uint64_t m = tmp[j];
				int amd = ((m >> 56) & 0xff) == 0x02;   /* DRM_FORMAT_MOD_VENDOR_AMD */
				int dcc = amd && ((m >> 13) & 1);        /* AMD_FMT_MOD_DCC */
				if (dcc && !allow_dcc)
					continue; /* skip DCC: not importable at 4K */
				c->mods[i][k++] = m;
			}
			c->n_mods[i] = k;
		}
	}
	c->egl_ready = 1;
	DBG("egl: display ready, mods[BGRA]=%d (allowDcc=%d)\n", c->n_mods[0], allow_dcc);
}

/* egl_make_current binds the context to the CURRENT (loop) thread, once. */
static int egl_ensure_current(struct rpw_capture *c) {
	if (c->egl_current)
		return 1;
	if (!c->egl_ready)
		return 0;
	if (!E.MakeCurrent(c->dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, c->egl_ctx)) {
		DBG("egl: MakeCurrent failed 0x%x\n", E.GetError());
		return 0;
	}
	E.GenFramebuffers(1, &c->fbo);
	c->egl_current = 1;
	return 1;
}

/* want_flip decides whether to vertically flip THIS buffer so the output is image-top-first.
 * Signal priority: an explicit SPA_META_VideoTransform on the buffer (honour it - Flipped180 = a
 * vertical mirror), else the per-source default (RPW_FLIP_V / flip_default). The default is 0 =
 * top-down, correct for Spout2PW (DX-origin) and the PipeWire raw-video convention; a GL-origin
 * producer (bottom-up, e.g. our libfunnel test) sets flip_default=1. Both the readback and the VA
 * paths call this so they always agree. */
static int want_flip(struct rpw_capture *c, struct spa_buffer *sb) {
	struct spa_meta_videotransform *vt =
		spa_buffer_find_meta_data(sb, SPA_META_VideoTransform, sizeof(*vt));
	if (vt) {
		/* Honour only the vertical-mirror transforms; a rotation/horizontal-only transform is not a
		 * vertical flip, so fall back to the default (and it would need VPP rotation we don't do). */
		return vt->transform == SPA_META_TRANSFORMATION_Flipped180;
	}
	return c->flip_default;
}

/* buffer_pts returns the buffer's capture PTS (ns) from SPA_META_Header, or 0 when absent/invalid
 * (caller stamps a wall-clock time). Correct inter-frame timing for a variable-framerate source. */
static long long buffer_pts(struct spa_buffer *sb) {
	struct spa_meta_header *h = spa_buffer_find_meta_data(sb, SPA_META_Header, sizeof(*h));
	if (h && h->pts > 0)
		return (long long)h->pts;
	return 0;
}

/* egl_import_frame imports a dmabuf spa_buffer via EGL, reads it back to RGBA, stores it in the slot
 * (vertically flipped per want_flip). Loop thread. */
static int egl_import_frame(struct rpw_capture *c, struct spa_buffer *sb) {
	if (!egl_ensure_current(c) || c->width <= 0)
		return 0;
	uint32_t drm = spa_to_drm_fourcc(c->fmt);
	if (!drm)
		return 0;

	EGLint attr[6 + RPW_MAX_PLANES * 10 + 2];
	int a = 0;
	attr[a++] = EGL_WIDTH;
	attr[a++] = c->width;
	attr[a++] = EGL_HEIGHT;
	attr[a++] = c->height;
	attr[a++] = EGL_LINUX_DRM_FOURCC_EXT;
	attr[a++] = (EGLint)drm;
	static const EGLint plane_fd[] = {EGL_DMA_BUF_PLANE0_FD_EXT, EGL_DMA_BUF_PLANE1_FD_EXT, EGL_DMA_BUF_PLANE2_FD_EXT, EGL_DMA_BUF_PLANE3_FD_EXT};
	static const EGLint plane_off[] = {EGL_DMA_BUF_PLANE0_OFFSET_EXT, EGL_DMA_BUF_PLANE1_OFFSET_EXT, EGL_DMA_BUF_PLANE2_OFFSET_EXT, EGL_DMA_BUF_PLANE3_OFFSET_EXT};
	static const EGLint plane_pitch[] = {EGL_DMA_BUF_PLANE0_PITCH_EXT, EGL_DMA_BUF_PLANE1_PITCH_EXT, EGL_DMA_BUF_PLANE2_PITCH_EXT, EGL_DMA_BUF_PLANE3_PITCH_EXT};
	static const EGLint plane_ml[] = {EGL_DMA_BUF_PLANE0_MODIFIER_LO_EXT, EGL_DMA_BUF_PLANE1_MODIFIER_LO_EXT, EGL_DMA_BUF_PLANE2_MODIFIER_LO_EXT, EGL_DMA_BUF_PLANE3_MODIFIER_LO_EXT};
	static const EGLint plane_mh[] = {EGL_DMA_BUF_PLANE0_MODIFIER_HI_EXT, EGL_DMA_BUF_PLANE1_MODIFIER_HI_EXT, EGL_DMA_BUF_PLANE2_MODIFIER_HI_EXT, EGL_DMA_BUF_PLANE3_MODIFIER_HI_EXT};
	/* Pass every plane the producer gave (a tiled modifier needs its DCC metadata plane to detile).
	 * Single-plane at 1080p imports fine; 4K DCC comes back multi-plane and mesa rejects it. */
	uint32_t nplanes = sb->n_datas < RPW_MAX_PLANES ? sb->n_datas : RPW_MAX_PLANES;
	for (uint32_t p = 0; p < nplanes; p++) {
		struct spa_data *d = &sb->datas[p];
		DBG("egl: plane %u fd=%d off=%u stride=%d size=%u\n", p, (int)d->fd,
			d->chunk ? d->chunk->offset : 0, d->chunk ? d->chunk->stride : 0, d->chunk ? d->chunk->size : 0);
		attr[a++] = plane_fd[p];
		attr[a++] = (EGLint)d->fd;
		attr[a++] = plane_off[p];
		attr[a++] = (EGLint)(d->chunk ? d->chunk->offset : 0); /* dmabuf plane offset (not mapoffset: that's SHM) */
		attr[a++] = plane_pitch[p];
		attr[a++] = (EGLint)(d->chunk ? d->chunk->stride : c->width * 4);
		if (c->modifier != DRM_FORMAT_MOD_INVALID) {
			attr[a++] = plane_ml[p];
			attr[a++] = (EGLint)(c->modifier & 0xffffffff);
			attr[a++] = plane_mh[p];
			attr[a++] = (EGLint)(c->modifier >> 32);
		}
	}
	attr[a++] = EGL_NONE;

	EGLImageKHR img = E.CreateImageKHR(c->dpy, EGL_NO_CONTEXT, EGL_LINUX_DMA_BUF_EXT, NULL, attr);
	if (img == EGL_NO_IMAGE_KHR) {
		DBG("egl: CreateImage failed 0x%x (planes=%u mod=0x%llx)\n", E.GetError(), nplanes, (unsigned long long)c->modifier);
		return 0;
	}
	GLuint tex = 0;
	E.GenTextures(1, &tex);
	E.BindTexture(GL_TEXTURE_2D, tex);
	E.TexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
	E.TexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
	E.ImageTargetTexture2DOES(GL_TEXTURE_2D, img);
	E.BindFramebuffer(GL_FRAMEBUFFER, c->fbo);
	E.FramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, tex, 0);

	int ok = 0;
	if (E.CheckFramebufferStatus(GL_FRAMEBUFFER) == GL_FRAMEBUFFER_COMPLETE) {
		int need = c->width * c->height * 4;
		if (need > c->rbscratch_cap) {
			unsigned char *n = realloc(c->rbscratch, need);
			if (n) {
				c->rbscratch = n;
				c->rbscratch_cap = need;
			}
		}
		if (c->rbscratch_cap >= need) {
			E.PixelStorei(GL_PACK_ALIGNMENT, 1);
			E.ReadPixels(0, 0, c->width, c->height, GL_RGBA, GL_UNSIGNED_BYTE, c->rbscratch);
			E.Finish();
			ok = 1;
		}
	} else {
		DBG("egl: FBO incomplete\n");
	}
	E.FramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, 0, 0);
	E.BindFramebuffer(GL_FRAMEBUFFER, 0);
	E.DeleteTextures(1, &tex);
	E.DestroyImageKHR(c->dpy, img);
	if (!ok)
		return 0;

	/* flip rows (GL bottom-up -> image top-down) into the slot */
	pthread_mutex_lock(&c->mu);
	int need = c->width * c->height * 4;
	if (need > c->slot_cap) {
		unsigned char *n = realloc(c->slot, need);
		if (!n) {
			c->dropped++;
			pthread_mutex_unlock(&c->mu);
			return 0;
		}
		c->slot = n;
		c->slot_cap = need;
	}
	int row = c->width * 4;
	int flip = want_flip(c, sb);
	if (flip) {
		for (int y = 0; y < c->height; y++)
			memcpy(c->slot + (size_t)y * row, c->rbscratch + (size_t)(c->height - 1 - y) * row, row);
	} else {
		memcpy(c->slot, c->rbscratch, (size_t)c->height * row);
	}
	if (c->has_frame)
		c->dropped++;
	c->has_frame = 1;
	c->slot_pts = buffer_pts(sb);
	pthread_cond_signal(&c->cv);
	pthread_mutex_unlock(&c->mu);
	return 1;
}

/* convert one SHM frame to RGBA (Go NRGBA order) handling stride + the 4 layouts. */
static void to_rgba(unsigned char *dst, const unsigned char *src, int w, int h, int stride, enum spa_video_format f) {
	int rgb = (f == SPA_VIDEO_FORMAT_RGBA || f == SPA_VIDEO_FORMAT_RGBx);
	int opaque = (f == SPA_VIDEO_FORMAT_BGRx || f == SPA_VIDEO_FORMAT_RGBx);
	for (int y = 0; y < h; y++) {
		const unsigned char *s = src + (size_t)y * stride;
		unsigned char *d = dst + (size_t)y * w * 4;
		for (int x = 0; x < w; x++) {
			unsigned char c0 = s[0], c1 = s[1], c2 = s[2], c3 = s[3];
			if (rgb) {
				d[0] = c0;
				d[1] = c1;
				d[2] = c2;
			} else {
				d[0] = c2;
				d[1] = c1;
				d[2] = c0;
			}
			d[3] = opaque ? 255 : c3;
			s += 4;
			d += 4;
		}
	}
}

static void store_shm(struct rpw_capture *c, const unsigned char *src, int stride) {
	pthread_mutex_lock(&c->mu);
	int need = c->width * c->height * 4;
	if (need > c->slot_cap) {
		unsigned char *n = realloc(c->slot, need);
		if (!n) {
			c->dropped++;
			pthread_mutex_unlock(&c->mu);
			return;
		}
		c->slot = n;
		c->slot_cap = need;
	}
	if (c->has_frame)
		c->dropped++;
	to_rgba(c->slot, src, c->width, c->height, stride, c->fmt);
	c->has_frame = 1;
	pthread_cond_signal(&c->cv);
	pthread_mutex_unlock(&c->mu);
}

/* build_dmabuf_format: one Format object advertising all EGL modifiers for fmt as a DONT_FIXATE
 * enum choice (the PipeWire dmabuf negotiation convention). */
static struct spa_pod *build_dmabuf_format(struct spa_pod_builder *b, struct rpw_capture *c, int idx) {
	struct spa_pod_frame f0, f1;
	spa_pod_builder_push_object(b, &f0, SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat);
	spa_pod_builder_add(b, SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video), 0);
	spa_pod_builder_add(b, SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw), 0);
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_format, SPA_POD_Id(OFFER_FORMATS[idx]), 0);
	/* modifier: enum choice of EGL-supported modifiers, DONT_FIXATE so we fixate on param_changed */
	spa_pod_builder_prop(b, SPA_FORMAT_VIDEO_modifier, SPA_POD_PROP_FLAG_MANDATORY | SPA_POD_PROP_FLAG_DONT_FIXATE);
	spa_pod_builder_push_choice(b, &f1, SPA_CHOICE_Enum, 0);
	spa_pod_builder_long(b, (int64_t)c->mods[idx][0]); /* default */
	for (int i = 0; i < c->n_mods[idx]; i++)
		spa_pod_builder_long(b, (int64_t)c->mods[idx][i]);
	spa_pod_builder_pop(b, &f1);
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_size,
		SPA_POD_CHOICE_RANGE_Rectangle(&SPA_RECTANGLE(1920, 1080), &SPA_RECTANGLE(1, 1), &SPA_RECTANGLE(8192, 8192)), 0);
	/* Default framerate 60: a rate-pacing producer (libfunnel/Spout2PW) delivers at the NEGOTIATED
	 * rate, so offering 30 capped a 120 fps game at 30. 60 is our sender cap; a slower source still
	 * delivers its own rate (range min 0). */
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_framerate,
		SPA_POD_CHOICE_RANGE_Fraction(&SPA_FRACTION(60, 1), &SPA_FRACTION(0, 1), &SPA_FRACTION(240, 1)), 0);
	return spa_pod_builder_pop(b, &f0);
}

/* build_shm_format: one Format object (no modifier) for SHM producers. */
static struct spa_pod *build_shm_format(struct spa_pod_builder *b) {
	return spa_pod_builder_add_object(b,
		SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat,
		SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video),
		SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw),
		SPA_FORMAT_VIDEO_format, SPA_POD_CHOICE_ENUM_Id(5,
			SPA_VIDEO_FORMAT_BGRA, SPA_VIDEO_FORMAT_BGRA, SPA_VIDEO_FORMAT_RGBA,
			SPA_VIDEO_FORMAT_BGRx, SPA_VIDEO_FORMAT_RGBx),
		SPA_FORMAT_VIDEO_size, SPA_POD_CHOICE_RANGE_Rectangle(
			&SPA_RECTANGLE(1920, 1080), &SPA_RECTANGLE(1, 1), &SPA_RECTANGLE(8192, 8192)),
		SPA_FORMAT_VIDEO_framerate, SPA_POD_CHOICE_RANGE_Fraction(
			&SPA_FRACTION(60, 1), &SPA_FRACTION(0, 1), &SPA_FRACTION(240, 1)));
}

/* n_enum_formats: how many Format objects we advertise (dmabuf per format where EGL has modifiers,
 * + one SHM). */
static uint32_t build_enum_formats(struct spa_pod_builder *b, struct rpw_capture *c, const struct spa_pod **params, uint32_t maxp) {
	uint32_t n = 0;
	if (c->egl_ready) {
		for (size_t i = 0; i < N_OFFER && n < maxp - 1; i++) {
			if (c->n_mods[i] > 0)
				params[n++] = build_dmabuf_format(b, c, (int)i);
		}
	}
	if (n < maxp)
		params[n++] = build_shm_format(b);
	return n;
}

/* fixate_format: re-advertise a single Format with the modifier fixed to `mod` (no DONT_FIXATE),
 * the second step of dmabuf negotiation. */
static struct spa_pod *fixate_format(struct spa_pod_builder *b, enum spa_video_format fmt, uint64_t mod) {
	struct spa_pod_frame f0;
	spa_pod_builder_push_object(b, &f0, SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat);
	spa_pod_builder_add(b, SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video), 0);
	spa_pod_builder_add(b, SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw), 0);
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_format, SPA_POD_Id(fmt), 0);
	spa_pod_builder_prop(b, SPA_FORMAT_VIDEO_modifier, SPA_POD_PROP_FLAG_MANDATORY);
	spa_pod_builder_long(b, (int64_t)mod);
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_size,
		SPA_POD_CHOICE_RANGE_Rectangle(&SPA_RECTANGLE(1920, 1080), &SPA_RECTANGLE(1, 1), &SPA_RECTANGLE(8192, 8192)), 0);
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_framerate,
		SPA_POD_CHOICE_RANGE_Fraction(&SPA_FRACTION(60, 1), &SPA_FRACTION(0, 1), &SPA_FRACTION(240, 1)), 0);
	return spa_pod_builder_pop(b, &f0);
}

/* egl_supports_mod: is mod in our EGL-supported set for fmt? */
static int egl_supports_mod(struct rpw_capture *c, enum spa_video_format fmt, uint64_t mod) {
	for (size_t i = 0; i < N_OFFER; i++) {
		if (OFFER_FORMATS[i] != fmt)
			continue;
		for (int j = 0; j < c->n_mods[i]; j++)
			if (c->mods[i][j] == mod)
				return 1;
	}
	return 0;
}

static void request_buffers(struct rpw_capture *c, int dmabuf) {
	uint8_t bb[512];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(bb, sizeof(bb));
	const struct spa_pod *params[1];
	int types = dmabuf ? (1 << SPA_DATA_DmaBuf)
	                   : ((1 << SPA_DATA_MemFd) | (1 << SPA_DATA_MemPtr));
	params[0] = spa_pod_builder_add_object(&b,
		SPA_TYPE_OBJECT_ParamBuffers, SPA_PARAM_Buffers,
		SPA_PARAM_BUFFERS_buffers, SPA_POD_CHOICE_RANGE_Int(8, 2, 16),
		SPA_PARAM_BUFFERS_dataType, SPA_POD_CHOICE_FLAGS_Int(types));
	PW.stream_update_params(c->stream, params, 1);
}

static void on_param_changed(void *data, uint32_t id, const struct spa_pod *param) {
	struct rpw_capture *c = data;
	if (param == NULL || id != SPA_PARAM_Format)
		return;
	uint32_t mt, mst;
	if (spa_format_parse(param, &mt, &mst) < 0 || mt != SPA_MEDIA_TYPE_video || mst != SPA_MEDIA_SUBTYPE_raw)
		return;
	struct spa_video_info_raw raw;
	spa_zero(raw);
	if (spa_format_video_raw_parse(param, &raw) < 0)
		return;

	/* dmabuf modifier fixation dance */
	const struct spa_pod_prop *pmod = spa_pod_find_prop(param, NULL, SPA_FORMAT_VIDEO_modifier);
	if (pmod && c->egl_ready && (pmod->flags & SPA_POD_PROP_FLAG_DONT_FIXATE)) {
		/* server still offers a modifier CHOICE - pick one we support (prefer non-linear) + fixate */
		uint32_t nvals = 0, ch = 0;
		struct spa_pod *vals = spa_pod_get_values(&pmod->value, &nvals, &ch);
		uint64_t *mods = SPA_POD_BODY(vals);
		/* Prefer the first TILED (non-LINEAR) modifier the producer+EGL share. libfunnel's LINEAR is
		 * mislabelled (reports modifier 0 but the buffer is not actually linear -> garbage readback),
		 * and importing a tiled buffer's plane-0 without its metadata gives garbage too. The native
		 * tiled modifier imports correctly when it is single-plane (1080p here). It becomes
		 * multi-plane (DCC metadata) at 4K, which mesa's eglCreateImageKHR rejects here
		 * (EGL_BAD_MATCH) - a documented 4K limitation (see devnote; zero-copy dmabuf->VAAPI is the
		 * fix). LINEAR kept only as a last resort. */
		/* Prefer the first tiled (non-LINEAR) modifier the producer+EGL share - this is the
		 * producer's native modifier (0x...a6bf04 on radeonsi GFX11), which VA's DRM_PRIME_2 import
		 * accepts (incl. its DCC metadata plane). LINEAR last (mislabelled by libfunnel). */
		uint64_t pick = DRM_FORMAT_MOD_INVALID, firsttiled = DRM_FORMAT_MOD_INVALID;
		int prefer_linear = getenv("RPW_PREFER_LINEAR") != NULL;
		for (uint32_t i = 0; i < nvals; i++) {
			uint64_t m = mods[i];
			if (!egl_supports_mod(c, raw.format, m))
				continue;
			if (m == DRM_FORMAT_MOD_LINEAR) {
				if (prefer_linear) {
					pick = m;
					break;
				}
				continue;
			}
			if (firsttiled == DRM_FORMAT_MOD_INVALID)
				firsttiled = m;
			if (!prefer_linear) {
				pick = m;
				break;
			}
		}
		if (pick == DRM_FORMAT_MOD_INVALID)
			pick = firsttiled;
		if (pick == DRM_FORMAT_MOD_INVALID) {
			DBG("fixate: no common modifier for fmt %d\n", raw.format);
			return;
		}
		uint8_t fb[256];
		struct spa_pod_builder b = SPA_POD_BUILDER_INIT(fb, sizeof(fb));
		const struct spa_pod *params[1] = {fixate_format(&b, raw.format, pick)};
		DBG("fixate: picked mod 0x%llx for fmt %d\n", (unsigned long long)pick, raw.format);
		PW.stream_update_params(c->stream, params, 1);
		return;
	}

	pthread_mutex_lock(&c->mu);
	c->width = raw.size.width;
	c->height = raw.size.height;
	c->fmt = raw.format;
	c->modifier = pmod ? raw.modifier : DRM_FORMAT_MOD_INVALID;
	c->is_dmabuf = (pmod != NULL);
	pthread_mutex_unlock(&c->mu);
	DBG("format: %dx%d fmt=%d dmabuf=%d mod=0x%llx\n", c->width, c->height, c->fmt, c->is_dmabuf, (unsigned long long)c->modifier);
	request_buffers(c, c->is_dmabuf);
}

static void on_process(void *data) {
	struct rpw_capture *c = data;
	struct pw_buffer *pb, *last = NULL;
	while ((pb = PW.stream_dequeue(c->stream)) != NULL) {
		if (last) {
			PW.stream_queue(c->stream, last);
			c->dropped++;
		}
		last = pb;
	}
	if (!last)
		return;
	struct spa_buffer *sb = last->buffer;
	if (sb->n_datas >= 1) {
		struct spa_data *d = &sb->datas[0];
		if (d->type == SPA_DATA_DmaBuf) {
			if (!c->va_probed && getenv("RPW_VA_PROBE")) { /* one-shot go/no-go for the VA import path */
				c->va_probed = 1;
				const char *cnode = getenv("RPW_EGL_DEVICE");
				rva_log_enc_caps(cnode && *cnode ? cnode : "/dev/dri/renderD128");
				int np = sb->n_datas < RPW_MAX_PLANES ? (int)sb->n_datas : RPW_MAX_PLANES;
				int fds[RPW_MAX_PLANES];
				unsigned offs[RPW_MAX_PLANES], strs[RPW_MAX_PLANES];
				for (int p = 0; p < np; p++) {
					fds[p] = sb->datas[p].fd;
					offs[p] = sb->datas[p].chunk ? sb->datas[p].chunk->offset : 0;
					strs[p] = sb->datas[p].chunk ? sb->datas[p].chunk->stride : 0;
				}
				const char *node = getenv("RPW_EGL_DEVICE");
				if (!node || !*node)
					node = "/dev/dri/renderD128";
				unsigned char cn[12];
				memset(cn, 0, sizeof(cn));
				int r = rva_import_probe(node, spa_to_drm_fourcc(c->fmt), c->modifier, c->width, c->height, np, fds, offs, strs, cn);
				fprintf(stderr, "RVA_PROBE rc=%d planes=%d mod=0x%llx %dx%d TL=%d,%d,%d TR=%d,%d,%d BL=%d,%d,%d BR=%d,%d,%d\n",
					r, np, (unsigned long long)c->modifier, c->width, c->height,
					cn[0], cn[1], cn[2], cn[3], cn[4], cn[5], cn[6], cn[7], cn[8], cn[9], cn[10], cn[11]);
			}
			/* vaapi-native encode mode: fused dmabuf -> VA import -> VPP NV12 -> H.264/HEVC AU.
			 * Set via rpw_set_encode (production) or RPW_VA_ENC (low-level harness). */
			if (c->enc_mode || getenv("RPW_VA_ENC")) {
				if (!c->enc && !c->enc_fail) {
					const char *en = c->enc_node[0] ? c->enc_node : getenv("RPW_EGL_DEVICE");
					if (!en || !*en)
						en = "/dev/dri/renderD128";
					int kbps = c->enc_kbps > 0 ? c->enc_kbps : 20000;
					const char *kb = getenv("RPW_ENC_KBPS");
					if (kb && c->enc_kbps <= 0)
						kbps = atoi(kb);
					int fps = c->enc_fps > 0 ? c->enc_fps : 60;
					int ow = c->width, oh = c->height;
					if (c->enc_maxh > 0 && oh > c->enc_maxh) { /* VPP downscale of the CODED size only */
						ow = ow * c->enc_maxh / oh;
						oh = c->enc_maxh;
					}
					ow &= ~1; oh &= ~1;
					c->enc = rva_enc_new(en, c->enc_codec, ow, oh, fps, kbps, c->enc_quality_qp);
					if (!c->enc) {
						c->enc_fail = 1;
					} else {
						c->coded_w = ow;
						c->coded_h = oh;
					}
				}
				if (c->enc) {
					pthread_mutex_lock(&c->mu);
					int idr = c->force_idr; c->force_idr = 0;
					int newkbps = c->pending_kbps; c->pending_kbps = 0;
					int newmaxh = c->pending_maxh; c->pending_maxh = -1;
					int newqual = c->pending_quality; c->pending_quality = 0;
					pthread_mutex_unlock(&c->mu);
					if (newkbps > 0)
						rva_enc_set_bitrate(c->enc, newkbps);
					/* mode_switch = a legacy(CBR) encoder asked to go constant-quality. The RC mode is
					 * fixed at config creation, so that needs a recreate; a quality->quality QP change is
					 * applied live by rva_enc_set_quality (no reopen). */
					int mode_switch = 0;
					if (newqual > 0) {
						if (c->enc_quality_qp > 0)
							rva_enc_set_quality(c->enc, newqual); /* already CQP: live QP change */
						else
							mode_switch = 1;                      /* legacy -> quality: recreate below */
						c->enc_quality_qp = newqual;
					}
					/* recreate on a coded-size change OR a legacy->quality mode switch (both rare). */
					if (newmaxh >= 0 || mode_switch) {
						int want_maxh = newmaxh >= 0 ? newmaxh : c->enc_maxh;
						int ow = c->width, oh = c->height;
						if (want_maxh > 0 && oh > want_maxh) {
							ow = ow * want_maxh / oh;
							oh = want_maxh;
						}
						ow &= ~1; oh &= ~1;
						if (oh != c->coded_h || ow != c->coded_w || mode_switch) {
							const char *en = c->enc_node[0] ? c->enc_node : "/dev/dri/renderD128";
							struct rva_enc *ne = rva_enc_new(en, c->enc_codec, ow, oh,
								c->enc_fps > 0 ? c->enc_fps : 60, c->enc_kbps > 0 ? c->enc_kbps : 20000,
								c->enc_quality_qp);
							if (ne) { /* nominal source geometry unchanged; coded size shrinks, receiver upscales */
								rva_enc_free(c->enc);
								c->enc = ne;
								c->coded_w = ow;
								c->coded_h = oh;
								c->enc_maxh = want_maxh;
							}
						}
					}
					c->cap_frames++;
					int np = sb->n_datas < RPW_MAX_PLANES ? (int)sb->n_datas : RPW_MAX_PLANES;
					int efds[RPW_MAX_PLANES];
					unsigned eoff[RPW_MAX_PLANES], estr[RPW_MAX_PLANES];
					for (int p = 0; p < np; p++) {
						efds[p] = sb->datas[p].fd;
						eoff[p] = sb->datas[p].chunk ? sb->datas[p].chunk->offset : 0;
						estr[p] = sb->datas[p].chunk ? sb->datas[p].chunk->stride : 0;
					}
					unsigned char *au = NULL;
					int aulen = 0, kf = 0;
					int flip = want_flip(c, sb);
					long long pts = buffer_pts(sb);
					if (rva_enc_frame(c->enc, spa_to_drm_fourcc(c->fmt), c->modifier, c->width, c->height,
						np, efds, eoff, estr, flip, idr, &au, &aulen, &kf) == 0 && aulen > 0) {
						c->import_ok = 1;
						/* snapshot the quality feedback on the loop thread (the getter reads these scalars,
						 * never c->enc, so a recreate on another path cannot cause a use-after-free). */
						rva_enc_feedback(c->enc, &c->fb_present, &c->fb_avg_qp, &c->fb_rate_limited);
						/* lock-free hand-off to the Go consumer; the data path takes no mutex. */
						if (au_ring_push(c, au, aulen, kf, pts)) {
							c->force_idr = 1; /* overflow dropped a frame: resync with an IDR next */
						} else {
							/* wake a blocked consumer (bare signal, no data copy under the lock). */
							pthread_mutex_lock(&c->mu);
							pthread_cond_signal(&c->cv);
							pthread_mutex_unlock(&c->mu);
						}
					} else {
						c->dropped++;
						c->enc_drops++;
					}
					PW.stream_queue(c->stream, last);
					return;
				}
			}
			if (!egl_import_frame(c, sb)) {
				c->dmabuf_skipped++;
				c->dropped++;
			}
		} else if (d->data && d->chunk && d->chunk->size > 0 && c->width > 0) {
			uint32_t stride = d->chunk->stride ? d->chunk->stride : (uint32_t)(c->width * 4);
			store_shm(c, (const unsigned char *)d->data + d->chunk->offset, (int)stride);
		} else {
			c->dropped++;
		}
	}
	PW.stream_queue(c->stream, last);
}

static void on_state_changed(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
	(void)old;
	struct rpw_capture *c = data;
	DBG("state: %d%s%s\n", state, error ? " err=" : "", error ? error : "");
	/* ERROR means the producer vanished mid-stream (VRChat/producer restart) and AUTOCONNECT does
	 * NOT recover from ERROR - flag a reconnect; rpw_wait_frame (Go thread) re-arms the stream. */
	if (state == PW_STREAM_STATE_ERROR) {
		pthread_mutex_lock(&c->mu);
		c->need_reconnect = 1;
		pthread_cond_signal(&c->cv);
		pthread_mutex_unlock(&c->mu);
	}
}

static const struct pw_stream_events stream_events = {
	.version = PW_VERSION_STREAM_EVENTS,
	.state_changed = on_state_changed,
	.param_changed = on_param_changed,
	.process = on_process,
};

/* do_reconnect re-arms the stream after an ERROR (producer restart). Disconnect + reconnect with
 * fresh EnumFormat params under the thread-loop lock (safe from the Go thread). AUTOCONNECT then
 * re-binds to the target node once the new producer appears. */
static void do_reconnect(struct rpw_capture *c) {
	PW.tl_lock(c->loop);
	PW.stream_disconnect(c->stream);
	uint8_t fb[4096];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(fb, sizeof(fb));
	const struct spa_pod *params[N_OFFER + 1];
	uint32_t np = build_enum_formats(&b, c, params, N_OFFER + 1);
	PW.stream_connect(c->stream, PW_DIRECTION_INPUT, PW_ID_ANY,
		PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS, params, np);
	PW.tl_unlock(c->loop);
	c->reconnects++;
	DBG("reconnect #%ld\n", c->reconnects);
}

struct rpw_capture *rpw_start(const char *target_node) {
	if (!rpw_available())
		return NULL;
	struct rpw_capture *c = calloc(1, sizeof(*c));
	if (!c)
		return NULL;
	pthread_mutex_init(&c->mu, NULL);
	pthread_cond_init(&c->cv, NULL);
	c->modifier = DRM_FORMAT_MOD_INVALID;
	c->drm_fd = -1;
	c->flip_default = getenv("RPW_FLIP_V") != NULL; /* default no-flip (Spout2PW/DX, PipeWire convention) */
	c->pending_maxh = -1;                           /* -1 = no live coded-height cap pending */

	const char *rn = getenv("RPW_EGL_DEVICE");
	egl_setup_display(c, rn && *rn ? rn : "/dev/dri/renderD128");

	c->loop = PW.tl_new("rave-mate-pwcapture", NULL);
	if (!c->loop)
		goto fail;
	PW.tl_lock(c->loop);
	c->context = PW.ctx_new(PW.tl_get_loop(c->loop), NULL, 0);
	if (!c->context)
		goto fail_unlock;
	c->core = PW.ctx_connect(c->context, NULL, 0);
	if (!c->core)
		goto fail_unlock;

	/* NO PW_KEY_MEDIA_ROLE="Camera": the role makes wireplumber's camera-device policy auto-link this
	 * input to a default V4L2 webcam, hijacking the link when PW_KEY_TARGET_OBJECT doesn't match (the
	 * "no more input formats" churn). We bind by exact node.name via TARGET_OBJECT + AUTOCONNECT; no
	 * role means the policy honours the target instead of substituting a camera. */
	struct pw_properties *props = PW.props_new(
		PW_KEY_MEDIA_TYPE, "Video", PW_KEY_MEDIA_CATEGORY, "Capture",
		PW_KEY_TARGET_OBJECT, target_node, NULL);
	c->stream = PW.stream_new(c->core, "rave-mate-capture", props);
	if (!c->stream)
		goto fail_unlock;
	PW.stream_add_listener(c->stream, &c->listener, &stream_events, c);

	uint8_t fb[4096];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(fb, sizeof(fb));
	const struct spa_pod *params[N_OFFER + 1];
	uint32_t np = build_enum_formats(&b, c, params, N_OFFER + 1);
	if (PW.stream_connect(c->stream, PW_DIRECTION_INPUT, PW_ID_ANY,
		PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS, params, np) < 0)
		goto fail_unlock;
	PW.tl_unlock(c->loop);

	if (PW.tl_start(c->loop) < 0)
		goto fail;
	return c;

fail_unlock:
	PW.tl_unlock(c->loop);
fail:
	rpw_stop(c);
	return NULL;
}

int rpw_wait_frame(struct rpw_capture *c, unsigned char *dst, int cap, int *w, int *h, long long *pts, long timeout_ms) {
	struct timespec ts;
	clock_gettime(CLOCK_REALTIME, &ts);
	ts.tv_sec += timeout_ms / 1000;
	ts.tv_nsec += (timeout_ms % 1000) * 1000000L;
	if (ts.tv_nsec >= 1000000000L) {
		ts.tv_sec++;
		ts.tv_nsec -= 1000000000L;
	}
	pthread_mutex_lock(&c->mu);
	if (c->need_reconnect && !c->has_frame) {
		c->need_reconnect = 0;
		pthread_mutex_unlock(&c->mu);
		do_reconnect(c); /* Go thread: safe to lock the pw thread-loop */
		return 0;        /* treated as a timeout by Next -> keeps the route alive, retries */
	}
	while (!c->has_frame && !c->fatal && !c->need_reconnect) {
		if (pthread_cond_timedwait(&c->cv, &c->mu, &ts) != 0)
			break;
	}
	if (c->need_reconnect && !c->has_frame) {
		c->need_reconnect = 0;
		pthread_mutex_unlock(&c->mu);
		do_reconnect(c);
		return 0;
	}
	if (c->fatal && !c->has_frame) {
		pthread_mutex_unlock(&c->mu);
		return -1;
	}
	if (!c->has_frame) {
		pthread_mutex_unlock(&c->mu);
		return 0;
	}
	int need = c->slot_len > 0 ? c->slot_len : c->width * c->height * 4; /* encoded AU vs RGBA */
	*w = c->width;
	*h = c->height;
	if (pts)
		*pts = c->slot_pts;
	if (need > cap) {
		pthread_mutex_unlock(&c->mu);
		return -2;
	}
	memcpy(dst, c->slot, need);
	c->has_frame = 0;
	pthread_mutex_unlock(&c->mu);
	return need;
}

long rpw_dropped(struct rpw_capture *c) { return c ? c->dropped : 0; }
long rpw_dmabuf_skipped(struct rpw_capture *c) { return c ? c->dmabuf_skipped : 0; }

/* rpw_set_encode switches the capture to fused dmabuf->VA encode mode (codec 0=H.264, 1=HEVC).
 * Must be called before any AU is consumed; the encoder is created lazily on the first dmabuf frame
 * at the coded size (source height capped by max_h). Returns 1 when VA is usable (caller commits to
 * the AU path), 0 when VA is unavailable (caller falls back to the readback + ffmpeg path). */
int rpw_set_encode(struct rpw_capture *c, int codec, int kbps, int max_h, int fps, const char *node, int quality_qp) {
	if (!c || !rva_available())
		return 0;
	pthread_mutex_lock(&c->mu);
	c->enc_codec = codec;
	c->enc_kbps = kbps;
	c->enc_maxh = max_h;
	c->enc_quality_qp = quality_qp > 0 ? quality_qp : 0;
	c->enc_fps = fps > 0 ? fps : 60;
	if (node && *node) {
		snprintf(c->enc_node, sizeof(c->enc_node), "%s", node);
	}
	c->enc_mode = 1;
	c->has_frame = 0; /* discard any readback frame captured before the mode switch */
	c->slot_len = 0;
	au_ring_init(c); /* preallocate + empty the AU ring */
	pthread_mutex_unlock(&c->mu);
	return 1;
}

/* rpw_wait_au is rpw_wait_frame for the encode path: it returns one Annex-B AU (H.264/HEVC), sets
 * *keyframe, and reports the CODED dims in *w/*h (<= source; VPP downscaled). Same return contract:
 * >0 bytes, 0 timeout (incl. mid-reconnect), -1 fatal, -2 cap too small (dims set, AU kept).
 *
 * Lock-free DATA path: the AU copy-out is a bare acquire-load of the ring tail + memcpy, no mutex
 * (the memcpy never runs under a lock - the single-slot's main cost). The mutex + cond are used only
 * to BLOCK when the ring is empty (immediate wakeup: the producer signals after its lock-free enqueue;
 * the critical section is a bare signal, no data copy), so the consumer does not poll and the cadence
 * stays tight. */
static int au_ring_pop(struct rpw_capture *c, unsigned char *dst, int cap, int *w, int *h, int *keyframe, long long *pts) {
	unsigned hh = atomic_load_explicit(&c->ring_head, memory_order_relaxed);
	unsigned tt = atomic_load_explicit(&c->ring_tail, memory_order_acquire);
	if (tt == hh)
		return -3; /* empty */
	struct au_slot *s = &c->ring[hh % AU_RING_N]; /* producer won't reuse ring[hh] until head advances */
	*w = s->w > 0 ? s->w : c->width;
	*h = s->h > 0 ? s->h : c->height;
	*keyframe = s->keyframe;
	if (pts)
		*pts = s->pts;
	if (s->len > cap)
		return -2; /* caller grows dst + retries; head NOT advanced */
	int n = s->len;
	memcpy(dst, s->buf, n);
	atomic_fetch_sub_explicit(&c->ring_bytes, n, memory_order_relaxed);
	atomic_store_explicit(&c->ring_head, hh + 1, memory_order_release); /* free the slot */
	return n;
}

int rpw_wait_au(struct rpw_capture *c, unsigned char *dst, int cap, int *w, int *h, int *keyframe, long long *pts, long timeout_ms) {
	struct timespec ts;
	clock_gettime(CLOCK_REALTIME, &ts);
	ts.tv_sec += timeout_ms / 1000;
	ts.tv_nsec += (timeout_ms % 1000) * 1000000L;
	if (ts.tv_nsec >= 1000000000L) {
		ts.tv_sec++;
		ts.tv_nsec -= 1000000000L;
	}
	for (;;) {
		int n = au_ring_pop(c, dst, cap, w, h, keyframe, pts); /* lock-free fast path */
		if (n != -3)
			return n; /* >0 AU, or -2 cap-too-small */
		/* ring empty: block on the cond until the producer enqueues or a flag flips. Re-check the ring
		 * UNDER the lock (the producer signals under the lock) so a wakeup can't be missed. */
		pthread_mutex_lock(&c->mu);
		if (atomic_load_explicit(&c->ring_tail, memory_order_acquire) != atomic_load_explicit(&c->ring_head, memory_order_relaxed)) {
			pthread_mutex_unlock(&c->mu);
			continue; /* producer raced in: dequeue it */
		}
		if (c->need_reconnect) {
			c->need_reconnect = 0;
			pthread_mutex_unlock(&c->mu);
			do_reconnect(c);
			return 0;
		}
		if (c->fatal) {
			pthread_mutex_unlock(&c->mu);
			return -1;
		}
		int rc = pthread_cond_timedwait(&c->cv, &c->mu, &ts);
		pthread_mutex_unlock(&c->mu);
		if (rc != 0) { /* timeout: one last lock-free check, else give up */
			n = au_ring_pop(c, dst, cap, w, h, keyframe, pts);
			return n == -3 ? 0 : n;
		}
	}
}

/* rpw_request_keyframe forces an IDR on the next encoded frame (PLI / route start / reconnect). */
void rpw_request_keyframe(struct rpw_capture *c) {
	if (!c)
		return;
	pthread_mutex_lock(&c->mu);
	c->force_idr = 1;
	pthread_mutex_unlock(&c->mu);
}

/* rpw_set_bitrate updates the live target bitrate (kbps); applied on the next encoded frame. */
void rpw_set_bitrate(struct rpw_capture *c, int kbps) {
	if (!c || kbps <= 0)
		return;
	pthread_mutex_lock(&c->mu);
	c->pending_kbps = kbps;
	pthread_mutex_unlock(&c->mu);
}

/* rpw_set_quality updates the live constant-quality target QP; applied on the next encoded frame. */
void rpw_set_quality(struct rpw_capture *c, int target_qp) {
	if (!c || target_qp <= 0)
		return;
	pthread_mutex_lock(&c->mu);
	c->pending_quality = target_qp;
	pthread_mutex_unlock(&c->mu);
}

/* rpw_enc_feedback reports the adaptive-budget quality signal (scalar snapshot written on the loop
 * thread by on_process; no c->enc deref, so it is safe against a concurrent encoder recreate). */
void rpw_enc_feedback(struct rpw_capture *c, int *present, double *avg_qp, int *rate_limited) {
	if (present)
		*present = c ? c->fb_present : 0;
	if (avg_qp)
		*avg_qp = c ? c->fb_avg_qp : 0.0;
	if (rate_limited)
		*rate_limited = c ? c->fb_rate_limited : 0;
}

/* rpw_set_cap_height applies a live CODED-height cap (0 = native): the encoder is re-created at the
 * capped coded size on the next frame if it changed. The nominal source geometry is unchanged - the
 * receiver upscales the smaller coded frame back to nominal. */
void rpw_set_cap_height(struct rpw_capture *c, int max_h) {
	if (!c || max_h < 0)
		return;
	pthread_mutex_lock(&c->mu);
	c->pending_maxh = max_h;
	pthread_mutex_unlock(&c->mu);
}

/* encode telemetry getters (read from the Go telemetry goroutine). */
long rpw_enc_aus(struct rpw_capture *c) { return c ? c->enc_aus : 0; }
long rpw_enc_drops(struct rpw_capture *c) { return c ? c->enc_drops : 0; }
long rpw_cap_frames(struct rpw_capture *c) { return c ? c->cap_frames : 0; }
long rpw_reconnects(struct rpw_capture *c) { return c ? c->reconnects : 0; }
int rpw_import_ok(struct rpw_capture *c) { return c ? c->import_ok : 0; }
int rpw_coded_w(struct rpw_capture *c) { return c ? c->coded_w : 0; }
int rpw_coded_h(struct rpw_capture *c) { return c ? c->coded_h : 0; }
/* AU-ring telemetry: current fill, high-water, and overflow drops (consumer-behind-the-ring). */
int rpw_ring_depth(struct rpw_capture *c) {
	if (!c)
		return 0;
	unsigned t = atomic_load_explicit(&c->ring_tail, memory_order_relaxed);
	unsigned h = atomic_load_explicit(&c->ring_head, memory_order_relaxed);
	return (int)(t - h);
}
int rpw_ring_hiwater(struct rpw_capture *c) { return c ? c->ring_hiwater : 0; }
long rpw_ring_overflow(struct rpw_capture *c) { return c ? c->ring_overflow : 0; }

void rpw_stop(struct rpw_capture *c) {
	if (!c)
		return;
	if (c->loop)
		PW.tl_stop(c->loop);
	if (c->stream) {
		PW.stream_disconnect(c->stream);
		PW.stream_destroy(c->stream);
	}
	if (c->core)
		PW.core_disconnect(c->core);
	if (c->context)
		PW.ctx_destroy(c->context);
	if (c->loop)
		PW.tl_destroy(c->loop);
	if (c->enc) /* loop stopped: no more on_process, safe to tear down the VA encode session */
		rva_enc_free(c->enc);
	au_ring_free(c); /* loop stopped: no more producer; release the ring buffers */
	/* EGL teardown: the context was made current on the loop thread, now stopped. Best-effort. */
	if (c->egl_ready) {
		if (c->egl_ctx)
			E.DestroyContext(c->dpy, c->egl_ctx);
		if (c->dpy)
			E.Terminate(c->dpy);
		if (c->gbm)
			E.gbm_device_destroy(c->gbm);
		if (c->drm_fd >= 0)
			close(c->drm_fd);
	}
	free(c->rbscratch);
	free(c->slot);
	pthread_mutex_destroy(&c->mu);
	pthread_cond_destroy(&c->cv);
	free(c);
}
