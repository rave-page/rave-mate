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
#include <ravemedia.h> /* native realtime TX (zigmedia C ABI); -I set in capture_pipewire.go cgo CFLAGS */

#include <dlfcn.h>
#include <fcntl.h>
#include <netdb.h>
#include <pthread.h>
#include <sched.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <sys/resource.h>
#include <sys/socket.h>
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

/* Lock-free SPSC AU ring (encode path): the encode thread (producer) hands encoded AUs to the Go
 * consumer (rpw_wait_au) without a mutex on the per-frame path, so a jittery/stalled downstream
 * (packetize/pace/send, a receiver hiccup blocking TCP) is absorbed. Bounded by COUNT (AU_RING_N =
 * 200 ms at 60 fps: rides through the 20-140 ms receiver stalls measured on the rig) AND BYTES
 * (AU_RING_MAXBYTES). Slots start at AU_RING_SLOT_INIT and only realloc on a rare larger AU. Overflow
 * policy: the encode thread checks for room BEFORE encoding and SKIPS the frame when full (pre-encode:
 * the frame never enters the encoder, so the reference chain stays intact - no IDR, no keyframe storm;
 * counted as ring_skips). Only a post-encode byte-cap miss drops an AU, which forces ONE IDR (no
 * further forcing while that IDR is outstanding). */
#define AU_RING_N 12
#define AU_RING_SLOT_INIT (512 * 1024)       /* 512 KB/slot start (a 4K P ~100 KB); grows rarely */
#define AU_RING_MAXBYTES (32 * 1024 * 1024)  /* byte ceiling on queued AUs */
#define AU_RING_HEADROOM (4 * 1024 * 1024)   /* pre-encode room check: > any single AU */

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
static pthread_mutex_t egl_load_mu = PTHREAD_MUTEX_INITIALIZER;

static int egl_load_locked(void);

/* egl_load: one-time dlopen, serialized. Unguarded, a capture started concurrently with another
 * (daemon restart = every route at once) read the in-progress 0 as "no EGL", built a SHM-only offer,
 * and a dmabuf-only producer then failed every link ("no more input formats") forever. */
static int egl_load(void) {
	pthread_mutex_lock(&egl_load_mu);
	if (egl_loaded == -1)
		egl_loaded = egl_load_locked();
	int r = egl_loaded;
	pthread_mutex_unlock(&egl_load_mu);
	return r;
}

static int egl_load_locked(void) {
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

/* ---- native per-AU interval histogram (lock-free; single writer = the encode thread) ---------------
 * Same bucket bounds as medialink.stageBucketsUpperMs (framestages.go) so the native enc/tx stages read
 * exactly like the Go-pump stages in `ctl routes`. Marked on the encode thread (relaxed atomics, no
 * lock, no alloc); rpw_hist_snap computes the percentiles at Go's <= 1 Hz sample, off the frame path. */
#define RPW_HIST_NB 32
static const double rpw_hist_ub_ms[RPW_HIST_NB] = {
	4, 8,
	10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30,
	33, 40, 50, 75, 100, 150, 200, 500, 1e12};

struct rpw_hist {
	_Atomic long long last; /* mono ns of the previous mark (0 = none) */
	_Atomic unsigned long long n, sum_ns, max_ns, o25, o50, o100;
	_Atomic unsigned long long b[RPW_HIST_NB];
};

static void rpw_hist_mark(struct rpw_hist *h, long long now) {
	long long prev = atomic_exchange_explicit(&h->last, now, memory_order_relaxed);
	if (prev == 0 || now <= prev)
		return;
	unsigned long long d = (unsigned long long)(now - prev);
	atomic_fetch_add_explicit(&h->n, 1, memory_order_relaxed);
	atomic_fetch_add_explicit(&h->sum_ns, d, memory_order_relaxed);
	if (d > atomic_load_explicit(&h->max_ns, memory_order_relaxed)) /* single writer: plain max */
		atomic_store_explicit(&h->max_ns, d, memory_order_relaxed);
	double ms = (double)d / 1e6;
	if (ms > 25)
		atomic_fetch_add_explicit(&h->o25, 1, memory_order_relaxed);
	if (ms > 50)
		atomic_fetch_add_explicit(&h->o50, 1, memory_order_relaxed);
	if (ms > 100)
		atomic_fetch_add_explicit(&h->o100, 1, memory_order_relaxed);
	for (int i = 0; i < RPW_HIST_NB; i++) {
		if (ms <= rpw_hist_ub_ms[i]) {
			atomic_fetch_add_explicit(&h->b[i], 1, memory_order_relaxed);
			break;
		}
	}
}

/* rpw_hist_pct: upper bound of the bucket holding quantile q (bucket-approximated, like StageHist). */
static double rpw_hist_pct(struct rpw_hist *h, unsigned long long n, double q) {
	unsigned long long target = (unsigned long long)(q * (double)n), cum = 0;
	for (int i = 0; i < RPW_HIST_NB; i++) {
		cum += atomic_load_explicit(&h->b[i], memory_order_relaxed);
		if (cum >= target)
			return rpw_hist_ub_ms[i];
	}
	return rpw_hist_ub_ms[RPW_HIST_NB - 1];
}

static void rpw_hist_snap(struct rpw_hist *h, rpw_stage_snap *o) {
	memset(o, 0, sizeof(*o));
	unsigned long long n = atomic_load_explicit(&h->n, memory_order_relaxed);
	o->count = n;
	o->max_ms = (double)atomic_load_explicit(&h->max_ns, memory_order_relaxed) / 1e6;
	o->over25 = atomic_load_explicit(&h->o25, memory_order_relaxed);
	o->over50 = atomic_load_explicit(&h->o50, memory_order_relaxed);
	o->over100 = atomic_load_explicit(&h->o100, memory_order_relaxed);
	if (n == 0)
		return;
	o->mean_ms = (double)atomic_load_explicit(&h->sum_ns, memory_order_relaxed) / 1e6 / (double)n;
	o->p50_ms = rpw_hist_pct(h, n, 0.50);
	o->p99_ms = rpw_hist_pct(h, n, 0.99);
}

/* ---- per-step duration histograms (encode thread writes; Go samples): where a frame's time goes ---- */
#define RPW_UH_NB 22
static const long long rpw_uh_ub_us[RPW_UH_NB] = {250, 500, 750, 1000, 1500, 2000, 3000, 4000, 5000, 6000,
	8000, 10000, 12000, 14000, 16000, 18000, 20000, 25000, 33000, 50000, 100000, 1LL << 62};
struct rpw_uhist {
	_Atomic unsigned long long n, sum_us, max_us, b[RPW_UH_NB];
};
static void rpw_uhist_add(struct rpw_uhist *h, long long us) {
	if (us < 0)
		return;
	atomic_fetch_add_explicit(&h->n, 1, memory_order_relaxed);
	atomic_fetch_add_explicit(&h->sum_us, (unsigned long long)us, memory_order_relaxed);
	if ((unsigned long long)us > atomic_load_explicit(&h->max_us, memory_order_relaxed))
		atomic_store_explicit(&h->max_us, (unsigned long long)us, memory_order_relaxed);
	for (int i = 0; i < RPW_UH_NB; i++)
		if (us <= rpw_uh_ub_us[i]) {
			atomic_fetch_add_explicit(&h->b[i], 1, memory_order_relaxed);
			break;
		}
}
static double rpw_uhist_pct(struct rpw_uhist *h, unsigned long long n, double q) {
	unsigned long long target = (unsigned long long)(q * (double)n), cum = 0;
	for (int i = 0; i < RPW_UH_NB; i++) {
		cum += atomic_load_explicit(&h->b[i], memory_order_relaxed);
		if (cum >= target)
			return (double)rpw_uh_ub_us[i] / 1000.0;
	}
	return (double)rpw_uh_ub_us[RPW_UH_NB - 1] / 1000.0;
}
static void rpw_uhist_snap(struct rpw_uhist *h, rpw_stage_snap *o) {
	memset(o, 0, sizeof(*o));
	unsigned long long n = atomic_load_explicit(&h->n, memory_order_relaxed);
	o->count = n;
	o->max_ms = (double)atomic_load_explicit(&h->max_us, memory_order_relaxed) / 1000.0;
	if (!n)
		return;
	o->mean_ms = (double)atomic_load_explicit(&h->sum_us, memory_order_relaxed) / 1000.0 / (double)n;
	o->p50_ms = rpw_uhist_pct(h, n, 0.50);
	o->p99_ms = rpw_uhist_pct(h, n, 0.99);
}

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
	/* reconnect backoff: a link that errors again without delivering a frame (negotiation failure,
	 * wrong target) doubles the delay 50 ms -> 2 s instead of churning ~40 reconnects/s forever. */
	_Atomic long frames_seen;     /* buffers seen by on_process (loop thread +=) */
	long frames_at_reconnect;     /* frames_seen at the last reconnect (Go thread) */
	int reconnect_backoff_ms;     /* current backoff (Go thread) */
	long long next_reconnect_ns;  /* mono_ns before which no reconnect is issued (Go thread) */

	/* ---- vaapi-native encode mode (production: rpw_set_encode; also RPW_VA_ENC env for the harness) */
	int enc_mode;        /* 0 = readback RGBA; 1 = fused dmabuf->VA encode (AU in slot) */
	int enc_codec;       /* 0 = H.264, 1 = HEVC */
	int enc_kbps;        /* target bitrate budget (CBR target, or the CQP ceiling in quality mode) */
	int enc_maxh;        /* coded-height cap (0 = native); VPP downscales taller sources */
	int enc_fps;
	int cap_fps; /* receiver rate hint MaxFPS (atomic; 0 = none): the PTS grid decimates to min(enc_fps, cap_fps) */
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
	_Atomic int consumer_back;  /* consumer asked for an AU after taking the first one (start-up hold end) */
	int ring_hiwater;           /* max fill seen (producer-only) */
	long ring_overflow;         /* AUs dropped because the consumer fell behind the ring (producer-only) */

	/* B.2 dedicated encode thread: the PipeWire callback (on_process) only grabs the newest buffer +
	 * hands it off here, so it never blocks on vaSyncSurface/vaMapBuffer (which was making the callback
	 * run late under GPU load -> capture-side 60->58 slip). The thread does VPP+encode+sync+emit and
	 * requeues the pw buffer (via pw_thread_loop_lock). Single pending slot = newest-wins at capture
	 * (if the encoder is behind, the callback requeues the stale buffer and keeps the freshest). */
	pthread_t enc_thread;
	int enc_thread_on;             /* thread created */
	int enc_stop;                  /* shutdown signal (set under enc_mu) */
	pthread_mutex_t enc_mu;        /* guards the pending slot */
	pthread_cond_t enc_cv;         /* wakes the encode thread */
	/* capture hand-off FIFO (callback -> encode thread), bounded RPW_HQ_MAX deep (RPW_CAP_QUEUE, default
	 * 3): a TRANSIENT encode spike (GFX contention: VPP 10 ms + sync 13 ms on the live rig) no longer
	 * overwrites a frame - the thread catches up (mean cost ~0.3 ms CPU / ~3.5 ms GPU per 4K frame). Only a
	 * sustained overload overflows: drop the OLDEST (counted as cap_drops). Holds <= depth pw buffers. */
	struct rpw_pend {
		struct pw_buffer *buf;
		int np, fds[RPW_MAX_PLANES];
		unsigned off[RPW_MAX_PLANES], str[RPW_MAX_PLANES];
		uint32_t fourcc;
		uint64_t mod;
		int w, h, flip;
		long long pts;
	} hq[4];
	int hq_head, hq_n, hq_max, hq_hiwater, hq_win_peak;
	/* auto coded-size controller state (encode thread; see ac_tick). rx_maxh = the receiver/router cap. */
	int rx_maxh;
	int ac_on, ac_down_pmille, ac_win_s, ac_up_base_s, ac_up_s, ac_level, ac_nwin, ac_probe, ac_settle;
	long ac_drops[30], ac_arr[30], ac_cap0, ac_drop0;
	long long ac_sec_start, ac_ok_since, ac_last_up, ac_last_down;
	char ac_reason[160]; /* guarded by mu */
	_Atomic int ac_gen;
	/* source-static detector (encode thread; results under mu) */
	int fz_hist[64], fz_hn, fz_med, fz_n_since_med, fz_run, fz_run_repeats;
	long long fz_first_pts, fz_last_pts, fz_run_wall;
	long fz_count;
	long long fz_last_ms, fz_longest_ms, fz_last_wall; /* wall = unix ms at the freeze start */
	int fz_last_delivered, fz_last_repeats;
	int fz_dbg; /* RPW_FZ_DEBUG */
	int fz_prev_len, fz_flat, fz_flat_max;
	struct pw_buffer *enc_pending; /* non-NULL while hq_n > 0 (wait-loop predicate) */
	long cap_drops;                /* buffers dropped at the capture hand-off (encoder behind) */
	/* B.3 PTS-grid frame selection: when the source runs faster than the negotiated rate (VRChat 120 ->
	 * 60), emit the newest frame per 1/fps grid slot by capture PTS, so the output is evenly spaced in
	 * source time (not bunched by the callback's own timing). grid_drops = sub-grid frames paced out
	 * (deliberate rate-cap, PRE-encode -> no reference break, no IDR; surfaced as RateCapped). */
	long long enc_grid_next; /* next grid PTS (ns); 0 = uninitialised */
	long long grid_last_pts, grid_src_iv; /* loop thread: last frame PTS + source interval EMA (ns) */
	long grid_drops;
	int enc_src_w, enc_src_h; /* source dims the encoder was built for (recreate on change) */
	/* robustness (encode thread writes, getters read): */
	_Atomic int streaming;   /* pw stream STREAMING (loop thread): cadence repeats only while live */
	long repeats;            /* cadence AUs emitted without a new source frame (static/slow/hitch) */
	long ring_skips;         /* frames skipped PRE-encode because the AU ring was full (no ref break) */
	long start_holds;        /* frames skipped pre-encode while the route's first AU was unconsumed */
	long forced_idrs;        /* IDRs forced by a post-encode drop (byte cap) - at most one per episode */
	int slow_us;             /* RPW_ENC_SLOW_US test hook: simulated encode cost at native size */
	int enc_sync;            /* RPW_ENC_SYNC: emit every frame synchronously (no 1-frame pipeline) */
	/* native-path telemetry (encode thread writes; Go samples <= 1 Hz): per-AU interval histograms of
	 * the encoder output (enc) and the native-sender hand-off (tx), keyframes, AU bytes. */
	struct rpw_hist h_enc, h_tx;
	struct rpw_uhist h_step[RVA_NSTEP + 1]; /* RVA_ST_* steps + [RVA_NSTEP] = whole real frame */
	_Atomic long long busy_us;              /* encode-thread busy time (headroom: busy/wall) */
	long pts_gaps;                          /* emitted AUs whose PTS jumped > 1.5 period (content skip) */
	long pts_skipped;                       /* source frames those jumps skipped (output-visible loss) */
	long long last_emit_pts;
	long enc_keyframes;
	long long enc_bytes;

	/* Native realtime TX (unit 3): when the router attaches a native sender (UDP route + per-route
	 * keys), the encode thread submits AUs straight to it (rm_tx_submit_au) instead of the AU ring ->
	 * Go pump. NULL = the Go-ring fallback path. Set under enc_mu before the encode thread reads it. */
	rm_sender *native_tx;
	/* pending-frame metadata (captured from the buffer in the callback; the buffer stays valid while
	 * held, but copying keeps the thread off the spa_buffer). */

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

/* mono_ns is a monotonic nanosecond clock (grid fallback when a buffer carries no capture PTS). */
static long long mono_ns(void) {
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return (long long)t.tv_sec * 1000000000LL + t.tv_nsec;
}

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
	atomic_store(&c->consumer_back, 0);
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

/* au_ring_room: 1 when two more AUs fit (one encode step can emit the pipelined previous frame AND
 * flush the current one) incl. byte headroom. Producer-side, lock-free. */
static int au_ring_room(struct rpw_capture *c) {
	unsigned t = atomic_load_explicit(&c->ring_tail, memory_order_relaxed);
	unsigned h = atomic_load_explicit(&c->ring_head, memory_order_acquire);
	long qb = atomic_load_explicit(&c->ring_bytes, memory_order_relaxed);
	return (t - h) + 2 <= AU_RING_N && qb + 2 * AU_RING_HEADROOM <= AU_RING_MAXBYTES;
}

/* au_ring_first_unconsumed: the route's first AU (the IDR) is queued but not yet read. Route start-up:
 * the consumer is still opening (TCP slow start, receiver decoder open); encoding on would only queue
 * stale frames (latency burst) or overflow (drop burst + IDR storm, the live "36 drops in the first
 * second"). Hold - skip pre-encode - until it is read; the next encoded frame is then fresh. */
static int au_ring_first_unconsumed(struct rpw_capture *c) {
	/* "consumed" = the consumer came BACK for the next AU after taking the IDR: the Go pump pops the IDR
	 * then blocks in its first socket write while the receiver opens (live: 390 ms) - releasing the hold
	 * at the pop queued 11 stale AUs and skipped 19 (ringHiwater 11, ringSkips 19 on the live route). */
	return atomic_load_explicit(&c->ring_tail, memory_order_relaxed) > 0 &&
	       !atomic_load_explicit(&c->consumer_back, memory_order_acquire);
}

/* egl_setup_display: open the render node, GBM device, EGL display + GLES context (NOT current),
 * and query dmabuf modifiers per offered format. Runs on the caller thread in rpw_start. */
static void egl_setup_display(struct rpw_capture *c, const char *node) {
	if (getenv("RPW_SHM_ONLY") || !egl_load()) /* RPW_SHM_ONLY: test hook (SHM-only offer) */
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

/* egl_teardown releases whatever egl_setup_display built (also a partial setup). Not with the
 * context current on a live loop thread (callers: rpw_stop after the loop stopped, or a retry when
 * egl_ready == 0, i.e. never made current). */
static void egl_teardown(struct rpw_capture *c) {
	if (E.ok) {
		if (c->egl_ctx)
			E.DestroyContext(c->dpy, c->egl_ctx);
		if (c->dpy)
			E.Terminate(c->dpy);
		if (c->gbm)
			E.gbm_device_destroy(c->gbm);
	}
	if (c->drm_fd >= 0)
		close(c->drm_fd);
	c->egl_ctx = NULL;
	c->dpy = NULL;
	c->gbm = NULL;
	c->drm_fd = -1;
	c->egl_ready = 0;
	c->egl_current = 0;
	memset(c->n_mods, 0, sizeof(c->n_mods));
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

/* hq_flush requeues every queued (not yet taken) hand-off buffer. Loop thread: before the pool is
 * renegotiated/removed (format change, stream leaving STREAMING), so no stale pw_buffer stays queued. */
static void hq_flush(struct rpw_capture *c) {
	pthread_mutex_lock(&c->enc_mu);
	while (c->hq_n > 0) {
		PW.stream_queue(c->stream, c->hq[c->hq_head].buf);
		c->hq_head = (c->hq_head + 1) % 4;
		c->hq_n--;
	}
	c->enc_pending = NULL;
	pthread_mutex_unlock(&c->enc_mu);
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

	hq_flush(c);
	pthread_mutex_lock(&c->mu);
	c->width = raw.size.width;
	c->height = raw.size.height;
	c->fmt = raw.format;
	c->modifier = pmod ? raw.modifier : DRM_FORMAT_MOD_INVALID;
	c->is_dmabuf = (pmod != NULL);
	c->force_idr = 1; /* (re)negotiated = new producer/pool: resume on an IDR */
	pthread_mutex_unlock(&c->mu);
	c->enc_grid_next = 0; /* loop-thread-owned: re-prime the PTS grid (new producer = new PTS origin) */
	c->grid_last_pts = 0;
	c->grid_src_iv = 0;
	DBG("format: %dx%d fmt=%d dmabuf=%d mod=0x%llx\n", c->width, c->height, c->fmt, c->is_dmabuf, (unsigned long long)c->modifier);
	request_buffers(c, c->is_dmabuf);
}

/* enc_ensure creates/recreates the VA encoder to match the current config (lazy create, live bitrate/
 * quality, coded-size cap + legacy->quality recreate). Runs on the encode thread; moved out of the
 * PipeWire callback. Returns the force-IDR flag consumed from the control state. */
static int enc_ensure(struct rpw_capture *c) {
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
		if (c->enc_maxh > 0 && oh > c->enc_maxh) {
			ow = ow * c->enc_maxh / oh;
			oh = c->enc_maxh;
		}
		ow &= ~1;
		oh &= ~1;
		c->enc = rva_enc_new(en, c->enc_codec, ow, oh, fps, kbps, c->enc_quality_qp);
		if (!c->enc) {
			c->enc_fail = 1;
		} else {
			c->coded_w = ow;
			c->coded_h = oh;
			c->enc_src_w = c->width;
			c->enc_src_h = c->height;
		}
	}
	if (c->enc)
		rva_enc_set_fast_scale(c->enc, c->ac_level > 0); /* auto overload step: cheap VPP downscale */
	if (c->enc && (c->width != c->enc_src_w || c->height != c->enc_src_h)) {
		/* producer came back at another size: rebuild at the new coded size (fresh encoder => IDR) */
		rva_enc_free(c->enc);
		c->enc = NULL;
		return enc_ensure(c);
	}
	if (!c->enc)
		return 0;
	pthread_mutex_lock(&c->mu);
	int idr = c->force_idr; c->force_idr = 0;
	int newkbps = c->pending_kbps; c->pending_kbps = 0;
	int newmaxh = c->pending_maxh; c->pending_maxh = -1;
	int newqual = c->pending_quality; c->pending_quality = 0;
	pthread_mutex_unlock(&c->mu);
	if (newkbps > 0)
		rva_enc_set_bitrate(c->enc, newkbps);
	int mode_switch = 0;
	if (newqual > 0) {
		if (c->enc_quality_qp > 0)
			rva_enc_set_quality(c->enc, newqual);
		else
			mode_switch = 1;
		c->enc_quality_qp = newqual;
	}
	if (newmaxh >= 0 || mode_switch) {
		int want_maxh = newmaxh >= 0 ? newmaxh : c->enc_maxh;
		int ow = c->width, oh = c->height;
		if (want_maxh > 0 && oh > want_maxh) {
			ow = ow * want_maxh / oh;
			oh = want_maxh;
		}
		ow &= ~1;
		oh &= ~1;
		if (oh != c->coded_h || ow != c->coded_w || mode_switch) {
			const char *en = c->enc_node[0] ? c->enc_node : "/dev/dri/renderD128";
			struct rva_enc *ne = rva_enc_new(en, c->enc_codec, ow, oh,
				c->enc_fps > 0 ? c->enc_fps : 60, c->enc_kbps > 0 ? c->enc_kbps : 20000,
				c->enc_quality_qp);
			if (ne) {
				rva_enc_free(c->enc);
				c->enc = ne;
				c->coded_w = ow;
				c->coded_h = oh;
				c->enc_src_w = c->width;
				c->enc_src_h = c->height;
				c->enc_maxh = want_maxh;
				rva_enc_set_fast_scale(c->enc, c->ac_level > 0);
			}
		}
	}
	return idr;
}

/* enc_emit_au hands one encoded AU downstream: the native sender (non-blocking; it owns its bounded
 * queue + drop + IDR callback) or the Go AU ring. A post-encode ring miss (byte cap only - room was
 * checked pre-encode) drops the AU and forces ONE IDR. Encode thread. */
/* ---- auto coded-size controller (encode thread; native) ----------------------------------------------
 * When the GPU cannot sustain the encode next to the games, an evenly paced lower CODED size beats 4K
 * with random repeated frames. Signal: capture hand-off drops (the encoder fell behind past the FIFO).
 * Down: drop rate > down_pmille over the last win_s seconds -> next level (h, 5/6 h, 2/3 h, 1/2 h; the
 * nominal geometry is unchanged, the receiver upscales). Up: no drop and the hand-off FIFO never filled
 * for up_s seconds -> one level up. Anti-flap: a step-down within 60 s of a step-up doubles up_s (cap
 * 600 s); 10 min stable resets it. Windows restart after every step (encoder rebuild = IDR transient).
 * Env: RPW_AUTOCAP=0 disables; RPW_AUTOCAP_DOWN_PCT (0.5), RPW_AUTOCAP_WIN_S (5), RPW_AUTOCAP_UP_S (30). */
#define AC_LEVELS 4
#define AC_WIN_MAX 30

static int ac_level_h(int src_h, int level) {
	static const int num[AC_LEVELS] = {6, 5, 4, 3}; /* x/6: 1, 5/6, 2/3, 1/2 */
	if (level <= 0 || src_h <= 0)
		return 0; /* 0 = native */
	return (src_h * num[level] / 6) & ~1;
}

/* ac_apply_cap: effective coded-height cap = the tighter of the receiver's (rx_maxh) and auto's. Under mu. */
static void ac_apply_cap_locked(struct rpw_capture *c) {
	int a = ac_level_h(c->height, c->ac_level), r = c->rx_maxh, eff = r;
	if (a > 0 && (eff <= 0 || a < eff))
		eff = a;
	c->pending_maxh = eff;
}

static void ac_init(struct rpw_capture *c) {
	const char *e = getenv("RPW_AUTOCAP");
	c->ac_on = !(e && !strcmp(e, "0"));
	e = getenv("RPW_AUTOCAP_DOWN_PCT");
	c->ac_down_pmille = e ? (int)(atof(e) * 10 + 0.5) : 5;
	e = getenv("RPW_AUTOCAP_WIN_S");
	c->ac_win_s = e ? atoi(e) : 5;
	if (c->ac_win_s < 1)
		c->ac_win_s = 1;
	if (c->ac_win_s > AC_WIN_MAX)
		c->ac_win_s = AC_WIN_MAX;
	e = getenv("RPW_AUTOCAP_UP_S");
	c->ac_up_base_s = e ? atoi(e) : 30;
	if (c->ac_up_base_s < 5)
		c->ac_up_base_s = 5;
	c->ac_up_s = c->ac_up_base_s;
}

static void ac_reset_windows(struct rpw_capture *c, long long now) {
	memset(c->ac_drops, 0, sizeof(c->ac_drops));
	memset(c->ac_arr, 0, sizeof(c->ac_arr));
	c->ac_nwin = 0;
	c->ac_sec_start = now;
	c->ac_cap0 = c->cap_frames;
	c->ac_drop0 = c->cap_drops;
	c->ac_ok_since = now;
}

static void ac_step(struct rpw_capture *c, long long now, int dir, const char *why) {
	int nl = c->ac_level + dir;
	if (nl < 0 || nl >= AC_LEVELS)
		return;
	c->ac_level = nl;
	int h = ac_level_h(c->height, nl);
	int w = h > 0 && c->height > 0 ? (c->width * h / c->height) & ~1 : c->width;
	pthread_mutex_lock(&c->mu);
	ac_apply_cap_locked(c);
	snprintf(c->ac_reason, sizeof(c->ac_reason), "%s -> coded %dx%d", why, w, h > 0 ? h : c->height);
	pthread_mutex_unlock(&c->mu);
	if (dir < 0) {
		c->ac_last_up = now;
		c->ac_probe = 1; /* judged on a short window until it has held for win_s */
	} else {
		c->ac_probe = 0;
		if (c->ac_last_up && now - c->ac_last_up < 60000000000LL) { /* stepped up into overload: back off */
			c->ac_up_s *= 2;
			if (c->ac_up_s > 600)
				c->ac_up_s = 600;
		}
		c->ac_last_down = now;
	}
	atomic_fetch_add_explicit(&c->ac_gen, 1, memory_order_release);
	ac_reset_windows(c, now);
	c->ac_settle = 2; /* skip the encoder-rebuild transient (IDR, first frames) */
}

/* ac_tick runs once per real frame on the encode thread; evaluates once per second. */
static void ac_tick(struct rpw_capture *c, long long now) {
	if (!c->ac_on || c->height <= 0)
		return;
	if (!c->ac_sec_start) {
		ac_reset_windows(c, now);
		return;
	}
	if (now - c->ac_sec_start < 1000000000LL)
		return;
	long dr = c->cap_drops - c->ac_drop0, cf = c->cap_frames - c->ac_cap0;
	pthread_mutex_lock(&c->enc_mu);
	int peak = c->hq_win_peak;
	c->hq_win_peak = c->hq_n;
	pthread_mutex_unlock(&c->enc_mu);
	if (c->ac_settle > 0) { /* post-step settle second: not judged */
		c->ac_settle--;
		c->ac_sec_start = now;
		c->ac_cap0 = c->cap_frames;
		c->ac_drop0 = c->cap_drops;
		c->ac_ok_since = now;
		return;
	}
	int i = c->ac_nwin % AC_WIN_MAX;
	c->ac_drops[i] = dr;
	c->ac_arr[i] = cf + dr;
	c->ac_nwin++;
	c->ac_sec_start = now;
	c->ac_cap0 = c->cap_frames;
	c->ac_drop0 = c->cap_drops;
	if (dr > 0 || peak >= c->hq_max)
		c->ac_ok_since = now;
	if (c->ac_last_down && now - c->ac_last_down > 600000000000LL)
		c->ac_up_s = c->ac_up_base_s; /* 10 min without a step-down: forget the back-off */
	/* right after a step-up the probe judges on a 2 s window: an up-probe into overload costs ~2 s of
	 * drops, not win_s (the dwell then doubles) */
	int win = (c->ac_probe && c->ac_win_s > 2) ? 2 : c->ac_win_s;
	if (c->ac_probe && c->ac_nwin >= c->ac_win_s)
		c->ac_probe = 0; /* the up-step held a full window: normal judging */
	if (c->ac_nwin >= win) {
		long sd = 0, sa = 0;
		for (int k = 0; k < win; k++) {
			int j = (c->ac_nwin - 1 - k) % AC_WIN_MAX;
			sd += c->ac_drops[j];
			sa += c->ac_arr[j];
		}
		if (sa > 0 && sd * 1000 > (long)c->ac_down_pmille * sa && c->ac_level + 1 < AC_LEVELS) {
			char why[96];
			snprintf(why, sizeof(why), "auto: encoder overload (%.1f%% capture drops over %d s)",
				100.0 * (double)sd / (double)sa, win);
			ac_step(c, now, +1, why);
			return;
		}
	}
	if (c->ac_level > 0 && now - c->ac_ok_since >= (long long)c->ac_up_s * 1000000000LL) {
		char why[96];
		snprintf(why, sizeof(why), "auto: headroom back (no drops for %d s)", c->ac_up_s);
		ac_step(c, now, -1, why);
	}
}

/* ---- source-static ("frozen picture") detector: native, no pixel reads ---------------------------------
 * A source that keeps delivering the SAME picture (VRChat hitching on a world/avatar load while
 * Spout2PW keeps publishing at 60 fps) encodes as a run of near-empty P-frames. A run of >= FZ_MIN_RUN
 * non-key AUs each below max(64, p75/12) bytes (p75 of the last 64 non-key AUs, real
 * frames only) is one freeze event: count, duration (PTS span), wall time, and whether the source
 * DELIVERED frames throughout (every AU from a new buffer = the picture itself froze) or our cadence
 * repeats filled in (the source stopped delivering). Keyframes inside a run are ignored (GOP IDRs). */
#define FZ_MIN_RUN 12
#define FZ_HIST 64

static int fz_median(struct rpw_capture *c) {
	int n = c->fz_hn < FZ_HIST ? c->fz_hn : FZ_HIST, t[FZ_HIST];
	if (n < 16)
		return 0;
	memcpy(t, c->fz_hist, sizeof(int) * n);
	for (int i = 1; i < n; i++) { /* insertion sort, 64 ints, once per 16 AUs */
		int v = t[i], j = i - 1;
		while (j >= 0 && t[j] > v) {
			t[j + 1] = t[j];
			j--;
		}
		t[j + 1] = v;
	}
	return t[n * 3 / 4]; /* p75: a 30 fps source doubled to 60 makes half the AUs near-empty duplicates */
}

static void fz_end_run(struct rpw_capture *c) {
	/* qualifies: >= FZ_MIN_RUN small AUs incl. >= 8 consecutive near-equal sizes (CBR keeps refining a
	 * static picture for a few frames, then emits the same few-KB skip frame; real low motion varies) */
	if (c->fz_run >= FZ_MIN_RUN && c->fz_flat_max >= 8 && c->enc_fps > 0) {
		long long ms = (c->fz_last_pts - c->fz_first_pts) / 1000000 + 1000 / c->enc_fps;
		pthread_mutex_lock(&c->mu);
		c->fz_count++;
		c->fz_last_ms = ms;
		if (ms > c->fz_longest_ms)
			c->fz_longest_ms = ms;
		c->fz_last_wall = c->fz_run_wall;
		c->fz_last_delivered = c->fz_run_repeats == 0;
		c->fz_last_repeats = c->fz_run_repeats;
		pthread_mutex_unlock(&c->mu);
	}
	c->fz_run = 0;
	c->fz_run_repeats = 0;
	c->fz_flat = 0;
	c->fz_flat_max = 0;
}

static void fz_au(struct rpw_capture *c, int aulen, int kf, long long pts, int real) {
	if (kf)
		return;
	if (c->fz_n_since_med++ >= 16) {
		c->fz_med = fz_median(c);
		c->fz_n_since_med = 0;
	}
	int thr = c->fz_med / 12;
	if (thr < 64)
		thr = 64;
	if (c->fz_dbg && aulen < thr * 2)
		fprintf(stderr, "FZDBG len=%d thr=%d p75=%d real=%d run=%d\n", aulen, thr, c->fz_med, real, c->fz_run);
	if (c->fz_med > 0 && aulen < thr) {
		if (c->fz_run++ == 0) {
			struct timespec w;
			clock_gettime(CLOCK_REALTIME, &w);
			c->fz_run_wall = (long long)w.tv_sec * 1000 + w.tv_nsec / 1000000;
			c->fz_first_pts = pts;
		}
		c->fz_last_pts = pts;
		c->fz_run_repeats += !real;
		int tol = aulen / 50 > 32 ? aulen / 50 : 32; /* a frozen picture converges to a FLAT size series */
		int dlen = aulen > c->fz_prev_len ? aulen - c->fz_prev_len : c->fz_prev_len - aulen;
		c->fz_flat = (c->fz_run > 1 && dlen <= tol) ? c->fz_flat + 1 : 0;
		if (c->fz_flat > c->fz_flat_max)
			c->fz_flat_max = c->fz_flat;
		c->fz_prev_len = aulen;
		return;
	}
	fz_end_run(c);
	if (real) { /* median of real content only */
		c->fz_hist[c->fz_hn % FZ_HIST] = aulen;
		c->fz_hn++;
	}
}

/* enc_mark_steps folds the encoder's last-call step timings into the step histograms. */
static void enc_mark_steps(struct rpw_capture *c) {
	long long st[RVA_NSTEP];
	rva_enc_last_steps(c->enc, st);
	for (int i = 0; i < RVA_NSTEP; i++)
		rpw_uhist_add(&c->h_step[i], st[i]);
}

static void enc_emit_au(struct rpw_capture *c, rm_sender *tx, unsigned char *au, int aulen, int kf, long long pts, int real) {
	rpw_hist_mark(&c->h_enc, mono_ns());
	fz_au(c, aulen, kf, pts, real);
	if (c->last_emit_pts > 0 && c->enc_fps > 0) { /* wall cadence hides a skipped source frame; PTS does not */
		long long period = 1000000000LL / c->enc_fps, d = pts - c->last_emit_pts;
		if (d > period * 3 / 2) {
			c->pts_gaps++;
			c->pts_skipped += (d + period / 2) / period - 1;
		}
	}
	c->last_emit_pts = pts;
	c->enc_bytes += aulen;
	if (kf)
		c->enc_keyframes++;
	if (tx) {
		rm_tx_submit_au(tx, au, (size_t)aulen, (int64_t)pts, (uint32_t)(kf ? RM_FLAG_KEYFRAME : 0));
		rpw_hist_mark(&c->h_tx, mono_ns()); /* queued to the native sender (== Go-pump "tx" stage) */
		c->enc_aus++;
		return;
	}
	if (au_ring_push(c, au, aulen, kf, pts)) {
		pthread_mutex_lock(&c->mu);
		if (!c->force_idr) {
			c->force_idr = 1;
			c->forced_idrs++;
		}
		pthread_mutex_unlock(&c->mu);
		return;
	}
	pthread_mutex_lock(&c->mu);
	pthread_cond_signal(&c->cv);
	pthread_mutex_unlock(&c->mu);
}

/* enc_thread_main: the dedicated encode thread. Waits for the callback's hand-off (newest buffer),
 * encodes (VPP + VA submit + sync + map - the blocking that used to stall the PipeWire callback), emits
 * the AU, requeues the pw buffer under the loop lock.
 *
 * Cadence (period p = 1/negotiated fps): the encoder pipelines (frame N is emitted when N+1 is
 * submitted), so a source that slows, hitches or goes static would hold its last frame back and starve
 * the route (receiver: gap, then burst). The thread therefore also wakes on deadlines:
 *  - pipelined frame still pending at t_submit + 1.5p (next frame late) => flush it now;
 *  - nothing pending, no real frame for 1.5p (hitch) - or the source runs slower than ~0.8x the rate -
 *    and no AU for p => re-encode the last frame (P-skip, tiny) so the route keeps the full cadence.
 *    Only while the stream is STREAMING; a vanished producer stops repeats (receiver holds).
 *  - a real frame arriving after a gap >= 1.5p (or the first) is flushed at once instead of waiting a
 *    period in the pipeline.
 * PTS: each AU carries ITS frame's capture PTS (strictly increasing); repeats advance by p. */
static void *enc_thread_main(void *arg) {
	struct rpw_capture *c = arg;
	/* best-effort elevated scheduling so the encode is not starved under load (both usually need
	 * privilege as a desktop user; non-fatal if denied - the win is that the callback no longer blocks). */
	struct sched_param sp;
	memset(&sp, 0, sizeof(sp));
	sp.sched_priority = 10;
	if (pthread_setschedparam(pthread_self(), SCHED_RR, &sp) != 0)
		setpriority(PRIO_PROCESS, 0, -5); /* fall back to a nice bump (also best-effort) */
	/* encode-thread-local cadence state (ns, CLOCK_MONOTONIC) */
	long long t_real = 0, t_emit = 0, t_sub = 0, src_iv = 0, last_pts = 0, pend_pts = 0;
	int enc_pend = 0; /* encoder holds a submitted, not yet emitted frame */
	struct rva_enc *enc_seen = NULL;
	for (;;) {
		long long period = 1000000000LL / (c->enc_fps > 0 ? c->enc_fps : 60);
		long long dl = 0;
		pthread_mutex_lock(&c->enc_mu);
		for (;;) {
			dl = 0;
			if (c->enc_pending || c->enc_stop)
				break;
			if (c->enc && c->enc == enc_seen) {
				if (enc_pend) {
					dl = t_sub + period * 3 / 2;
				} else if (t_emit && atomic_load_explicit(&c->streaming, memory_order_relaxed)) {
					long long gate = src_iv > period * 5 / 4 ? 0 : period * 3 / 2; /* slow source: even fill */
					dl = t_emit + period;
					if (t_real + gate > dl)
						dl = t_real + gate;
				}
			}
			if (!dl) {
				pthread_cond_wait(&c->enc_cv, &c->enc_mu);
				continue;
			}
			if (mono_ns() >= dl)
				break;
			struct timespec ts = {(time_t)(dl / 1000000000LL), (long)(dl % 1000000000LL)};
			if (pthread_cond_timedwait(&c->enc_cv, &c->enc_mu, &ts) != 0 && !c->enc_pending)
				break; /* deadline */
		}
		struct rpw_pend pe;
		memset(&pe, 0, sizeof(pe));
		if (c->hq_n > 0) { /* pop the OLDEST queued frame */
			pe = c->hq[c->hq_head];
			c->hq_head = (c->hq_head + 1) % 4;
			c->hq_n--;
		}
		c->enc_pending = c->hq_n > 0 ? c->hq[c->hq_head].buf : NULL;
		struct pw_buffer *buf = pe.buf;
		int stop = c->enc_stop;
		int np = pe.np, fds[RPW_MAX_PLANES];
		unsigned off[RPW_MAX_PLANES], str[RPW_MAX_PLANES];
		for (int p = 0; p < np; p++) {
			fds[p] = pe.fds[p];
			off[p] = pe.off[p];
			str[p] = pe.str[p];
		}
		uint32_t fourcc = pe.fourcc;
		uint64_t mod = pe.mod;
		int w = pe.w, h = pe.h, flip = pe.flip;
		long long pts = pe.pts;
		rm_sender *tx = c->native_tx; /* native-TX switch, read under enc_mu */
		pthread_mutex_unlock(&c->enc_mu);

		if (stop)
			break; /* shutting down: drop any held buffer - pw_stream_destroy frees the pool */
		unsigned char *au = NULL;
		int aulen = 0, kf = 0;
		if (!buf) { /* cadence deadline: flush the pending frame, or repeat the last one */
			if (!c->enc || c->enc != enc_seen)
				continue;
			if (!tx && (!au_ring_room(c) || au_ring_first_unconsumed(c))) {
				/* downstream stalled / start-up hold: never queue cadence fill (a stale burst later) */
				t_emit = dl ? dl : mono_ns();
				if (enc_pend)
					t_sub = mono_ns(); /* re-check the pending flush in 1.5p, not in a busy loop */
				continue;
			}
			int idr = enc_pend ? 0 : enc_ensure(c);
			if (c->enc != enc_seen) /* enc_ensure rebuilt it (size/quality change): wait for real input */
				continue;
			int was_pend = enc_pend;
			int rc = was_pend ? rva_enc_flush(c->enc, &au, &aulen, &kf) : rva_enc_repeat(c->enc, idr, &au, &aulen, &kf);
			enc_mark_steps(c);
			enc_pend = 0;
			long long tn = mono_ns();
			t_emit = (dl && tn - dl < period) ? dl : tn; /* stay on the ideal grid (no encode-time drift) */
			if (rc == 0 && aulen > 0) {
				long long apts = was_pend ? pend_pts : last_pts + period;
				if (apts <= last_pts)
					apts = last_pts + 1;
				last_pts = apts;
				if (!was_pend)
					c->repeats++;
				enc_emit_au(c, tx, au, aulen, kf, apts, was_pend);
			}
			continue;
		}
		long long now = mono_ns();
		/* start-up hold / downstream stall: skip PRE-encode (frame never enters the encoder => no
		 * reference break, no IDR). The pending pipelined frame is untouched. */
		if (!tx && au_ring_first_unconsumed(c)) {
			c->start_holds++;
		} else if (!tx && !au_ring_room(c)) {
			c->ring_skips++;
			c->dropped++;
		} else {
			int idr = enc_ensure(c);
			if (c->enc != enc_seen) { /* new/rebuilt encoder: empty pipeline */
				enc_seen = c->enc;
				enc_pend = 0;
				t_emit = 0;
			}
			if (c->enc) {
				long long gap = t_real ? now - t_real : 0;
				if (t_real) { /* source interval estimate (clamped: one hitch must not flip the mode) */
					long long g = gap < 2 * period ? gap : 2 * period;
					src_iv = src_iv ? (src_iv * 7 + g) / 8 : g;
				}
				int prev_pend = enc_pend;
				long long prev_pts = pend_pts;
				int sync_now = c->enc_sync || gap == 0 || gap >= period * 3 / 2;
				if (pts <= 0)
					pts = now;
				int rc = rva_enc_frame(c->enc, fourcc, mod, w, h, np, fds, off, str, flip, idr, &au, &aulen, &kf);
				enc_mark_steps(c);
				if (c->slow_us > 0 && c->height > 0) { /* test hook: encode cost ~ coded pixels (overload sim) */
					long long d = (long long)c->slow_us * c->coded_h / c->height * c->coded_h / c->height;
					struct timespec sl = {(time_t)(d / 1000000), (long)(d % 1000000) * 1000};
					nanosleep(&sl, NULL);
				}
				if (rc == 0) {
					c->import_ok = 1;
					rva_enc_feedback(c->enc, &c->fb_present, &c->fb_avg_qp, &c->fb_rate_limited);
					enc_pend = 1;
					pend_pts = pts;
					t_sub = now;
					t_real = now;
					if (aulen > 0 && prev_pend) { /* the previous frame, emitted by this submit */
						long long apts = prev_pts > last_pts ? prev_pts : last_pts + 1;
						last_pts = apts;
						t_emit = now;
						enc_emit_au(c, tx, au, aulen, kf, apts, 1);
					}
					if (sync_now) {
						/* first frame / after a gap: emit THIS frame now, not one period later */
						int frc = rva_enc_flush(c->enc, &au, &aulen, &kf);
						enc_mark_steps(c);
						if (frc == 0 && aulen > 0) {
							long long apts = pend_pts > last_pts ? pend_pts : last_pts + 1;
							last_pts = apts;
							t_emit = mono_ns();
							enc_emit_au(c, tx, au, aulen, kf, apts, 1);
						}
						enc_pend = 0;
					}
				} else {
					c->dropped++;
					c->enc_drops++;
				}
			}
		}
		/* requeue the held buffer from this thread (pw_stream is not thread-safe -> lock the loop). */
		PW.tl_lock(c->loop);
		PW.stream_queue(c->stream, buf);
		PW.tl_unlock(c->loop);
		long long fr_us = (mono_ns() - now) / 1000;
		rpw_uhist_add(&c->h_step[RVA_NSTEP], fr_us);
		atomic_fetch_add_explicit(&c->busy_us, fr_us, memory_order_relaxed);
		ac_tick(c, now);
	}
	return NULL;
}

/* enc_thread_start lazily creates the encode thread (once). */
static void enc_thread_start(struct rpw_capture *c) {
	if (c->enc_thread_on)
		return;
	c->enc_stop = 0;
	if (pthread_create(&c->enc_thread, NULL, enc_thread_main, c) == 0)
		c->enc_thread_on = 1;
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
	atomic_fetch_add_explicit(&c->frames_seen, 1, memory_order_relaxed);
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
				enc_thread_start(c); /* B.2: lazy-create the dedicated encode thread */
				if (c->enc_thread_on) {
					/* Hand the NEWEST buffer to the encode thread and return - the callback never blocks
					 * on VA (vaSyncSurface/vaMapBuffer run on the thread). Newest-wins at capture: if the
					 * encoder is still busy with a pending buffer, requeue that stale one here (loop thread
					 * -> safe) and keep the freshest. The thread requeues `last` after it has consumed the
					 * dmabuf (implicit dmabuf sync makes the post-submit requeue safe, as before). */
					/* B.3: PTS-grid gate - emit the newest frame per 1/fps slot (even pacing from a faster
					 * source). Prefer the capture PTS (VRChat/Spout2PW carries it); fall back to a monotonic
					 * arrival clock when the source has none. Tolerance period/4 keeps a jittery exactly-60
					 * source (every frame) while decimating 120->60 cleanly. */
					long long gpts = buffer_pts(sb);
					if (gpts <= 0)
						gpts = mono_ns();
					int gfps = c->enc_fps;
					int capf = __atomic_load_n(&c->cap_fps, __ATOMIC_RELAXED);
					if (capf > 0 && (gfps <= 0 || capf < gfps))
						gfps = capf; /* governor fps rung: same grid, coarser slot */
					if (gfps > 0) {
						long long period = 1000000000LL / gfps;
						long long tol = period / 4;
						/* source interval EMA (clamped to 2p): decimate ONLY a source that really runs
						 * faster than the rate (VRChat 120 -> 60). A ~rate source with arrival jitter (no
						 * header PTS: the arrival clock) must pass every frame - gating it on the grid
						 * dropped every other frame of a 4K60 libfunnel source (capFps 30). */
						if (c->grid_last_pts > 0 && gpts > c->grid_last_pts) {
							long long d = gpts - c->grid_last_pts;
							if (d > 2 * period)
								d = 2 * period;
							c->grid_src_iv = c->grid_src_iv ? (c->grid_src_iv * 7 + d) / 8 : d;
						}
						c->grid_last_pts = gpts;
						if (c->grid_src_iv == 0 || c->grid_src_iv > period * 3 / 4) {
							c->enc_grid_next = gpts + period; /* pass-through; keep the grid primed */
						} else if (c->enc_grid_next == 0 || gpts + 4 * period < c->enc_grid_next) {
							/* prime on the first frame; re-prime when PTS jumped BACK (new producer /
							 * clock origin) instead of pacing everything out until it catches up */
							c->enc_grid_next = gpts + period;
						} else if (gpts + tol < c->enc_grid_next) {
							PW.stream_queue(c->stream, last); /* sub-grid: pace it out (pre-encode skip) */
							c->grid_drops++;
							return;
						} else {
							c->enc_grid_next += period;
							if (c->enc_grid_next <= gpts + tol) /* catch up after a gap / slow source */
								c->enc_grid_next += ((gpts + tol - c->enc_grid_next) / period + 1) * period;
						}
					}
					int np = sb->n_datas < RPW_MAX_PLANES ? (int)sb->n_datas : RPW_MAX_PLANES;
					c->cap_frames++; /* captured frames handed to the encoder stage (CapFPS) */
					pthread_mutex_lock(&c->enc_mu);
					if (c->hq_n >= c->hq_max) { /* sustained overload: drop the OLDEST queued frame */
						PW.stream_queue(c->stream, c->hq[c->hq_head].buf);
						c->hq_head = (c->hq_head + 1) % 4;
						c->hq_n--;
						c->cap_drops++;
						c->dropped++;
					}
					struct rpw_pend *pe = &c->hq[(c->hq_head + c->hq_n) % 4];
					pe->np = np;
					for (int p = 0; p < np; p++) {
						pe->fds[p] = sb->datas[p].fd;
						pe->off[p] = sb->datas[p].chunk ? sb->datas[p].chunk->offset : 0;
						pe->str[p] = sb->datas[p].chunk ? sb->datas[p].chunk->stride : 0;
					}
					pe->fourcc = spa_to_drm_fourcc(c->fmt);
					pe->mod = c->modifier;
					pe->w = c->width;
					pe->h = c->height;
					pe->flip = want_flip(c, sb);
					pe->pts = gpts; /* capture PTS, else arrival (monotonic) - never 0 */
					pe->buf = last;  /* handed off; the thread requeues it */
					c->hq_n++;
					if (c->hq_n > c->hq_hiwater)
						c->hq_hiwater = c->hq_n;
					if (c->hq_n > c->hq_win_peak)
						c->hq_win_peak = c->hq_n;
					c->enc_pending = c->hq[c->hq_head].buf;
					pthread_cond_signal(&c->enc_cv);
					pthread_mutex_unlock(&c->enc_mu);
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
	atomic_store_explicit(&c->streaming, state == PW_STREAM_STATE_STREAMING, memory_order_relaxed);
	if (state != PW_STREAM_STATE_STREAMING && c->stream)
		hq_flush(c);
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
	if (!c->egl_ready) { /* SHM-only offer can never link a dmabuf producer: retry the EGL setup */
		egl_teardown(c);
		const char *rn = getenv("RPW_EGL_DEVICE");
		egl_setup_display(c, rn && *rn ? rn : "/dev/dri/renderD128");
	}
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

/* reconnect_or_backoff: called with c->mu held and need_reconnect set; releases c->mu. Re-arms the
 * stream now, or - while the backoff runs - sleeps (<= timeout_ms) and leaves need_reconnect set for the
 * next call. A reconnect that delivered no frame since the previous one doubles the backoff (50 ms ->
 * 2 s); any delivered frame resets it, so a real producer restart (VRChat hitch) re-arms immediately. */
static void reconnect_or_backoff(struct rpw_capture *c, long timeout_ms) {
	long long now = mono_ns();
	if (now < c->next_reconnect_ns) {
		pthread_mutex_unlock(&c->mu);
		long long d = c->next_reconnect_ns - now, cap = (long long)timeout_ms * 1000000LL;
		if (d > cap)
			d = cap;
		struct timespec ts = {(time_t)(d / 1000000000LL), (long)(d % 1000000000LL)};
		nanosleep(&ts, NULL);
		return;
	}
	c->need_reconnect = 0;
	pthread_mutex_unlock(&c->mu);
	long seen = atomic_load_explicit(&c->frames_seen, memory_order_relaxed);
	if (c->reconnects > 0 && seen == c->frames_at_reconnect) {
		c->reconnect_backoff_ms = c->reconnect_backoff_ms ? c->reconnect_backoff_ms * 2 : 50;
		if (c->reconnect_backoff_ms > 2000)
			c->reconnect_backoff_ms = 2000;
	} else {
		c->reconnect_backoff_ms = 0;
	}
	c->frames_at_reconnect = seen;
	c->next_reconnect_ns = mono_ns() + (long long)c->reconnect_backoff_ms * 1000000LL;
	do_reconnect(c);
}

struct rpw_capture *rpw_start(const char *target_node) {
	if (!rpw_available())
		return NULL;
	struct rpw_capture *c = calloc(1, sizeof(*c));
	if (!c)
		return NULL;
	pthread_mutex_init(&c->mu, NULL);
	pthread_cond_init(&c->cv, NULL);
	pthread_mutex_init(&c->enc_mu, NULL);
	pthread_condattr_t ca; /* encode-thread cadence deadlines are CLOCK_MONOTONIC (mono_ns) */
	pthread_condattr_init(&ca);
	pthread_condattr_setclock(&ca, CLOCK_MONOTONIC);
	pthread_cond_init(&c->enc_cv, &ca);
	pthread_condattr_destroy(&ca);
	c->modifier = DRM_FORMAT_MOD_INVALID;
	c->drm_fd = -1;
	c->flip_default = getenv("RPW_FLIP_V") != NULL; /* default no-flip (Spout2PW/DX, PipeWire convention) */
	c->pending_maxh = -1;                           /* -1 = no live coded-height cap pending */
	c->enc_sync = getenv("RPW_ENC_SYNC") != NULL;
	c->fz_dbg = getenv("RPW_FZ_DEBUG") != NULL;
	c->slow_us = getenv("RPW_ENC_SLOW_US") ? atoi(getenv("RPW_ENC_SLOW_US")) : 0;
	{
		const char *q = getenv("RPW_CAP_QUEUE"); /* 1 = the old single newest-wins slot */
		c->hq_max = q ? atoi(q) : 3;
		if (c->hq_max < 1)
			c->hq_max = 1;
		if (c->hq_max > 4)
			c->hq_max = 4;
	}

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
		reconnect_or_backoff(c, timeout_ms); /* Go thread: safe to lock the pw thread-loop; unlocks mu */
		return 0;                           /* treated as a timeout by Next -> keeps the route alive */
	}
	while (!c->has_frame && !c->fatal && !c->need_reconnect) {
		if (pthread_cond_timedwait(&c->cv, &c->mu, &ts) != 0)
			break;
	}
	if (c->need_reconnect && !c->has_frame) {
		reconnect_or_backoff(c, timeout_ms);
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
	c->rx_maxh = max_h;
	ac_init(c);
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
	if (atomic_load_explicit(&c->ring_head, memory_order_acquire) > 0)
		atomic_store_explicit(&c->consumer_back, 1, memory_order_release);
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
			reconnect_or_backoff(c, timeout_ms); /* unlocks mu */
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

/* rpw_set_cap_fps caps the encoded frame rate (receiver governor fps rung; 0 = clear) via the PTS
 * grid: newest frame per 1/fps slot, so the output stays evenly spaced. Applied on the next frame. */
void rpw_set_cap_fps(struct rpw_capture *c, int fps) {
	if (!c)
		return;
	__atomic_store_n(&c->cap_fps, fps > 0 ? fps : 0, __ATOMIC_RELAXED);
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
	c->rx_maxh = max_h;
	ac_apply_cap_locked(c); /* the tighter of the receiver's cap and the auto controller's */
	pthread_mutex_unlock(&c->mu);
}

/* rpw_source_freezes reports the source-static detector: event count, last + longest duration (ms), the
 * last one's wall-clock start (unix ms), whether the source delivered new buffers throughout (1 = the
 * PICTURE froze, e.g. VRChat hitch) and how many cadence repeats filled it (> 0 = the source stalled). */
void rpw_source_freezes(struct rpw_capture *c, long *count, long long *last_ms, long long *longest_ms,
                        long long *last_wall_ms, int *delivered, int *repeats) {
	if (!c)
		return;
	pthread_mutex_lock(&c->mu);
	*count = c->fz_count;
	*last_ms = c->fz_last_ms;
	*longest_ms = c->fz_longest_ms;
	*last_wall_ms = c->fz_last_wall;
	*delivered = c->fz_last_delivered;
	*repeats = c->fz_last_repeats;
	pthread_mutex_unlock(&c->mu);
}

/* rpw_autocap reports the auto coded-size controller: level (0 = native), active coded height (0 =
 * native), change generation, last reason. */
void rpw_autocap(struct rpw_capture *c, int *level, int *coded_h, int *gen, char *reason, int n) {
	if (!c)
		return;
	pthread_mutex_lock(&c->mu);
	if (level)
		*level = c->ac_level;
	if (coded_h)
		*coded_h = ac_level_h(c->height, c->ac_level);
	if (gen)
		*gen = atomic_load(&c->ac_gen);
	if (reason && n > 0)
		snprintf(reason, (size_t)n, "%s", c->ac_reason);
	pthread_mutex_unlock(&c->mu);
}

/* encode telemetry getters (read from the Go telemetry goroutine). */
long rpw_enc_aus(struct rpw_capture *c) { return c ? c->enc_aus : 0; }
long rpw_enc_drops(struct rpw_capture *c) { return c ? c->enc_drops : 0; }
long rpw_cap_frames(struct rpw_capture *c) { return c ? c->cap_frames : 0; }
long rpw_reconnects(struct rpw_capture *c) { return c ? c->reconnects : 0; }
int rpw_egl_ready(struct rpw_capture *c) { return c ? c->egl_ready : 0; }
long rpw_repeats(struct rpw_capture *c) { return c ? c->repeats : 0; }
long rpw_ring_skips(struct rpw_capture *c) { return c ? c->ring_skips : 0; }
long rpw_start_holds(struct rpw_capture *c) { return c ? c->start_holds : 0; }
long rpw_forced_idrs(struct rpw_capture *c) { return c ? c->forced_idrs : 0; }
long rpw_enc_keyframes(struct rpw_capture *c) { return c ? c->enc_keyframes : 0; }
void rpw_step_stats(struct rpw_capture *c, int step, rpw_stage_snap *out) {
	if (!out)
		return;
	if (!c || step < 0 || step > RVA_NSTEP) {
		memset(out, 0, sizeof(*out));
		return;
	}
	rpw_uhist_snap(&c->h_step[step], out);
}
long long rpw_busy_us(struct rpw_capture *c) { return c ? atomic_load(&c->busy_us) : 0; }
long rpw_pts_gaps(struct rpw_capture *c) { return c ? c->pts_gaps : 0; }
long rpw_pts_skipped(struct rpw_capture *c) { return c ? c->pts_skipped : 0; }
long long rpw_enc_bytes(struct rpw_capture *c) { return c ? c->enc_bytes : 0; }
void rpw_native_stages(struct rpw_capture *c, rpw_stage_snap *enc, rpw_stage_snap *tx) {
	if (!c) {
		if (enc)
			memset(enc, 0, sizeof(*enc));
		if (tx)
			memset(tx, 0, sizeof(*tx));
		return;
	}
	if (enc)
		rpw_hist_snap(&c->h_enc, enc);
	if (tx)
		rpw_hist_snap(&c->h_tx, tx);
}
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
/* capture-side drops: buffers dropped at the callback->encode-thread hand-off (encoder behind). */
long rpw_cap_drops(struct rpw_capture *c) { return c ? c->cap_drops : 0; }
/* grid drops: frames paced out by the PTS-grid (source faster than the negotiated rate) - deliberate. */
long rpw_grid_drops(struct rpw_capture *c) { return c ? c->grid_drops : 0; }

/* ---- Native realtime TX (unit 3): hand the encode thread's AU stream to the Zig sender ---- */

/* native_idr_cb: the Zig sender dropped an AU (queue full) -> force an IDR so the receiver resyncs.
 * Fires on the sender's thread; sets the flag under c->mu, which enc_ensure reads it under. */
static void native_idr_cb(void *user) {
	struct rpw_capture *c = user;
	pthread_mutex_lock(&c->mu);
	c->force_idr = 1;
	pthread_mutex_unlock(&c->mu);
}

/* rpw_attach_native_tx opens a native sender from the per-route params Go derived + switches the
 * encode thread onto it (rm_tx_submit_au). peer_host/peer_port are resolved here (native opens its own
 * socket; socket_fd < 0). Returns 1 on success (native path engaged), 0 on failure (Go keeps the ring
 * fallback). Call AFTER rpw_set_encode. codec: 4=H.264 5=HEVC 6=AV1; transport: 0=UDP 1=TCP. */
int rpw_attach_native_tx(struct rpw_capture *c, int transport, int codec, int stream, int body_max,
                         const char *peer_host, const char *peer_port, int encrypt,
                         const unsigned char *send_key, unsigned send_prefix,
                         const unsigned char *recv_key, unsigned recv_prefix,
                         unsigned token, double initial_rate_bps,
                         unsigned long long initial_seq, int fps, long long startup_grace_ns,
                         long long media_clock_ns, long long socket_fd) {
	if (!c || c->native_tx || !send_key || !recv_key)
		return 0;
	struct addrinfo hints, *res = NULL;
	if (socket_fd < 0) { /* UDP: native opens its own socket toward the peer */
		memset(&hints, 0, sizeof(hints));
		hints.ai_family = AF_UNSPEC;
		hints.ai_socktype = (transport == 1) ? SOCK_STREAM : SOCK_DGRAM;
		if (getaddrinfo(peer_host, peer_port, &hints, &res) != 0 || !res)
			return 0;
	}
	rm_open_params p;
	memset(&p, 0, sizeof(p));
	p.struct_size = (uint32_t)sizeof(rm_open_params); /* ABI v2 layout guard */
	p.abi_version = RM_ABI_VERSION;
	p.transport = (transport == 1) ? RM_TRANSPORT_TCP : RM_TRANSPORT_UDP;
	p.codec = (codec == 5) ? RM_CODEC_HEVC : (codec == 6) ? RM_CODEC_AV1 : RM_CODEC_H264;
	p.stream = (uint16_t)stream;
	p.body_max = body_max;
	p.socket_fd = socket_fd >= 0 ? socket_fd : -1; /* TCP: Go's accepted route connection (dup; Go closes it) */
	p.peer_addr = res ? (const uint8_t *)res->ai_addr : NULL;
	p.peer_addr_len = res ? (int32_t)res->ai_addrlen : 0;
	memcpy(p.keys.send_key, send_key, 32);
	p.keys.send_nonce_prefix = send_prefix;
	memcpy(p.keys.recv_key, recv_key, 32);
	p.keys.recv_nonce_prefix = recv_prefix;
	p.keys.token = token;
	p.keys.encrypt = encrypt ? 1 : 0;
	p.keys.initiator = 0; /* the sender is the answerer (responder); send = s2c */
	p.initial_rate_bytes_per_sec = initial_rate_bps;
	p.initial_seq = (uint64_t)initial_seq; /* continue the Go handshake conn's seq space -> receiver rebinds */
	p.fps = (int32_t)fps;
	p.startup_grace_ns = (int64_t)startup_grace_ns; /* 0 -> native default */
	p.media_clock_ns = (int64_t)media_clock_ns; /* ABI v3: PTS into the Go media-clock domain (0 = local mono) */
	rm_sender *s = NULL;
	rm_status st = rm_tx_open(&p, native_idr_cb, c, &s);
	if (res)
		freeaddrinfo(res);
	if (st != RM_OK || !s)
		return 0;
	pthread_mutex_lock(&c->enc_mu);
	c->native_tx = s;
	pthread_mutex_unlock(&c->enc_mu);
	pthread_mutex_lock(&c->mu);
	c->force_idr = 1; /* open the native stream with an IDR */
	pthread_mutex_unlock(&c->mu);
	return 1;
}

/* rpw_native_tx_set_rate updates the native pacer (bytes/sec). */
void rpw_native_tx_set_rate(struct rpw_capture *c, double bytes_per_sec) {
	if (c && c->native_tx)
		rm_tx_set_rate(c->native_tx, bytes_per_sec);
}

/* rpw_native_tx_stats copies the sender's lock-free counters (zeroed if not attached). */
/* rpw_native_tx_rate_hint copies the native sender's last receiver MetaRate (ABI v4; gen 0 = none). */
void rpw_native_tx_rate_hint(struct rpw_capture *c, rm_rate_hint *out) {
	if (!out)
		return;
	if (c && c->native_tx)
		rm_tx_rate_hint(c->native_tx, out);
	else
		memset(out, 0, sizeof(*out));
}

void rpw_native_tx_stats(struct rpw_capture *c, rm_tx_stats *out) {
	if (!out)
		return;
	if (c && c->native_tx)
		rm_tx_stats_get(c->native_tx, out);
	else
		memset(out, 0, sizeof(*out));
}

/* rpw_native_tx_close stops + frees the native sender; the encode thread reverts to the ring. */
void rpw_native_tx_close(struct rpw_capture *c) {
	if (!c)
		return;
	pthread_mutex_lock(&c->enc_mu);
	rm_sender *s = c->native_tx;
	c->native_tx = NULL;
	pthread_mutex_unlock(&c->enc_mu);
	if (s)
		rm_tx_close(s); /* no callback fires after this returns */
}

void rpw_stop(struct rpw_capture *c) {
	if (!c)
		return;
	if (c->loop)
		PW.tl_stop(c->loop); /* no more on_process callbacks after this */
	if (c->enc_thread_on) {  /* stop + join the encode thread while the stream still exists */
		pthread_mutex_lock(&c->enc_mu);
		c->enc_stop = 1;
		pthread_cond_signal(&c->enc_cv);
		pthread_mutex_unlock(&c->enc_mu);
		pthread_join(c->enc_thread, NULL);
		c->enc_thread_on = 0;
	}
	if (c->native_tx) { /* encode thread joined -> no more submits; close the native sender */
		rm_tx_close(c->native_tx);
		c->native_tx = NULL;
	}
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
	/* EGL teardown (also a partial setup): the context was made current on the loop thread, now stopped. */
	egl_teardown(c);
	free(c->rbscratch);
	free(c->slot);
	pthread_mutex_destroy(&c->mu);
	pthread_cond_destroy(&c->cv);
	pthread_mutex_destroy(&c->enc_mu);
	pthread_cond_destroy(&c->enc_cv);
	free(c);
}
