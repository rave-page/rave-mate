//go:build linux && pipewire

/* vaenc.c - VA-API dmabuf import + VPP (and, later, encode). libva/libva-drm are dlopen'd so a box
 * without them degrades cleanly. See vaenc.h. */
#include "vaenc.h"

#include <dlfcn.h>
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include <va/va.h>
#include <va/va_drm.h>
#include <va/va_drmcommon.h>
#include <va/va_vpp.h>
#include <va/va_enc_hevc.h>
#include <libdrm/drm_fourcc.h>
#include <fcntl.h>
#include <pthread.h>

#define RVA_MAX_PLANES 4

static int va_dbg = 0;
#define VDBG(...)                          \
	do {                                   \
		if (va_dbg)                        \
			fprintf(stderr, __VA_ARGS__);  \
	} while (0)

static long long now_us(void) {
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return (long long)t.tv_sec * 1000000LL + t.tv_nsec / 1000;
}

struct va_api {
	int ok;
	/* libva */
	VAStatus (*Initialize)(VADisplay, int *, int *);
	VAStatus (*Terminate)(VADisplay);
	const char *(*ErrorStr)(VAStatus);
	VAStatus (*CreateConfig)(VADisplay, VAProfile, VAEntrypoint, VAConfigAttrib *, int, VAConfigID *);
	VAStatus (*DestroyConfig)(VADisplay, VAConfigID);
	VAStatus (*CreateContext)(VADisplay, VAConfigID, int, int, int, VASurfaceID *, int, VAContextID *);
	VAStatus (*DestroyContext)(VADisplay, VAContextID);
	VAStatus (*CreateSurfaces)(VADisplay, unsigned, unsigned, unsigned, VASurfaceID *, unsigned, VASurfaceAttrib *, unsigned);
	VAStatus (*DestroySurfaces)(VADisplay, VASurfaceID *, int);
	VAStatus (*CreateBuffer)(VADisplay, VAContextID, VABufferType, unsigned, unsigned, void *, VABufferID *);
	VAStatus (*DestroyBuffer)(VADisplay, VABufferID);
	VAStatus (*BeginPicture)(VADisplay, VAContextID, VASurfaceID);
	VAStatus (*RenderPicture)(VADisplay, VAContextID, VABufferID *, int);
	VAStatus (*EndPicture)(VADisplay, VAContextID);
	VAStatus (*SyncSurface)(VADisplay, VASurfaceID);
	VAStatus (*DeriveImage)(VADisplay, VASurfaceID, VAImage *);
	VAStatus (*DestroyImage)(VADisplay, VAImageID);
	VAStatus (*MapBuffer)(VADisplay, VABufferID, void **);
	VAStatus (*UnmapBuffer)(VADisplay, VABufferID);
	VAStatus (*GetConfigAttributes)(VADisplay, VAProfile, VAEntrypoint, VAConfigAttrib *, int);
	/* libva-drm */
	VADisplay (*GetDisplayDRM)(int);
};
static struct va_api VA;
static int va_loaded = -1;
static pthread_mutex_t va_load_mu = PTHREAD_MUTEX_INITIALIZER;

static int va_load_locked(void);

/* va_load: one-time dlopen, serialized. Unguarded, a concurrent first caller (daemon restart = every
 * route starts at once) read the in-progress 0 as "unavailable" and fell back for the route's life. */
static int va_load(void) {
	pthread_mutex_lock(&va_load_mu);
	if (va_loaded == -1)
		va_loaded = va_load_locked();
	int r = va_loaded;
	pthread_mutex_unlock(&va_load_mu);
	return r;
}

static int va_load_locked(void) {
	va_dbg = getenv("RPW_VA_DEBUG") != NULL || getenv("RPW_EGL_DEBUG") != NULL;
	void *l = dlopen("libva.so.2", RTLD_NOW | RTLD_GLOBAL);
	void *d = dlopen("libva-drm.so.2", RTLD_NOW | RTLD_GLOBAL);
	if (!l || !d) {
		VDBG("va_load: dlopen failed (va=%p drm=%p)\n", l, d);
		return 0;
	}
	struct {
		const char *n;
		void **s;
		void *h;
	} t[] = {
		{"vaInitialize", (void **)&VA.Initialize, l},
		{"vaTerminate", (void **)&VA.Terminate, l},
		{"vaErrorStr", (void **)&VA.ErrorStr, l},
		{"vaCreateConfig", (void **)&VA.CreateConfig, l},
		{"vaDestroyConfig", (void **)&VA.DestroyConfig, l},
		{"vaCreateContext", (void **)&VA.CreateContext, l},
		{"vaDestroyContext", (void **)&VA.DestroyContext, l},
		{"vaCreateSurfaces", (void **)&VA.CreateSurfaces, l},
		{"vaDestroySurfaces", (void **)&VA.DestroySurfaces, l},
		{"vaCreateBuffer", (void **)&VA.CreateBuffer, l},
		{"vaDestroyBuffer", (void **)&VA.DestroyBuffer, l},
		{"vaBeginPicture", (void **)&VA.BeginPicture, l},
		{"vaRenderPicture", (void **)&VA.RenderPicture, l},
		{"vaEndPicture", (void **)&VA.EndPicture, l},
		{"vaSyncSurface", (void **)&VA.SyncSurface, l},
		{"vaDeriveImage", (void **)&VA.DeriveImage, l},
		{"vaDestroyImage", (void **)&VA.DestroyImage, l},
		{"vaMapBuffer", (void **)&VA.MapBuffer, l},
		{"vaUnmapBuffer", (void **)&VA.UnmapBuffer, l},
		{"vaGetConfigAttributes", (void **)&VA.GetConfigAttributes, l},
		{"vaGetDisplayDRM", (void **)&VA.GetDisplayDRM, d},
	};
	for (size_t i = 0; i < sizeof(t) / sizeof(t[0]); i++) {
		void *s = dlsym(t[i].h, t[i].n);
		if (!s) {
			VDBG("va_load: missing %s\n", t[i].n);
			return 0;
		}
		*t[i].s = s;
	}
	VA.ok = 1;
	return 1;
}

int rva_available(void) { return va_load(); }

/* drm fourcc -> VA fourcc for the RGB32 import formats we offer. */
static uint32_t drm_to_va_fourcc(uint32_t drm) {
	switch (drm) {
	case DRM_FORMAT_ARGB8888: return VA_FOURCC_BGRA; /* bytes B,G,R,A */
	case DRM_FORMAT_ABGR8888: return VA_FOURCC_RGBA;
	case DRM_FORMAT_XRGB8888: return VA_FOURCC_BGRX;
	case DRM_FORMAT_XBGR8888: return VA_FOURCC_RGBX;
	default: return 0;
	}
}

/* import_dmabuf builds a DRM_PRIME_2 surface from the planes (all planes incl. DCC metadata, with
 * the modifier on each object). radeonsi interprets the multi-plane tiled layout. */
static VAStatus import_dmabuf(VADisplay dpy, uint32_t drm_fourcc, uint64_t modifier, int w, int h,
                              int n_planes, const int *fds, const unsigned *offsets, const unsigned *strides,
                              VASurfaceID *out) {
	if (n_planes < 1 || n_planes > RVA_MAX_PLANES)
		return VA_STATUS_ERROR_INVALID_PARAMETER;
	uint32_t va_fourcc = drm_to_va_fourcc(drm_fourcc);
	if (!va_fourcc)
		return VA_STATUS_ERROR_INVALID_PARAMETER;

	VADRMPRIMESurfaceDescriptor d;
	memset(&d, 0, sizeof(d));
	d.fourcc = va_fourcc;
	d.width = (uint32_t)w;
	d.height = (uint32_t)h;

	/* Dedup planes to objects by the underlying dmabuf (st_ino), NOT the fd number: AMD DCC exports
	 * the color + metadata planes as offsets into ONE buffer, dup'd to several fds. Treating them as
	 * one object (with the plane offsets) is what radeonsi's VA import expects. */
	int objOf[RVA_MAX_PLANES];
	ino_t objIno[RVA_MAX_PLANES];
	for (int p = 0; p < n_planes; p++) {
		struct stat sb;
		ino_t ino = (fstat(fds[p], &sb) == 0) ? sb.st_ino : (ino_t)(uintptr_t)fds[p];
		int oi = -1;
		for (uint32_t j = 0; j < d.num_objects; j++)
			if (objIno[j] == ino)
				oi = (int)j;
		if (oi < 0) {
			oi = (int)d.num_objects;
			objIno[oi] = ino;
			d.objects[oi].fd = fds[p];
			off_t sz = lseek(fds[p], 0, SEEK_END);
			d.objects[oi].size = sz > 0 ? (uint32_t)sz : 0;
			d.objects[oi].drm_format_modifier = modifier;
			d.num_objects++;
		}
		objOf[p] = oi;
	}
	/* Plane selection for radeonsi DCC: VA import takes color + the PIPE-ALIGNED DCC plane. At 4K
	 * the driver adds a 3rd DCC_RETILE (display-scanout) plane and the 3-plane layout is rejected;
	 * import {color, last-DCC} = the pipe-aligned plane. 1080p is {color, DCC}. */
	/* Plane layouts VA imports cleanly: 1 plane (non-DCC tiled, incl. the AMD_DEBUG=nodcc producer
	 * path) and 2 planes (color + pipe-aligned DCC, e.g. 1080p). The 3-plane DCC_RETILE layout the
	 * driver adds at 4K is NOT importable here (invalid parameter / partial garbage), so reject it -
	 * the fix is producer-side (AMD_DEBUG=nodcc gives a 1-plane tiled buffer, correct at any size).
	 * LINEAR is rejected too: libfunnel mislabels tiled buffers as modifier 0. */
	int sel[2] = {0, 1};
	int imp_planes;
	if (modifier == DRM_FORMAT_MOD_LINEAR || n_planes > 2 || n_planes < 1)
		return VA_STATUS_ERROR_INVALID_PARAMETER;
	imp_planes = n_planes;
	d.num_layers = 1;
	d.layers[0].drm_format = drm_fourcc;
	d.layers[0].num_planes = (uint32_t)imp_planes;
	for (int i = 0; i < imp_planes; i++) {
		int p = sel[i];
		d.layers[0].object_index[i] = (uint32_t)objOf[p];
		d.layers[0].offset[i] = offsets[p];
		d.layers[0].pitch[i] = strides[p];
	}

	VASurfaceAttrib attrs[2];
	memset(attrs, 0, sizeof(attrs));
	attrs[0].type = VASurfaceAttribMemoryType;
	attrs[0].flags = VA_SURFACE_ATTRIB_SETTABLE;
	attrs[0].value.type = VAGenericValueTypeInteger;
	attrs[0].value.value.i = VA_SURFACE_ATTRIB_MEM_TYPE_DRM_PRIME_2;
	attrs[1].type = VASurfaceAttribExternalBufferDescriptor;
	attrs[1].flags = VA_SURFACE_ATTRIB_SETTABLE;
	attrs[1].value.type = VAGenericValueTypePointer;
	attrs[1].value.value.p = &d;

	return VA.CreateSurfaces(dpy, VA_RT_FORMAT_RGB32, (unsigned)w, (unsigned)h, out, 1, attrs, 2);
}

/* yuv2rgb (BT.709 full-range approx) for the corner check. */
static void yuv2rgb(int y, int u, int v, unsigned char *rgb) {
	double Y = y, U = u - 128, V = v - 128;
	int r = (int)(Y + 1.5748 * V);
	int g = (int)(Y - 0.1873 * U - 0.4681 * V);
	int b = (int)(Y + 1.8556 * U);
	rgb[0] = r < 0 ? 0 : r > 255 ? 255 : r;
	rgb[1] = g < 0 ? 0 : g > 255 ? 255 : g;
	rgb[2] = b < 0 ? 0 : b > 255 ? 255 : b;
}

/* rva_log_enc_caps logs H.264 EncSlice rate-control + packed-header + max-ref caps (determines the
 * encoder design: whether the app must write packed SPS/PPS/slice headers). Diagnostic. */
void rva_log_enc_caps(const char *node) {
	if (!va_load())
		return;
	int fd = open(node, O_RDWR | O_CLOEXEC);
	if (fd < 0)
		return;
	VADisplay dpy = VA.GetDisplayDRM(fd);
	int maj, min;
	if (!dpy || VA.Initialize(dpy, &maj, &min) != VA_STATUS_SUCCESS) {
		close(fd);
		return;
	}
	const VAProfile profs[] = {VAProfileH264ConstrainedBaseline, VAProfileHEVCMain};
	const char *pn[] = {"H264CB", "HEVCMain"};
	for (int i = 0; i < 2; i++) {
		VAConfigAttrib a[6];
		a[0].type = VAConfigAttribRateControl;
		a[1].type = VAConfigAttribEncPackedHeaders;
		a[2].type = VAConfigAttribEncMaxRefFrames;
		a[3].type = VAConfigAttribRTFormat;        /* RGB32 bit => VCN encodes RGB input itself (no VPP) */
		a[4].type = VAConfigAttribEncQualityRange; /* > 1 => VAEncMiscParameterTypeQualityLevel presets */
		a[5].type = VAConfigAttribContextPriority; /* > 0 => per-context priority settable */
		VAStatus st = VA.GetConfigAttributes(dpy, profs[i], VAEntrypointEncSlice, a, 6);
		fprintf(stderr, "RVA_ENC_CAPS %s st=%d ratecontrol=0x%x packedheaders=0x%x maxref=0x%x rtformat=0x%x(rgb32=%d) qualityrange=0x%x ctxprio=0x%x\n",
			pn[i], st, a[0].value, a[1].value, a[2].value, a[3].value, (a[3].value & VA_RT_FORMAT_RGB32) != 0,
			a[4].value, a[5].value);
	}
	VAConfigAttrib vp[2];
	vp[0].type = VAConfigAttribRTFormat;
	vp[1].type = VAConfigAttribContextPriority;
	VAStatus vst = VA.GetConfigAttributes(dpy, VAProfileNone, VAEntrypointVideoProc, vp, 2);
	fprintf(stderr, "RVA_VPP_CAPS st=%d rtformat=0x%x ctxprio=0x%x\n", vst, vp[0].value, vp[1].value);
	VA.Terminate(dpy);
	close(fd);
}

int rva_import_probe(const char *node, uint32_t drm_fourcc, uint64_t modifier, int w, int h,
                     int n_planes, const int *fds, const unsigned *offsets, const unsigned *strides,
                     unsigned char corners[12]) {
	if (!va_load())
		return -1;
	int fd = open(node, O_RDWR | O_CLOEXEC);
	if (fd < 0) {
		VDBG("va: open %s failed\n", node);
		return -2;
	}
	int rc = -10;
	VADisplay dpy = VA.GetDisplayDRM(fd);
	if (!dpy) {
		close(fd);
		return -3;
	}
	int maj, min;
	VAStatus st = VA.Initialize(dpy, &maj, &min);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("va: Initialize: %s\n", VA.ErrorStr(st));
		close(fd);
		return -4;
	}
	VDBG("va: init ok %d.%d\n", maj, min);

	VASurfaceID rgb = VA_INVALID_ID, nv12 = VA_INVALID_ID;
	VAConfigID cfg = VA_INVALID_ID;
	VAContextID ctx = VA_INVALID_ID;
	VABufferID pbuf = VA_INVALID_ID;
	VAImage img;
	img.image_id = VA_INVALID_ID;

	st = import_dmabuf(dpy, drm_fourcc, modifier, w, h, n_planes, fds, offsets, strides, &rgb);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("va: import (DRM_PRIME_2, %d planes, mod 0x%llx): %s\n", n_planes, (unsigned long long)modifier, VA.ErrorStr(st));
		rc = -5;
		goto done;
	}
	VDBG("va: imported %dx%d as RGB surface\n", w, h);

	/* NV12 output surface */
	VASurfaceAttrib na;
	memset(&na, 0, sizeof(na));
	na.type = VASurfaceAttribPixelFormat;
	na.flags = VA_SURFACE_ATTRIB_SETTABLE;
	na.value.type = VAGenericValueTypeInteger;
	na.value.value.i = VA_FOURCC_NV12;
	st = VA.CreateSurfaces(dpy, VA_RT_FORMAT_YUV420, (unsigned)w, (unsigned)h, &nv12, 1, &na, 1);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("va: create NV12: %s\n", VA.ErrorStr(st));
		rc = -6;
		goto done;
	}
	st = VA.CreateConfig(dpy, VAProfileNone, VAEntrypointVideoProc, NULL, 0, &cfg);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("va: create VPP config: %s\n", VA.ErrorStr(st));
		rc = -7;
		goto done;
	}
	st = VA.CreateContext(dpy, cfg, w, h, VA_PROGRESSIVE, &nv12, 1, &ctx);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("va: create VPP context: %s\n", VA.ErrorStr(st));
		rc = -8;
		goto done;
	}
	VAProcPipelineParameterBuffer pp;
	memset(&pp, 0, sizeof(pp));
	pp.surface = rgb;
	pp.surface_region = NULL;
	pp.output_region = NULL;
	pp.filter_flags = VA_FILTER_SCALING_HQ;
	pp.mirror_state = VA_MIRROR_VERTICAL; /* GL producer renders bottom-up; flip to image space */
	st = VA.CreateBuffer(dpy, ctx, VAProcPipelineParameterBufferType, sizeof(pp), 1, &pp, &pbuf);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("va: create pipeline buf: %s\n", VA.ErrorStr(st));
		rc = -9;
		goto done;
	}
	st = VA.BeginPicture(dpy, ctx, nv12);
	if (st == VA_STATUS_SUCCESS)
		st = VA.RenderPicture(dpy, ctx, &pbuf, 1);
	if (st == VA_STATUS_SUCCESS)
		st = VA.EndPicture(dpy, ctx);
	if (st == VA_STATUS_SUCCESS)
		st = VA.SyncSurface(dpy, nv12);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("va: VPP blit: %s\n", VA.ErrorStr(st));
		rc = -11;
		goto done;
	}

	st = VA.DeriveImage(dpy, nv12, &img);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("va: derive NV12: %s\n", VA.ErrorStr(st));
		rc = -12;
		goto done;
	}
	unsigned char *base = NULL;
	st = VA.MapBuffer(dpy, img.buf, (void **)&base);
	if (st != VA_STATUS_SUCCESS || !base) {
		VDBG("va: map NV12: %s\n", VA.ErrorStr(st));
		rc = -13;
		goto done;
	}
	{
		int s = (w < h ? w : h) / 8, q = s / 2;
		int cx[4] = {q, w - 1 - q, q, w - 1 - q};
		int cy[4] = {q, q, h - 1 - q, h - 1 - q};
		for (int i = 0; i < 4; i++) {
			int x = cx[i], y = cy[i];
			int Y = base[img.offsets[0] + (size_t)y * img.pitches[0] + x];
			int uvx = (x / 2) * 2;
			int U = base[img.offsets[1] + (size_t)(y / 2) * img.pitches[1] + uvx];
			int V = base[img.offsets[1] + (size_t)(y / 2) * img.pitches[1] + uvx + 1];
			yuv2rgb(Y, U, V, &corners[i * 3]);
		}
		rc = 0;
	}
	VA.UnmapBuffer(dpy, img.buf);

done:
	if (img.image_id != VA_INVALID_ID)
		VA.DestroyImage(dpy, img.image_id);
	if (pbuf != VA_INVALID_ID)
		VA.DestroyBuffer(dpy, pbuf);
	if (ctx != VA_INVALID_ID)
		VA.DestroyContext(dpy, ctx);
	if (cfg != VA_INVALID_ID)
		VA.DestroyConfig(dpy, cfg);
	if (nv12 != VA_INVALID_ID)
		VA.DestroySurfaces(dpy, &nv12, 1);
	if (rgb != VA_INVALID_ID)
		VA.DestroySurfaces(dpy, &rgb, 1);
	VA.Terminate(dpy);
	close(fd);
	return rc;
}

/* ===================== H.264 encoder (NV12 -> Annex-B via VAEntrypointEncSlice) ================
 * Constrained Baseline, CAVLC, no B-frames, 1 reference (P refs the previous frame), CBR. radeonsi
 * requires the app to supply packed SPS/PPS/slice headers; the driver inserts them + the slice data
 * into the coded buffer (Annex-B with start codes). We prepend an AUD so the framing matches the
 * ffmpeg path. All sizes validated; coded buffer bounded; mapped buffers unmapped in order. */

/* --- Annex-B bit writer with emulation-prevention ------------------------------------------- */
struct bs {
	unsigned char *p;
	int cap;
	int len;    /* bytes written */
	uint32_t acc; /* bit accumulator */
	int nbits;    /* bits in acc */
	int zeros;    /* trailing 00 run for emulation prevention */
};
static void bs_init(struct bs *b, unsigned char *buf, int cap) {
	b->p = buf;
	b->cap = cap;
	b->len = 0;
	b->acc = 0;
	b->nbits = 0;
	b->zeros = 0;
}
static void bs_startcode(struct bs *b) { /* 00 00 00 01, no EP */
	if (b->len + 4 <= b->cap) {
		b->p[b->len++] = 0;
		b->p[b->len++] = 0;
		b->p[b->len++] = 0;
		b->p[b->len++] = 1;
	}
	b->zeros = 0;
}
static void bs_putbyte_ep(struct bs *b, unsigned char v) {
	if (b->zeros >= 2 && v <= 3) {
		if (b->len < b->cap)
			b->p[b->len++] = 3;
		b->zeros = 0;
	}
	if (b->len < b->cap)
		b->p[b->len++] = v;
	b->zeros = (v == 0) ? b->zeros + 1 : 0;
}
static void bs_bits(struct bs *b, uint32_t val, int n) {
	for (int i = n - 1; i >= 0; i--) {
		b->acc = (b->acc << 1) | ((val >> i) & 1);
		if (++b->nbits == 8) {
			bs_putbyte_ep(b, (unsigned char)b->acc);
			b->acc = 0;
			b->nbits = 0;
		}
	}
}
static void bs_ue(struct bs *b, uint32_t v) {
	uint32_t x = v + 1;
	int nb = 0;
	while (x >> nb)
		nb++;
	bs_bits(b, 0, nb - 1);
	bs_bits(b, x, nb);
}
static void bs_se(struct bs *b, int32_t v) {
	uint32_t u = v <= 0 ? (uint32_t)(-2 * v) : (uint32_t)(2 * v - 1);
	bs_ue(b, u);
}
static void bs_trailing(struct bs *b) { /* rbsp_stop_one_bit + align */
	bs_bits(b, 1, 1);
	while (b->nbits != 0)
		bs_bits(b, 0, 1);
}

struct rva_enc {
	int fd;
	VADisplay dpy;
	int in_w, in_h, out_w, out_h, fps, kbps;
	int rc_dirty; /* kbps changed via rva_enc_set_bitrate: re-send the RC misc param next frame */
	int codec;    /* 0 = H.264, 1 = HEVC */
	/* closed-loop bitrate corrector: a 1s sliding window over emitted AU bits. radeonsi's RC (esp.
	 * HEVC) overshoots the requested budget by a content-dependent factor, so the shim scales the
	 * EFFECTIVE budget it sends the driver (rc_scale_pct) until the measured output lands on the real
	 * budget - a 1.4x driver overshoot converges to rc_scale ~72%. Driver-agnostic; bounds the rate. */
	int rc_scale_pct;     /* effective budget = kbps * rc_scale_pct/100 (0 => codec default) */
	long long win_bits;   /* AU bits accumulated in the current ~1s window */
	int win_frames;
	/* constant-quality-with-ceiling (adaptive budget). quality_mode => the enc config is VA_RC_CQP and
	 * the frame QP is cur_qp (not driver rate control): hold cur_qp == target_qp so simple content
	 * undershoots, and raise cur_qp toward 51 (via the sub-window ceiling controller) only when the 1s
	 * rate would exceed the kbps ceiling. rate_limited = cur_qp > target_qp. */
	int quality_mode;     /* 1 = CQP constant-quality; 0 = legacy CBR */
	int target_qp;        /* quality floor (medialink.QualityToQP); cur_qp never drops below it */
	int cur_qp;           /* current CQP (>= target_qp); encoded into pic_init_qp + the packed PPS */
	long long qc_bits;    /* bits emitted in the current sub-window (~250ms) */
	int qc_frames;        /* frames in the current sub-window */
	int qc_pframes;       /* non-IDR frames in it (qc_bits counts only these) */
	long long qc_idr_bits; /* last IDR's bits, amortized over the GOP by the controller input */
	int qc_sub;           /* frames per sub-window (~fps/4): raise the ceiling fast, release slow */
	int qc_under;         /* consecutive comfortable sub-windows (slow-release hysteresis streak) */
	int rate_limited;     /* last controller verdict (cur_qp > target_qp): reported in feedback */
	int mbw, mbh;
	int log2_max_fn;
	/* VPP (rgb import -> nv12 scale/convert) */
	VAConfigID vpp_cfg;
	VAContextID vpp_ctx;
	VASurfaceID nv12[2]; /* VPP output = encoder input (ping-pong for pipelining) */
	/* encoder */
	VAConfigID enc_cfg;
	VAContextID enc_ctx;
	VASurfaceID recon[2];
	VABufferID coded[2];
	int frame_num;   /* H264 frame_num (mod 2^log2_max_fn) */
	int pp;          /* ping-pong index (nv12/recon/coded) */
	int have_ref;
	int idr_id;
	int gop;         /* IDR interval in frames */
	int since_idr;
	/* pipeline: submit frame N, emit frame N-1 (sync+map), to overlap GPU encode with CPU prep. */
	int have_pending;
	int pend_idx;
	int pend_idr;
	int last_idx;  /* nv12[] holding the last real frame's VPP output (rva_enc_repeat source) */
	int have_last;
	/* per-call step timings (us; -1 = step not run this call) for the shim's step histograms */
	long long st_us[RVA_NSTEP];
	int vpp_filter;  /* VA_FILTER_SCALING_* for VPP (fast when not scaling; RPW_VPP_FILTER overrides) */
	int fast_scale;  /* shim auto-cap overload step: FAST VPP downscale */
	int vpp_sync;    /* RPW_VPP_SYNC (diag): sync VPP before encode submit to time the GFX colour convert */
	int qlevel;      /* VAEncMiscParameterTypeQualityLevel (0 = driver default; RPW_ENC_QL overrides) */
	int qrange;      /* driver's VAConfigAttribEncQualityRange */
	unsigned char *au; /* returned AU buffer (AUD + coded) */
	int au_cap;
	/* import-surface cache: the PipeWire pool cycles a fixed set of dmabuf fds; create a VASurface
	 * per fd once + reuse (per-frame vaCreateSurfaces import was the ~55fps-at-4K bottleneck + a slow
	 * RSS creep). Keyed by plane-0 fd AND its dmabuf inode: after a producer restart the new pool's fds
	 * reuse the old numbers, and an fd-only key would encode the OLD buffer forever (frozen picture). */
	int cache_fd[16];
	unsigned long long cache_ino[16];
	VASurfaceID cache_surf[16];
	int cache_n;
	int cache_next; /* round-robin eviction slot when full */
};

/* enc_init_qp is the init QP written into the PPS and the picture param: the live cur_qp in quality
 * (CQP) mode, the fixed 26 reference in legacy (CBR) mode (the driver deltas from 26 itself). */
static int enc_init_qp(struct rva_enc *e) { return e->quality_mode ? e->cur_qp : 26; }

static int level_for(int w, int h, int fps) {
	long mbs = (long)(w / 16) * (h / 16) * fps;
	if (w <= 1920 && h <= 1088 && fps <= 60)
		return 42; /* 4.2 covers 1080p60 */
	(void)mbs;
	return 52; /* 5.2 covers 4K60 */
}

/* write_sps/pps/slice: Annex-B NALs into b (with start code + EP). */
static void write_sps(struct bs *b, struct rva_enc *e) {
	bs_startcode(b);
	bs_bits(b, 0, 1);       /* forbidden_zero */
	bs_bits(b, 3, 2);       /* nal_ref_idc */
	bs_bits(b, 7, 5);       /* nal_unit_type = SPS */
	bs_bits(b, 66, 8);      /* profile_idc = Constrained Baseline/Baseline */
	bs_bits(b, 1, 1);       /* constraint_set0 */
	bs_bits(b, 1, 1);       /* constraint_set1 (constrained baseline) */
	bs_bits(b, 0, 1);       /* set2 */
	bs_bits(b, 0, 1);       /* set3 */
	bs_bits(b, 0, 1);       /* set4 */
	bs_bits(b, 0, 1);       /* set5 */
	bs_bits(b, 0, 2);       /* reserved_zero_2bits */
	bs_bits(b, (uint32_t)level_for(e->out_w, e->out_h, e->fps), 8);
	bs_ue(b, 0);            /* seq_parameter_set_id */
	bs_ue(b, (uint32_t)(e->log2_max_fn - 4)); /* log2_max_frame_num_minus4 */
	bs_ue(b, 2);            /* pic_order_cnt_type = 2 */
	bs_ue(b, 1);            /* max_num_ref_frames */
	bs_bits(b, 0, 1);       /* gaps_in_frame_num_value_allowed */
	bs_ue(b, (uint32_t)(e->mbw - 1)); /* pic_width_in_mbs_minus1 */
	bs_ue(b, (uint32_t)(e->mbh - 1)); /* pic_height_in_map_units_minus1 */
	bs_bits(b, 1, 1);       /* frame_mbs_only_flag */
	bs_bits(b, 1, 1);       /* direct_8x8_inference_flag */
	int crop = (e->mbh * 16) - e->out_h; /* bottom crop in luma samples */
	if (crop > 0) {
		bs_bits(b, 1, 1);   /* frame_cropping_flag */
		bs_ue(b, 0);        /* left */
		bs_ue(b, 0);        /* right */
		bs_ue(b, 0);        /* top */
		bs_ue(b, (uint32_t)(crop / 2)); /* bottom (chroma 4:2:0 -> /2) */
	} else {
		bs_bits(b, 0, 1);
	}
	bs_bits(b, 0, 1);       /* vui_parameters_present_flag */
	bs_trailing(b);
}
static void write_pps(struct bs *b, struct rva_enc *e) {
	bs_startcode(b);
	bs_bits(b, 0, 1);
	bs_bits(b, 3, 2);
	bs_bits(b, 8, 5);       /* nal_unit_type = PPS */
	bs_ue(b, 0);            /* pic_parameter_set_id */
	bs_ue(b, 0);            /* seq_parameter_set_id */
	bs_bits(b, 0, 1);       /* entropy_coding_mode_flag = CAVLC */
	bs_bits(b, 0, 1);       /* bottom_field_pic_order_in_frame_present */
	bs_ue(b, 0);            /* num_slice_groups_minus1 */
	bs_ue(b, 0);            /* num_ref_idx_l0_default_active_minus1 */
	bs_ue(b, 0);            /* num_ref_idx_l1_default_active_minus1 */
	bs_bits(b, 0, 1);       /* weighted_pred_flag */
	bs_bits(b, 0, 2);       /* weighted_bipred_idc */
	bs_se(b, enc_init_qp(e) - 26); /* pic_init_qp_minus26 (quality mode: the live CQP value; else 26) */
	bs_se(b, 0);            /* pic_init_qs_minus26 */
	bs_se(b, 0);            /* chroma_qp_index_offset */
	bs_bits(b, 0, 1);       /* deblocking_filter_control_present */
	bs_bits(b, 0, 1);       /* constrained_intra_pred */
	bs_bits(b, 0, 1);       /* redundant_pic_cnt_present */
	bs_trailing(b);
}
static void write_slice_hdr(struct bs *b, struct rva_enc *e, int idr) {
	bs_startcode(b);
	bs_bits(b, 0, 1);
	bs_bits(b, idr ? 3 : 2, 2); /* nal_ref_idc */
	bs_bits(b, idr ? 5 : 1, 5); /* nal_unit_type: 5=IDR,1=non-IDR */
	bs_ue(b, 0);                /* first_mb_in_slice */
	bs_ue(b, idr ? 7 : 5);      /* slice_type: 7=I(all),5=P(all) */
	bs_ue(b, 0);                /* pic_parameter_set_id */
	bs_bits(b, (uint32_t)(e->frame_num & ((1 << e->log2_max_fn) - 1)), e->log2_max_fn); /* frame_num */
	if (idr)
		bs_ue(b, (uint32_t)e->idr_id); /* idr_pic_id */
	/* pic_order_cnt_type==2 -> no POC syntax */
	if (!idr) {
		bs_bits(b, 0, 1);       /* num_ref_idx_active_override_flag */
		bs_bits(b, 0, 1);       /* ref_pic_list_modification_flag_l0 */
	}
	if (idr) {
		bs_bits(b, 0, 1);       /* no_output_of_prior_pics_flag */
		bs_bits(b, 0, 1);       /* long_term_reference_flag */
	} else {
		bs_bits(b, 0, 1);       /* adaptive_ref_pic_marking_mode_flag */
	}
	bs_se(b, 0);                /* slice_qp_delta */
	/* deblocking_filter_control_present==0 -> no deblocking syntax. Driver appends slice data. */
}

/* ---- HEVC (H.265) Main-profile bitstream writers ------------------------------------------------
 * Main profile, 4:2:0 8-bit, no scaling lists, no PCM, no SAO, one short-term ref (previous frame),
 * no B-frames. radeonsi requires app-written VPS/SPS/PPS/slice packed headers, same as H.264. */

/* hevc_nal: 2-byte NAL header (forbidden_zero=0, nal_unit_type u6, nuh_layer_id u6=0, tid_plus1 u3=1). */
static void hevc_nal(struct bs *b, int type) {
	bs_startcode(b);
	bs_bits(b, 0, 1);              /* forbidden_zero_bit */
	bs_bits(b, (uint32_t)type, 6); /* nal_unit_type */
	bs_bits(b, 0, 6);              /* nuh_layer_id */
	bs_bits(b, 1, 3);              /* nuh_temporal_id_plus1 */
}

/* write_ptl: profile_tier_level for max_sub_layers_minus1 == 0 (no sub-layers). Main profile. */
static void write_ptl(struct bs *b, int level_x30) {
	bs_bits(b, 0, 2);  /* general_profile_space */
	bs_bits(b, 0, 1);  /* general_tier_flag (main tier) */
	bs_bits(b, 1, 5);  /* general_profile_idc = 1 (Main) */
	for (int i = 0; i < 32; i++) /* general_profile_compatibility_flag[i]: only Main (i==1) */
		bs_bits(b, i == 1 ? 1 : 0, 1);
	bs_bits(b, 1, 1);  /* general_progressive_source_flag */
	bs_bits(b, 0, 1);  /* general_interlaced_source_flag */
	bs_bits(b, 0, 1);  /* general_non_packed_constraint_flag */
	bs_bits(b, 1, 1);  /* general_frame_only_constraint_flag */
	bs_bits(b, 0, 32); /* general_reserved_zero_43bits [43] + general_reserved_zero_bit [1] = 44 */
	bs_bits(b, 0, 12);
	bs_bits(b, (uint32_t)level_x30, 8); /* general_level_idc (30 x level number) */
}

/* hevc_level_x30: general_level_idc = 30 x level. 1080p60 -> 4.1 (123); 4K60 -> 5.1 (153). */
static int hevc_level_x30(int w, int h, int fps) {
	long lumaRate = (long)w * h * (fps > 0 ? fps : 60);
	if (lumaRate <= 1920L * 1088 * 60)
		return 123; /* 4.1 */
	if (lumaRate <= 3840L * 2160 * 60)
		return 153; /* 5.1 */
	return 156;     /* 5.2 */
}

static void write_vps_hevc(struct bs *b, struct rva_enc *e) {
	hevc_nal(b, 32); /* VPS */
	bs_bits(b, 0, 4);     /* vps_video_parameter_set_id */
	bs_bits(b, 3, 2);     /* vps_base_layer_internal_flag=1, vps_base_layer_available_flag=1 */
	bs_bits(b, 0, 6);     /* vps_max_layers_minus1 */
	bs_bits(b, 0, 3);     /* vps_max_sub_layers_minus1 */
	bs_bits(b, 1, 1);     /* vps_temporal_id_nesting_flag */
	bs_bits(b, 0xffff, 16); /* vps_reserved_0xffff_16bits */
	write_ptl(b, hevc_level_x30(e->out_w, e->out_h, e->fps));
	bs_bits(b, 1, 1);     /* vps_sub_layer_ordering_info_present_flag */
	bs_ue(b, 1);          /* vps_max_dec_pic_buffering_minus1[0] (>= num refs+1) */
	bs_ue(b, 0);          /* vps_max_num_reorder_pics[0] (no reorder) */
	bs_ue(b, 0);          /* vps_max_latency_increase_plus1[0] */
	bs_bits(b, 0, 6);     /* vps_max_layer_id */
	bs_ue(b, 0);          /* vps_num_layer_sets_minus1 */
	bs_bits(b, 0, 1);     /* vps_timing_info_present_flag */
	bs_bits(b, 0, 1);     /* vps_extension_flag */
	bs_trailing(b);
}

static void write_sps_hevc(struct bs *b, struct rva_enc *e) {
	hevc_nal(b, 33); /* SPS */
	bs_bits(b, 0, 4);     /* sps_video_parameter_set_id */
	bs_bits(b, 0, 3);     /* sps_max_sub_layers_minus1 */
	bs_bits(b, 1, 1);     /* sps_temporal_id_nesting_flag */
	write_ptl(b, hevc_level_x30(e->out_w, e->out_h, e->fps));
	bs_ue(b, 0);          /* sps_seq_parameter_set_id */
	bs_ue(b, 1);          /* chroma_format_idc = 1 (4:2:0) */
	bs_ue(b, (uint32_t)e->out_w); /* pic_width_in_luma_samples */
	bs_ue(b, (uint32_t)e->out_h); /* pic_height_in_luma_samples */
	bs_bits(b, 0, 1);     /* conformance_window_flag (dims are multiples of 8 at our sizes) */
	bs_ue(b, 0);          /* bit_depth_luma_minus8 */
	bs_ue(b, 0);          /* bit_depth_chroma_minus8 */
	bs_ue(b, 4);          /* log2_max_pic_order_cnt_lsb_minus4 => 8-bit POC lsb */
	bs_bits(b, 1, 1);     /* sps_sub_layer_ordering_info_present_flag */
	bs_ue(b, 1);          /* sps_max_dec_pic_buffering_minus1[0] */
	bs_ue(b, 0);          /* sps_max_num_reorder_pics[0] */
	bs_ue(b, 0);          /* sps_max_latency_increase_plus1[0] */
	bs_ue(b, 0);          /* log2_min_luma_coding_block_size_minus3 (min CU 8) */
	bs_ue(b, 3);          /* log2_diff_max_min_luma_coding_block_size (max CTB 64) */
	bs_ue(b, 0);          /* log2_min_luma_transform_block_size_minus2 (min TU 4) */
	bs_ue(b, 3);          /* log2_diff_max_min_luma_transform_block_size (max TU 32) */
	bs_ue(b, 2);          /* max_transform_hierarchy_depth_inter */
	bs_ue(b, 2);          /* max_transform_hierarchy_depth_intra */
	bs_bits(b, 0, 1);     /* scaling_list_enabled_flag */
	bs_bits(b, 0, 1);     /* amp_enabled_flag */
	bs_bits(b, 0, 1);     /* sample_adaptive_offset_enabled_flag */
	bs_bits(b, 0, 1);     /* pcm_enabled_flag */
	bs_ue(b, 1);          /* num_short_term_ref_pic_sets = 1 */
	/* st_ref_pic_set(0): 1 negative pic, delta_poc = -1, used by curr */
	bs_ue(b, 1);          /* num_negative_pics */
	bs_ue(b, 0);          /* num_positive_pics */
	bs_ue(b, 0);          /* delta_poc_s0_minus1[0] => -1 */
	bs_bits(b, 1, 1);     /* used_by_curr_pic_s0_flag[0] */
	bs_bits(b, 0, 1);     /* long_term_ref_pics_present_flag */
	bs_bits(b, 0, 1);     /* sps_temporal_mvp_enabled_flag */
	bs_bits(b, 0, 1);     /* strong_intra_smoothing_enabled_flag */
	bs_bits(b, 0, 1);     /* vui_parameters_present_flag */
	bs_bits(b, 0, 1);     /* sps_extension_present_flag */
	bs_trailing(b);
}

static void write_pps_hevc(struct bs *b, struct rva_enc *e) {
	hevc_nal(b, 34); /* PPS */
	bs_ue(b, 0);          /* pps_pic_parameter_set_id */
	bs_ue(b, 0);          /* pps_seq_parameter_set_id */
	bs_bits(b, 0, 1);     /* dependent_slice_segments_enabled_flag */
	bs_bits(b, 0, 1);     /* output_flag_present_flag */
	bs_bits(b, 0, 3);     /* num_extra_slice_header_bits */
	bs_bits(b, 0, 1);     /* sign_data_hiding_enabled_flag */
	bs_bits(b, 0, 1);     /* cabac_init_present_flag */
	bs_ue(b, 0);          /* num_ref_idx_l0_default_active_minus1 */
	bs_ue(b, 0);          /* num_ref_idx_l1_default_active_minus1 */
	bs_se(b, enc_init_qp(e) - 26); /* init_qp_minus26 (quality mode: the live CQP value; else 26) */
	bs_bits(b, 0, 1);     /* constrained_intra_pred_flag */
	bs_bits(b, 0, 1);     /* transform_skip_enabled_flag */
	bs_bits(b, 1, 1);     /* cu_qp_delta_enabled_flag=1: let the RC vary QP per-CU (bounds the bitrate;
	                       * without it HEVC is locked at init_qp and NO rate control works) */
	bs_ue(b, 0);          /* diff_cu_qp_delta_depth = 0 (QP delta at CTB granularity) */
	bs_se(b, 0);          /* pps_cb_qp_offset */
	bs_se(b, 0);          /* pps_cr_qp_offset */
	bs_bits(b, 0, 1);     /* pps_slice_chroma_qp_offsets_present_flag */
	bs_bits(b, 0, 1);     /* weighted_pred_flag */
	bs_bits(b, 0, 1);     /* weighted_bipred_flag */
	bs_bits(b, 0, 1);     /* transquant_bypass_enabled_flag */
	bs_bits(b, 0, 1);     /* tiles_enabled_flag */
	bs_bits(b, 0, 1);     /* entropy_coding_sync_enabled_flag */
	bs_bits(b, 0, 1);     /* pps_loop_filter_across_slices_enabled_flag=0 (single slice => no slice-level flag) */
	bs_bits(b, 0, 1);     /* deblocking_filter_control_present_flag */
	bs_bits(b, 0, 1);     /* pps_scaling_list_data_present_flag */
	bs_bits(b, 0, 1);     /* lists_modification_present_flag */
	bs_ue(b, 0);          /* log2_parallel_merge_level_minus2 */
	bs_bits(b, 0, 1);     /* slice_segment_header_extension_present_flag */
	bs_bits(b, 0, 1);     /* pps_extension_present_flag */
	bs_trailing(b);
}

/* write_slice_hdr_hevc: slice_segment_header. idr => IDR_W_RADL (19), else TRAIL_R (1). poc = frames
 * since the IDR (no B => encode order == display order). */
static void write_slice_hdr_hevc(struct bs *b, struct rva_enc *e, int idr) {
	int nal = idr ? 19 : 1; /* IDR_W_RADL : TRAIL_R */
	hevc_nal(b, nal);
	bs_bits(b, 1, 1);     /* first_slice_segment_in_pic_flag */
	/* IDR uses nal_unit_type in [16..23] => no_output_of_prior_pics_flag */
	if (idr)
		bs_bits(b, 0, 1); /* no_output_of_prior_pics_flag */
	bs_ue(b, 0);          /* slice_pic_parameter_set_id */
	/* dependent_slice_segments disabled + first slice => no slice_segment_address */
	bs_ue(b, idr ? 2 : 1); /* slice_type: 2=I, 1=P */
	if (!idr) {
		bs_bits(b, (uint32_t)(e->since_idr & 0xff), 8); /* slice_pic_order_cnt_lsb (log2=8) */
		bs_bits(b, 1, 1); /* short_term_ref_pic_set_sps_flag (use SPS set 0; num==1 => no idx) */
		/* sps_temporal_mvp_enabled_flag==0 => no slice_temporal_mvp_enabled_flag */
		bs_bits(b, 0, 1); /* num_ref_idx_active_override_flag (use PPS defaults: 1 ref) */
		/* lists_modification_present_flag==0 => no ref_pic_list_modification */
		/* no mvd_l1 (P slice), cabac_init_present==0 => none */
		bs_ue(b, 0);      /* five_minus_max_num_merge_cand => max_num_merge_cand = 5 */
	}
	bs_se(b, 0);          /* slice_qp_delta */
	/* pps_loop_filter_across_slices_enabled_flag==0 => no slice_loop_filter_across_slices flag.
	 * deblocking control absent => none. byte_alignment: */
	bs_bits(b, 1, 1);     /* alignment_bit_equal_to_one */
	while (b->nbits != 0)
		bs_bits(b, 0, 1); /* alignment_bit_equal_to_zero */
}

/* import_cached returns a VASurface for the dmabuf, reusing one created earlier for the same
 * plane-0 fd (the pool cycles a fixed fd set). Cache full / miss on a fresh fd => import + store. */
static VASurfaceID import_cached(struct rva_enc *e, uint32_t drm_fourcc, uint64_t modifier, int w, int h,
                                 int n_planes, const int *fds, const unsigned *offsets, const unsigned *strides) {
	struct stat sb;
	unsigned long long ino = fstat(fds[0], &sb) == 0 ? (unsigned long long)sb.st_ino : 0;
	int slot = -1;
	for (int i = 0; i < e->cache_n; i++) {
		if (e->cache_fd[i] != fds[0])
			continue;
		if (e->cache_ino[i] == ino)
			return e->cache_surf[i];
		slot = i; /* same fd number, different buffer (producer restarted): re-import into this slot */
		break;
	}
	VASurfaceID s = VA_INVALID_ID;
	VAStatus st = import_dmabuf(e->dpy, drm_fourcc, modifier, w, h, n_planes, fds, offsets, strides, &s);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("enc: import: %s\n", VA.ErrorStr(st));
		return VA_INVALID_ID;
	}
	const int capn = (int)(sizeof(e->cache_fd) / sizeof(e->cache_fd[0]));
	int evict = slot >= 0;
	if (slot < 0) {
		if (e->cache_n < capn) {
			slot = e->cache_n++;
		} else { /* full: evict round-robin (bounded; never leaks) */
			slot = e->cache_next;
			e->cache_next = (e->cache_next + 1) % capn;
			evict = 1;
		}
	}
	/* stale import: the driver refcounts the underlying resource, so an in-flight VPP read stays valid */
	if (evict && e->cache_surf[slot] != VA_INVALID_ID)
		VA.DestroySurfaces(e->dpy, &e->cache_surf[slot], 1);
	e->cache_fd[slot] = fds[0];
	e->cache_ino[slot] = ino;
	e->cache_surf[slot] = s;
	return s;
}

/* vpp_to_nv12: import (cached) the dmabuf, VPP (convert + optional vertical mirror + scale) into
 * e->nv12. flip=1 mirrors vertically (a bottom-up GL producer); flip=0 keeps orientation (a top-down
 * DX/Spout producer). Same decision as the readback path (shim want_flip), so both agree.
 * No explicit VPP sync: the encode's BeginPicture(nv12) + SyncSurface order the dependency on the
 * shared nv12 surface, which lets VPP and encode pipeline. 0=ok. */
static int vpp_to_nv12(struct rva_enc *e, VASurfaceID target, uint32_t drm_fourcc, uint64_t modifier, int w, int h,
                       int n_planes, const int *fds, const unsigned *offsets, const unsigned *strides, int flip) {
	long long t0 = now_us();
	VASurfaceID rgb = import_cached(e, drm_fourcc, modifier, w, h, n_planes, fds, offsets, strides);
	e->st_us[RVA_ST_IMPORT] = now_us() - t0;
	if (rgb == VA_INVALID_ID)
		return -1;
	VAProcPipelineParameterBuffer pp;
	memset(&pp, 0, sizeof(pp));
	pp.surface = rgb;
	/* HQ by default (measured cheaper than FAST at 1:1 on radeonsi under game load); FAST only for an
	 * overload downscale (1440p: GPU VPP mean 0.40 ms FAST vs 2.22 ms HQ) */
	pp.filter_flags = e->vpp_filter >= 0 ? (unsigned)e->vpp_filter
	                 : (e->fast_scale && (w != e->out_w || h != e->out_h)) ? VA_FILTER_SCALING_FAST : VA_FILTER_SCALING_HQ;
	pp.mirror_state = flip ? VA_MIRROR_VERTICAL : VA_MIRROR_NONE;
	VABufferID pbuf = VA_INVALID_ID;
	int rc = -2;
	VAStatus st = VA.CreateBuffer(e->dpy, e->vpp_ctx, VAProcPipelineParameterBufferType, sizeof(pp), 1, &pp, &pbuf);
	if (st == VA_STATUS_SUCCESS)
		st = VA.BeginPicture(e->dpy, e->vpp_ctx, target);
	if (st == VA_STATUS_SUCCESS)
		st = VA.RenderPicture(e->dpy, e->vpp_ctx, &pbuf, 1);
	if (st == VA_STATUS_SUCCESS)
		st = VA.EndPicture(e->dpy, e->vpp_ctx);
	if (st != VA_STATUS_SUCCESS)
		VDBG("enc: vpp: %s\n", VA.ErrorStr(st));
	else
		rc = 0;
	if (pbuf != VA_INVALID_ID)
		VA.DestroyBuffer(e->dpy, pbuf);
	return rc;
}

rva_enc *rva_enc_new(const char *node, int codec, int out_w, int out_h, int fps, int bitrate_kbps, int quality_qp) {
	if (!va_load() || out_w < 16 || out_h < 16 || out_w > 8192 || out_h > 8192)
		return NULL;
	if (codec != 0 && codec != 1) /* 0 = H.264, 1 = HEVC */
		return NULL;
	struct rva_enc *e = calloc(1, sizeof(*e));
	if (!e)
		return NULL;
	e->codec = codec;
	e->rc_scale_pct = codec == 1 ? 68 : 90; /* HEVC: pre-compensate radeonsi's ~1.4x RC overshoot */
	/* constant-quality-with-ceiling: quality_qp>0 => CQP at that QP (the floor), ceiling = bitrate. */
	if (quality_qp > 0) {
		e->quality_mode = 1;
		if (quality_qp > 51)
			quality_qp = 51;
		e->target_qp = quality_qp;
		e->cur_qp = quality_qp;
	}
	e->fd = open(node, O_RDWR | O_CLOEXEC);
	if (e->fd < 0) {
		free(e);
		return NULL;
	}
	e->out_w = out_w;
	e->out_h = out_h;
	e->fps = fps > 0 ? fps : 60;
	e->qc_sub = e->fps / 4 > 0 ? e->fps / 4 : 1; /* ~4 ceiling checks/s: fast raise, slow release */
	e->kbps = bitrate_kbps > 0 ? bitrate_kbps : 20000;
	e->mbw = (out_w + 15) / 16;
	e->mbh = (out_h + 15) / 16;
	e->log2_max_fn = 4;
	e->gop = e->fps * 2;
	/* VPP filter: HQ scaling only matters when scaling; a 1:1 colour convert takes the FAST path (less
	 * GFX time - the VPP runs on the shader queue the games are saturating). */
	e->vpp_filter = -1; /* auto: HQ (fast_scale: FAST when downscaling) */
	{
		const char *vf = getenv("RPW_VPP_FILTER");
		if (vf && !strcmp(vf, "fast"))
			e->vpp_filter = VA_FILTER_SCALING_FAST;
		else if (vf && !strcmp(vf, "default"))
			e->vpp_filter = VA_FILTER_SCALING_DEFAULT;
		else if (vf && !strcmp(vf, "hq"))
			e->vpp_filter = VA_FILTER_SCALING_HQ;
	}
	e->vpp_sync = getenv("RPW_VPP_SYNC") != NULL;
	{
		const char *ql = getenv("RPW_ENC_QL");
		e->qlevel = ql ? atoi(ql) : 0;
	}
	e->coded[0] = e->coded[1] = VA_INVALID_ID;
	e->vpp_cfg = e->enc_cfg = VA_INVALID_ID;
	e->vpp_ctx = e->enc_ctx = VA_INVALID_ID;
	e->nv12[0] = e->nv12[1] = e->recon[0] = e->recon[1] = VA_INVALID_ID;

	int maj, min;
	e->dpy = VA.GetDisplayDRM(e->fd);
	if (!e->dpy || VA.Initialize(e->dpy, &maj, &min) != VA_STATUS_SUCCESS) {
		VDBG("enc: va init failed\n");
		goto fail;
	}
	VASurfaceAttrib fa;
	memset(&fa, 0, sizeof(fa));
	fa.type = VASurfaceAttribPixelFormat;
	fa.flags = VA_SURFACE_ATTRIB_SETTABLE;
	fa.value.type = VAGenericValueTypeInteger;
	fa.value.value.i = VA_FOURCC_NV12;
	if (VA.CreateSurfaces(e->dpy, VA_RT_FORMAT_YUV420, out_w, out_h, e->nv12, 2, &fa, 1) != VA_STATUS_SUCCESS)
		goto fail;
	if (VA.CreateSurfaces(e->dpy, VA_RT_FORMAT_YUV420, out_w, out_h, e->recon, 2, &fa, 1) != VA_STATUS_SUCCESS)
		goto fail;
	if (VA.CreateConfig(e->dpy, VAProfileNone, VAEntrypointVideoProc, NULL, 0, &e->vpp_cfg) != VA_STATUS_SUCCESS)
		goto fail;
	if (VA.CreateContext(e->dpy, e->vpp_cfg, out_w, out_h, VA_PROGRESSIVE, e->nv12, 2, &e->vpp_ctx) != VA_STATUS_SUCCESS)
		goto fail;

	VAConfigAttrib ea[3];
	ea[0].type = VAConfigAttribRTFormat;
	ea[0].value = VA_RT_FORMAT_YUV420;
	ea[1].type = VAConfigAttribRateControl;
	/* Quality mode: VA_RC_CQP - the one RC mode radeonsi honours exactly (QP comes straight from the
	 * pic param, no driver rate seeking), so constant-quality + the software ceiling controller are
	 * reliable. Legacy mode: CBR holds the average at the budget directly (radeonsi VBR, esp. HEVC,
	 * overshoots 1.6-1.8x and does not honour min_qp as a ceiling lever); disable_bit_stuffing (set in
	 * append_rc_misc) keeps static content UNDER the budget instead of padding it. */
	ea[1].value = e->quality_mode ? VA_RC_CQP : VA_RC_CBR;
	ea[2].type = VAConfigAttribEncPackedHeaders;
	ea[2].value = VA_ENC_PACKED_HEADER_SEQUENCE | VA_ENC_PACKED_HEADER_PICTURE | VA_ENC_PACKED_HEADER_SLICE;
	VAProfile encProfile = (codec == 1) ? VAProfileHEVCMain : VAProfileH264ConstrainedBaseline;
	if (VA.CreateConfig(e->dpy, encProfile, VAEntrypointEncSlice, ea, 3, &e->enc_cfg) != VA_STATUS_SUCCESS) {
		VDBG("enc: create enc config failed\n");
		goto fail;
	}
	VASurfaceID ctx_targets[4] = {e->nv12[0], e->nv12[1], e->recon[0], e->recon[1]};
	if (VA.CreateContext(e->dpy, e->enc_cfg, out_w, out_h, VA_PROGRESSIVE, ctx_targets, 4, &e->enc_ctx) != VA_STATUS_SUCCESS) {
		VDBG("enc: create enc context failed\n");
		goto fail;
	}
	unsigned coded_sz = (unsigned)out_w * out_h * 3 / 2 + (1u << 16);
	if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncCodedBufferType, coded_sz, 1, NULL, &e->coded[0]) != VA_STATUS_SUCCESS)
		goto fail;
	if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncCodedBufferType, coded_sz, 1, NULL, &e->coded[1]) != VA_STATUS_SUCCESS)
		goto fail;
	e->au_cap = (int)coded_sz + 16;
	e->au = malloc(e->au_cap);
	if (!e->au)
		goto fail;
	VDBG("enc: ready %dx%d @%d %dkbps\n", out_w, out_h, e->fps, e->kbps);
	return e;
fail:
	rva_enc_free(e);
	return NULL;
}

/* append_rc_misc adds the rate-control + framerate + HRD misc param buffers (codec-agnostic). The
 * budget is a HARD CEILING for a congestion-safe link: bits_per_second (the VBR MAX) == budget, a ~1s
 * HRD/VBV so the peak cannot run away, max_qp so the RC can raise QP to hold the ceiling on complex
 * content, and disable_bit_stuffing so static content still UNDERSHOOTS (no CBR-style filler). If the
 * driver's VBR still overshoots, rva_enc_frame's closed-loop corrector (qp_ceiling) tightens it. */
static void append_rc_misc(struct rva_enc *e, VABufferID *bufs, int *pnb) {
	VAEncMiscParameterBuffer *mb;
	unsigned eff = (unsigned)e->kbps * 1000;
	if (e->rc_scale_pct > 0)
		eff = (unsigned)((long long)e->kbps * 1000 * e->rc_scale_pct / 100);
	/* Quality (CQP) mode: no bitrate RC / HRD param - the QP comes from the pic param and the ceiling
	 * is enforced in software (the sub-window controller). Only the frame rate is advertised. */
	VABufferID rcb;
	if (!e->quality_mode && VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncMiscParameterBufferType,
		sizeof(VAEncMiscParameterBuffer) + sizeof(VAEncMiscParameterRateControl), 1, NULL, &rcb) == VA_STATUS_SUCCESS &&
		VA.MapBuffer(e->dpy, rcb, (void **)&mb) == VA_STATUS_SUCCESS) {
		mb->type = VAEncMiscParameterTypeRateControl;
		VAEncMiscParameterRateControl *rc = (VAEncMiscParameterRateControl *)mb->data;
		memset(rc, 0, sizeof(*rc));
		rc->bits_per_second = eff;      /* CBR target == effective budget (corrector-scaled) */
		rc->target_percentage = 100;
		rc->window_size = 1000;
		rc->initial_qp = 26;
		rc->min_qp = 10;
		rc->max_qp = 51;
		rc->rc_flags.bits.disable_bit_stuffing = 1;     /* no padding: static content undershoots */
		VA.UnmapBuffer(e->dpy, rcb);
		bufs[(*pnb)++] = rcb;
	}
	VABufferID frb;
	if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncMiscParameterBufferType,
		sizeof(VAEncMiscParameterBuffer) + sizeof(VAEncMiscParameterFrameRate), 1, NULL, &frb) == VA_STATUS_SUCCESS &&
		VA.MapBuffer(e->dpy, frb, (void **)&mb) == VA_STATUS_SUCCESS) {
		mb->type = VAEncMiscParameterTypeFrameRate;
		VAEncMiscParameterFrameRate *fr = (VAEncMiscParameterFrameRate *)mb->data;
		memset(fr, 0, sizeof(*fr));
		fr->framerate = (unsigned)e->fps;
		VA.UnmapBuffer(e->dpy, frb);
		bufs[(*pnb)++] = frb;
	}
	VABufferID qlb;
	if (e->qlevel > 0 && VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncMiscParameterBufferType,
		sizeof(VAEncMiscParameterBuffer) + sizeof(VAEncMiscParameterBufferQualityLevel), 1, NULL, &qlb) == VA_STATUS_SUCCESS &&
		VA.MapBuffer(e->dpy, qlb, (void **)&mb) == VA_STATUS_SUCCESS) {
		mb->type = VAEncMiscParameterTypeQualityLevel;
		VAEncMiscParameterBufferQualityLevel *qq = (VAEncMiscParameterBufferQualityLevel *)mb->data;
		memset(qq, 0, sizeof(*qq));
		qq->quality_level = (unsigned)e->qlevel; /* 1 = best quality ... qrange = fastest */
		VA.UnmapBuffer(e->dpy, qlb);
		bufs[(*pnb)++] = qlb;
	}
	VABufferID hrdb;
	if (!e->quality_mode && VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncMiscParameterBufferType,
		sizeof(VAEncMiscParameterBuffer) + sizeof(VAEncMiscParameterHRD), 1, NULL, &hrdb) == VA_STATUS_SUCCESS &&
		VA.MapBuffer(e->dpy, hrdb, (void **)&mb) == VA_STATUS_SUCCESS) {
		mb->type = VAEncMiscParameterTypeHRD;
		VAEncMiscParameterHRD *hrd = (VAEncMiscParameterHRD *)mb->data;
		memset(hrd, 0, sizeof(*hrd));
		hrd->buffer_size = eff / 2;        /* ~0.5s VBV: caps 1s-window bursts tighter */
		hrd->initial_buffer_fullness = eff / 4;
		VA.UnmapBuffer(e->dpy, hrdb);
		bufs[(*pnb)++] = hrdb;
	}
}

/* build_hevc_bufs appends the HEVC seq (IDR) + picture + packed VPS/SPS/PPS (IDR) + packed slice
 * header + slice param buffers. Single slice, single short-term ref (previous frame), no B. */
static int build_hevc_bufs(struct rva_enc *e, int idx, int idr, VABufferID *bufs, int *pnb) {
	int cur = idx;
	int poc = e->since_idr; /* 0 at IDR, increments (no B => encode == display order) */
	VAStatus st;
	if (idr) {
		VAEncSequenceParameterBufferHEVC sp;
		memset(&sp, 0, sizeof(sp));
		sp.general_profile_idc = 1; /* Main */
		sp.general_level_idc = (uint8_t)hevc_level_x30(e->out_w, e->out_h, e->fps);
		sp.general_tier_flag = 0;
		sp.intra_period = (unsigned)e->gop;
		sp.intra_idr_period = (unsigned)e->gop;
		sp.ip_period = 1;
		sp.bits_per_second = (unsigned)e->kbps * 1000; /* ceiling == budget (HRD/VBV holds it) */
		sp.pic_width_in_luma_samples = (uint16_t)e->out_w;
		sp.pic_height_in_luma_samples = (uint16_t)e->out_h;
		sp.seq_fields.bits.chroma_format_idc = 1;
		sp.log2_min_luma_coding_block_size_minus3 = 0;    /* min CU 8 */
		sp.log2_diff_max_min_luma_coding_block_size = 3;  /* max CTB 64 */
		sp.log2_min_transform_block_size_minus2 = 0;      /* min TU 4 */
		sp.log2_diff_max_min_transform_block_size = 3;    /* max TU 32 */
		sp.max_transform_hierarchy_depth_inter = 2;
		sp.max_transform_hierarchy_depth_intra = 2;
		VABufferID sb;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncSequenceParameterBufferType, sizeof(sp), 1, &sp, &sb) != VA_STATUS_SUCCESS)
			return -3;
		bufs[(*pnb)++] = sb;
	}
	VAEncPictureParameterBufferHEVC pp;
	memset(&pp, 0, sizeof(pp));
	pp.decoded_curr_pic.picture_id = e->recon[cur];
	pp.decoded_curr_pic.pic_order_cnt = poc;
	for (int i = 0; i < 15; i++) {
		pp.reference_frames[i].picture_id = VA_INVALID_SURFACE;
		pp.reference_frames[i].flags = VA_PICTURE_HEVC_INVALID;
	}
	if (!idr && e->have_ref) {
		pp.reference_frames[0].picture_id = e->recon[cur ^ 1];
		pp.reference_frames[0].pic_order_cnt = poc - 1;
		pp.reference_frames[0].flags = VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE;
	}
	pp.coded_buf = e->coded[idx];
	pp.collocated_ref_pic_index = 0xff; /* temporal MVP off => no collocated */
	pp.last_picture = 0;
	pp.pic_init_qp = enc_init_qp(e); /* quality mode: the live CQP value; else the 26 CBR reference */
	pp.diff_cu_qp_delta_depth = 0;
	pp.nal_unit_type = (uint8_t)(idr ? 19 : 1); /* IDR_W_RADL : TRAIL_R */
	pp.pic_fields.bits.idr_pic_flag = idr ? 1 : 0;
	pp.pic_fields.bits.coding_type = idr ? 1 : 2; /* 1=I, 2=P */
	pp.pic_fields.bits.reference_pic_flag = 1;
	pp.pic_fields.bits.cu_qp_delta_enabled_flag = 1; /* must match the PPS: lets the RC bound the rate */
	VABufferID pb;
	if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPictureParameterBufferType, sizeof(pp), 1, &pp, &pb) != VA_STATUS_SUCCESS)
		return -4;
	bufs[(*pnb)++] = pb;
	unsigned char hdr[512];
	if (idr) { /* VPS+SPS = the Sequence packed header (IDR only) */
		struct bs b;
		bs_init(&b, hdr, sizeof(hdr));
		write_vps_hevc(&b, e);
		write_sps_hevc(&b, e);
		VAEncPackedHeaderParameterBuffer ph;
		memset(&ph, 0, sizeof(ph));
		ph.type = VAEncPackedHeaderSequence;
		ph.bit_length = (unsigned)b.len * 8;
		ph.has_emulation_bytes = 1;
		VABufferID phb, pdb;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderParameterBufferType, sizeof(ph), 1, &ph, &phb) == VA_STATUS_SUCCESS)
			bufs[(*pnb)++] = phb;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderDataBufferType, (unsigned)b.len, 1, hdr, &pdb) == VA_STATUS_SUCCESS)
			bufs[(*pnb)++] = pdb;
	}
	/* PPS = the Picture packed header. In quality mode re-emit it every frame (it carries the live CQP
	 * init_qp); in legacy mode only at IDR. ~12 bytes, negligible at 60fps. */
	if (idr || e->quality_mode) {
		struct bs b;
		bs_init(&b, hdr, sizeof(hdr));
		write_pps_hevc(&b, e);
		VAEncPackedHeaderParameterBuffer ph2;
		memset(&ph2, 0, sizeof(ph2));
		ph2.type = VAEncPackedHeaderPicture;
		ph2.bit_length = (unsigned)b.len * 8;
		ph2.has_emulation_bytes = 1;
		VABufferID phb2, pdb2;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderParameterBufferType, sizeof(ph2), 1, &ph2, &phb2) == VA_STATUS_SUCCESS)
			bufs[(*pnb)++] = phb2;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderDataBufferType, (unsigned)b.len, 1, hdr, &pdb2) == VA_STATUS_SUCCESS)
			bufs[(*pnb)++] = pdb2;
	}
	unsigned char sh[64];
	struct bs sb2;
	bs_init(&sb2, sh, sizeof(sh));
	write_slice_hdr_hevc(&sb2, e, idr); /* already byte-aligned (byte_alignment() in the writer) */
	VAEncPackedHeaderParameterBuffer sph;
	memset(&sph, 0, sizeof(sph));
	sph.type = VAEncPackedHeaderSlice;
	sph.bit_length = (unsigned)sb2.len * 8;
	sph.has_emulation_bytes = 1;
	VABufferID shpb, shdb;
	if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderParameterBufferType, sizeof(sph), 1, &sph, &shpb) == VA_STATUS_SUCCESS)
		bufs[(*pnb)++] = shpb;
	if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderDataBufferType, (unsigned)sb2.len, 1, sh, &shdb) == VA_STATUS_SUCCESS)
		bufs[(*pnb)++] = shdb;
	VAEncSliceParameterBufferHEVC sl;
	memset(&sl, 0, sizeof(sl));
	int ctbw = (e->out_w + 63) / 64, ctbh = (e->out_h + 63) / 64;
	sl.slice_segment_address = 0;
	sl.num_ctu_in_slice = (unsigned)(ctbw * ctbh);
	sl.slice_type = (uint8_t)(idr ? 2 : 1); /* 2=I, 1=P */
	sl.slice_pic_parameter_set_id = 0;
	sl.num_ref_idx_l0_active_minus1 = 0;
	sl.num_ref_idx_l1_active_minus1 = 0;
	for (int i = 0; i < 15; i++) {
		sl.ref_pic_list0[i].picture_id = VA_INVALID_SURFACE;
		sl.ref_pic_list0[i].flags = VA_PICTURE_HEVC_INVALID;
		sl.ref_pic_list1[i].picture_id = VA_INVALID_SURFACE;
		sl.ref_pic_list1[i].flags = VA_PICTURE_HEVC_INVALID;
	}
	if (!idr && e->have_ref) {
		sl.ref_pic_list0[0].picture_id = e->recon[cur ^ 1];
		sl.ref_pic_list0[0].pic_order_cnt = poc - 1;
		sl.ref_pic_list0[0].flags = VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE;
	}
	sl.max_num_merge_cand = 5;
	sl.slice_qp_delta = 0;
	sl.slice_fields.bits.last_slice_of_pic_flag = 1;
	VABufferID slb;
	st = VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncSliceParameterBufferType, sizeof(sl), 1, &sl, &slb);
	if (st != VA_STATUS_SUCCESS)
		return -5;
	bufs[(*pnb)++] = slb;
	return 0;
}

/* enc_submit builds the codec params for recon/coded[idx] and submits an encode of src (no sync).
 * Advances the frame/GOP state; *out_idr = whether this frame is an IDR. 0 = ok. */
static int enc_submit(rva_enc *e, int idx, VASurfaceID src, int force_idr, int *out_idr) {
	int idr = (e->since_idr == 0) || force_idr;
	*out_idr = idr;
	if (idr) {
		e->since_idr = 0;
		e->frame_num = 0;
		e->idr_id = (e->idr_id + 1) & 0xffff;
		e->have_ref = 0;
	}
	VABufferID bufs[24];
	int nb = 0;
	VAStatus st;

	if (e->codec == 1) { /* HEVC: build all codec-specific buffers, then the shared submit */
		if (idr || e->rc_dirty) {
			e->rc_dirty = 0;
			append_rc_misc(e, bufs, &nb);
		}
		int r = build_hevc_bufs(e, idx, idr, bufs, &nb);
		if (r != 0) {
			for (int i = 0; i < nb; i++)
				VA.DestroyBuffer(e->dpy, bufs[i]);
			return r;
		}
		goto do_submit;
	}

	if (idr) {
		VAEncSequenceParameterBufferH264 sp;
		memset(&sp, 0, sizeof(sp));
		sp.level_idc = (unsigned char)level_for(e->out_w, e->out_h, e->fps);
		sp.intra_period = e->gop;
		sp.intra_idr_period = e->gop;
		sp.ip_period = 1;
		sp.bits_per_second = (unsigned)e->kbps * 1000; /* ceiling == budget (HRD/VBV holds it) */
		sp.max_num_ref_frames = 1;
		sp.picture_width_in_mbs = e->mbw;
		sp.picture_height_in_mbs = e->mbh;
		sp.seq_fields.bits.chroma_format_idc = 1;
		sp.seq_fields.bits.frame_mbs_only_flag = 1;
		sp.seq_fields.bits.direct_8x8_inference_flag = 1;
		sp.seq_fields.bits.pic_order_cnt_type = 2;
		sp.seq_fields.bits.log2_max_frame_num_minus4 = e->log2_max_fn - 4;
		int crop = (e->mbh * 16) - e->out_h;
		if (crop > 0) {
			sp.frame_cropping_flag = 1;
			sp.frame_crop_bottom_offset = crop / 2;
		}
		VABufferID sb;
		st = VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncSequenceParameterBufferType, sizeof(sp), 1, &sp, &sb);
		if (st != VA_STATUS_SUCCESS)
			return -3;
		bufs[nb++] = sb;
	}

	/* Rate control (idr OR a live SetBitrate): constrained VBR - average at the budget, burst to a
	 * capped peak, and NO CBR filler, so a static/flat source drops to its natural low bitrate instead
	 * of the budget (the ffmpeg-path "50 Mbps of flat colour" shape). */
	if (idr || e->rc_dirty) {
		e->rc_dirty = 0;
		append_rc_misc(e, bufs, &nb);
	}

	VAEncPictureParameterBufferH264 pp;
	memset(&pp, 0, sizeof(pp));
	int cur = idx;
	pp.CurrPic.picture_id = e->recon[cur];
	pp.CurrPic.frame_idx = e->frame_num;
	for (int i = 0; i < 16; i++) {
		pp.ReferenceFrames[i].picture_id = VA_INVALID_SURFACE;
		pp.ReferenceFrames[i].flags = VA_PICTURE_H264_INVALID;
	}
	if (!idr && e->have_ref) {
		pp.ReferenceFrames[0].picture_id = e->recon[cur ^ 1];
		pp.ReferenceFrames[0].frame_idx = (e->frame_num - 1) & ((1 << e->log2_max_fn) - 1);
		pp.ReferenceFrames[0].flags = VA_PICTURE_H264_SHORT_TERM_REFERENCE;
	}
	pp.coded_buf = e->coded[idx];
	pp.frame_num = e->frame_num & ((1 << e->log2_max_fn) - 1);
	pp.pic_init_qp = enc_init_qp(e); /* quality mode: the live CQP value; else the 26 CBR reference */
	pp.pic_fields.bits.idr_pic_flag = idr ? 1 : 0;
	pp.pic_fields.bits.reference_pic_flag = 1;
	pp.pic_fields.bits.entropy_coding_mode_flag = 0;
	pp.last_picture = 0;
	VABufferID pb;
	st = VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPictureParameterBufferType, sizeof(pp), 1, &pp, &pb);
	if (st != VA_STATUS_SUCCESS)
		return -4;
	bufs[nb++] = pb;

	unsigned char hdr[512];
	if (idr) { /* SPS = the Sequence packed header (IDR only; carries no QP) */
		struct bs b;
		bs_init(&b, hdr, sizeof(hdr));
		write_sps(&b, e);
		VAEncPackedHeaderParameterBuffer ph;
		memset(&ph, 0, sizeof(ph));
		ph.type = VAEncPackedHeaderSequence;
		ph.bit_length = (unsigned)b.len * 8;
		ph.has_emulation_bytes = 1;
		VABufferID phb, pdb;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderParameterBufferType, sizeof(ph), 1, &ph, &phb) == VA_STATUS_SUCCESS)
			bufs[nb++] = phb;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderDataBufferType, (unsigned)b.len, 1, hdr, &pdb) == VA_STATUS_SUCCESS)
			bufs[nb++] = pdb;
	}
	/* PPS = the Picture packed header. In quality mode re-emit it every frame (it carries the live CQP
	 * init_qp); in legacy mode only at IDR. ~10 bytes, negligible at 60fps. */
	if (idr || e->quality_mode) {
		struct bs b;
		bs_init(&b, hdr, sizeof(hdr));
		write_pps(&b, e);
		VAEncPackedHeaderParameterBuffer ph2;
		memset(&ph2, 0, sizeof(ph2));
		ph2.type = VAEncPackedHeaderPicture;
		ph2.bit_length = (unsigned)b.len * 8;
		ph2.has_emulation_bytes = 1;
		VABufferID phb2, pdb2;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderParameterBufferType, sizeof(ph2), 1, &ph2, &phb2) == VA_STATUS_SUCCESS)
			bufs[nb++] = phb2;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderDataBufferType, (unsigned)b.len, 1, hdr, &pdb2) == VA_STATUS_SUCCESS)
			bufs[nb++] = pdb2;
	}

	unsigned char sh[64];
	struct bs sb2;
	bs_init(&sb2, sh, sizeof(sh));
	write_slice_hdr(&sb2, e, idr);
	if (sb2.nbits > 0) {
		sb2.acc <<= (8 - sb2.nbits);
		if (sb2.len < sb2.cap)
			sb2.p[sb2.len++] = (unsigned char)sb2.acc;
		sb2.nbits = 0;
	}
	VAEncPackedHeaderParameterBuffer sph;
	memset(&sph, 0, sizeof(sph));
	sph.type = VAEncPackedHeaderSlice;
	sph.bit_length = (unsigned)sb2.len * 8;
	sph.has_emulation_bytes = 1;
	VABufferID shpb, shdb;
	if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderParameterBufferType, sizeof(sph), 1, &sph, &shpb) == VA_STATUS_SUCCESS)
		bufs[nb++] = shpb;
	if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderDataBufferType, (unsigned)sb2.len, 1, sh, &shdb) == VA_STATUS_SUCCESS)
		bufs[nb++] = shdb;

	VAEncSliceParameterBufferH264 sl;
	memset(&sl, 0, sizeof(sl));
	sl.num_macroblocks = (unsigned)(e->mbw * e->mbh);
	sl.macroblock_info = VA_INVALID_ID;
	sl.slice_type = idr ? 2 : 0;
	sl.idr_pic_id = (unsigned short)e->idr_id;
	for (int i = 0; i < 32; i++) {
		sl.RefPicList0[i].picture_id = VA_INVALID_SURFACE;
		sl.RefPicList0[i].flags = VA_PICTURE_H264_INVALID;
		sl.RefPicList1[i].picture_id = VA_INVALID_SURFACE;
		sl.RefPicList1[i].flags = VA_PICTURE_H264_INVALID;
	}
	if (!idr && e->have_ref) {
		sl.RefPicList0[0].picture_id = e->recon[cur ^ 1];
		sl.RefPicList0[0].frame_idx = (e->frame_num - 1) & ((1 << e->log2_max_fn) - 1);
		sl.RefPicList0[0].flags = VA_PICTURE_H264_SHORT_TERM_REFERENCE;
	}
	VABufferID slb;
	st = VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncSliceParameterBufferType, sizeof(sl), 1, &sl, &slb);
	if (st != VA_STATUS_SUCCESS)
		return -5;
	bufs[nb++] = slb;

do_submit:
	st = VA.BeginPicture(e->dpy, e->enc_ctx, src);
	if (st == VA_STATUS_SUCCESS)
		st = VA.RenderPicture(e->dpy, e->enc_ctx, bufs, nb);
	if (st == VA_STATUS_SUCCESS)
		st = VA.EndPicture(e->dpy, e->enc_ctx); /* submit only - NO sync here (pipeline) */
	for (int i = 0; i < nb; i++)
		VA.DestroyBuffer(e->dpy, bufs[i]);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("enc: submit: %s\n", VA.ErrorStr(st));
		return -6;
	}

	/* advance state for the frame just submitted */
	e->frame_num++;
	e->since_idr++;
	if (e->since_idr >= e->gop)
		e->since_idr = 0;
	e->have_ref = 1;
	e->pp ^= 1;
	return 0;
}

/* enc_emit waits for the encode whose input surface is sync (submitted into coded[pidx]), copies the
 * Annex-B AU out (+ H.264 AUD) and runs the rate controllers. */
static int enc_emit(rva_enc *e, VASurfaceID sync, int pidx, int pidr, unsigned char **au, int *au_len, int *keyframe) {
	long long ts = now_us();
	VAStatus st = VA.SyncSurface(e->dpy, sync);
	long long tc = now_us();
	e->st_us[RVA_ST_SYNC] = tc - ts;
	if (st != VA_STATUS_SUCCESS) {
		VDBG("enc: sync pending: %s\n", VA.ErrorStr(st));
		return -6;
	}
	VACodedBufferSegment *seg = NULL;
	st = VA.MapBuffer(e->dpy, e->coded[pidx], (void **)&seg);
	if (st != VA_STATUS_SUCCESS || !seg)
		return -7;
	int off = 0;
	if (e->codec == 0) { /* H.264 AUD (nal_unit_type 9); HEVC carries no AUD (VPS/SPS/PPS already in-band) */
		static const unsigned char aud_i[] = {0, 0, 0, 1, 0x09, 0x10};
		static const unsigned char aud_p[] = {0, 0, 0, 1, 0x09, 0x30};
		if (off + 6 <= e->au_cap) {
			memcpy(e->au, pidr ? aud_i : aud_p, 6);
			off = 6;
		}
	}
	for (VACodedBufferSegment *s = seg; s; s = (VACodedBufferSegment *)s->next) {
		if (off + (int)s->size > e->au_cap)
			break;
		memcpy(e->au + off, s->buf, s->size);
		off += (int)s->size;
	}
	VA.UnmapBuffer(e->dpy, e->coded[pidx]);
	e->st_us[RVA_ST_COPY] = now_us() - tc;

	if (e->quality_mode) {
		/* constant-quality ceiling controller: hold cur_qp (== target_qp while content is simple, so it
		 * undershoots), and over a ~250ms sub-window raise cur_qp toward 51 when the rate would exceed the
		 * ceiling, releasing slowly toward target_qp when comfortably under. The next frame re-emits the
		 * PPS with the new QP (no reopen). rate_limited = held above the quality target by the ceiling. */
		if (off > 0 && e->fps > 0) {
			/* IDR bits are amortized over the GOP (they are on the wire once per GOP): fed raw, one IDR
			 * makes its sub-window read ~2x and the proportional step would pump quality every GOP. */
			if (pidr)
				e->qc_idr_bits = (long long)off * 8;
			else
				e->qc_bits += (long long)off * 8;
			e->qc_pframes += !pidr;
			if (++e->qc_frames >= e->qc_sub) {
				if (e->qc_pframes > 0) {
					long long eq = e->qc_bits * e->qc_frames / e->qc_pframes +
					               e->qc_idr_bits * e->qc_frames / (e->gop > 0 ? e->gop : e->fps);
					e->cur_qp = rva_qp_next(e->cur_qp, e->target_qp, eq, e->qc_frames, e->fps,
						(long long)e->kbps * 1000, &e->qc_under);
					e->rate_limited = e->cur_qp > e->target_qp;
				}
				e->qc_bits = 0;
				e->qc_frames = 0;
				e->qc_pframes = 0;
			}
		}
	} else if (off > 0 && e->fps > 0) {
		/* closed-loop bitrate corrector (legacy CBR): a 1s window over emitted AU bits. Scale the
		 * EFFECTIVE budget sent to the driver so the MEASURED output lands on the real budget - lowers
		 * rc_scale when the driver overshoots, raises it (toward 100%) when well under. */
		e->win_bits += (long long)off * 8;
		if (++e->win_frames >= e->fps) {
			long long budget = (long long)e->kbps * 1000;
			int cur = e->rc_scale_pct > 0 ? e->rc_scale_pct : 100;
			int ns = cur;
			if (budget > 0) {
				if (e->win_bits > budget * 130 / 100)
					ns -= 10; /* far over: big step */
				else if (e->win_bits > budget * 105 / 100)
					ns -= 4;
				else if (e->win_bits < budget * 85 / 100)
					ns += 4; /* under: give quality back toward 100% */
			}
			if (ns < 30)
				ns = 30;
			if (ns > 100)
				ns = 100;
			if (ns != cur) {
				e->rc_scale_pct = ns;
				e->rc_dirty = 1;
			}
			e->win_bits = 0;
			e->win_frames = 0;
		}
	}

	*au = e->au;
	*au_len = off;
	*keyframe = pidr;
	return 0;
}

int rva_enc_frame(rva_enc *e, uint32_t drm_fourcc, uint64_t modifier, int w, int h, int n_planes,
                  const int *fds, const unsigned *offsets, const unsigned *strides,
                  int flip, int force_idr, unsigned char **au, int *au_len, int *keyframe) {
	if (!e || !au || !au_len || !keyframe)
		return -1;
	*au_len = 0;
	int idx = e->pp, idr = 0;
	for (int i = 0; i < RVA_NSTEP; i++)
		e->st_us[i] = -1;
	long long t0 = now_us();
	if (vpp_to_nv12(e, e->nv12[idx], drm_fourcc, modifier, w, h, n_planes, fds, offsets, strides, flip) != 0)
		return -2;
	long long t1 = now_us();
	e->st_us[RVA_ST_VPP] = t1 - t0 - (e->st_us[RVA_ST_IMPORT] > 0 ? e->st_us[RVA_ST_IMPORT] : 0);
	if (e->vpp_sync) { /* diag: GPU latency of the colour convert alone (serializes the pipeline) */
		VA.SyncSurface(e->dpy, e->nv12[idx]);
		e->st_us[RVA_ST_VPPGPU] = now_us() - t1;
		t1 = now_us();
	}
	int r = enc_submit(e, idx, e->nv12[idx], force_idr, &idr);
	e->st_us[RVA_ST_SUBMIT] = now_us() - t1;
	if (r != 0)
		return r;
	e->last_idx = idx; /* repeat source (rva_enc_repeat) */
	e->have_last = 1;
	/* emit the PREVIOUS frame (its GPU encode overlapped with building+submitting this one). The
	 * first frame primes the pipeline and returns no AU. */
	if (!e->have_pending) {
		e->have_pending = 1;
		e->pend_idx = idx;
		e->pend_idr = idr;
		return 0; /* *au_len stays 0 */
	}
	int pidx = e->pend_idx, pidr = e->pend_idr;
	e->pend_idx = idx; /* this frame becomes the new pending regardless of the emit outcome */
	e->pend_idr = idr;
	return enc_emit(e, e->nv12[pidx], pidx, pidr, au, au_len, keyframe);
}

void rva_enc_set_fast_scale(rva_enc *e, int on) {
	if (e)
		e->fast_scale = on;
}

void rva_enc_last_steps(rva_enc *e, long long out[RVA_NSTEP]) {
	for (int i = 0; i < RVA_NSTEP; i++)
		out[i] = e ? e->st_us[i] : -1;
}

int rva_enc_flush(rva_enc *e, unsigned char **au, int *au_len, int *keyframe) {
	if (!e || !au || !au_len || !keyframe)
		return -1;
	*au_len = 0;
	if (!e->have_pending)
		return 0;
	for (int i = 0; i < RVA_NSTEP; i++)
		e->st_us[i] = -1;
	e->have_pending = 0;
	return enc_emit(e, e->nv12[e->pend_idx], e->pend_idx, e->pend_idr, au, au_len, keyframe);
}

int rva_enc_repeat(rva_enc *e, int force_idr, unsigned char **au, int *au_len, int *keyframe) {
	if (!e || !au || !au_len || !keyframe)
		return -1;
	*au_len = 0;
	if (e->have_pending) /* the pending real frame is the cadence AU */
		return rva_enc_flush(e, au, au_len, keyframe);
	if (!e->have_last)
		return -1;
	/* re-encode the last VPP output (still in nv12[last_idx]: the next real frame VPPs into the other
	 * surface, and this is synchronous) into recon/coded[pp]; identical content => P-skip, tiny AU. */
	int idx = e->pp, idr = 0;
	VASurfaceID src = e->nv12[e->last_idx];
	for (int i = 0; i < RVA_NSTEP; i++)
		e->st_us[i] = -1;
	long long t1 = now_us();
	int r = enc_submit(e, idx, src, force_idr, &idr);
	e->st_us[RVA_ST_SUBMIT] = now_us() - t1;
	if (r != 0)
		return r;
	return enc_emit(e, src, idx, idr, au, au_len, keyframe);
}

void rva_enc_set_bitrate(rva_enc *e, int kbps) {
	if (!e || kbps <= 0)
		return;
	e->kbps = kbps;
	if (e->quality_mode) {
		/* CQP: kbps is the ceiling the software controller enforces. Just reset the sub-window so the new
		 * ceiling takes effect cleanly on the next window (no RC param to resend). */
		e->qc_bits = 0;
		e->qc_frames = 0;
		e->qc_pframes = 0;
		e->qc_under = 0;
		return;
	}
	e->rc_scale_pct = e->codec == 1 ? 68 : 90; /* re-seed the corrector for the new budget */
	e->win_bits = 0;
	e->win_frames = 0;
	e->rc_dirty = 1; /* re-send the CBR rate-control misc param on the next frame */
}

/* rva_enc_set_quality updates the live constant-quality target QP (quality mode only; no reopen). */
void rva_enc_set_quality(rva_enc *e, int target_qp) {
	if (!e || !e->quality_mode || target_qp <= 0)
		return;
	if (target_qp > 51)
		target_qp = 51;
	e->target_qp = target_qp;
	if (e->cur_qp < target_qp) /* a lower quality raises the floor immediately */
		e->cur_qp = target_qp;  /* a higher quality lets the controller drift cur_qp back down */
	e->rate_limited = e->cur_qp > e->target_qp;
	e->qc_bits = 0;
	e->qc_frames = 0;
	e->qc_pframes = 0;
	e->qc_under = 0;
}

/* rva_enc_feedback reports the per-second quality signal (see vaenc.h). */
void rva_enc_feedback(rva_enc *e, int *present, double *avg_qp, int *rate_limited) {
	if (present)
		*present = (e && e->quality_mode) ? 1 : 0;
	if (avg_qp)
		*avg_qp = e ? (double)e->cur_qp : 0.0;
	if (rate_limited)
		*rate_limited = (e && e->rate_limited) ? 1 : 0;
}

/* rva_qp_next: the pure constant-quality ceiling controller (see vaenc.h). */
int rva_qp_next(int cur_qp, int target_qp, long long win_bits, int frames, int fps,
                long long budget_bits, int *under) {
	int q = cur_qp;
	if (frames > 0 && fps > 0 && budget_bits > 0) {
		double secs = (double)frames / (double)fps;
		double rate = secs > 0 ? (double)win_bits / secs : 0.0; /* bits/sec over the sub-window */
		if (rate > (double)budget_bits * 1.1) {
			/* over the ceiling: raise PROPORTIONALLY (CQP: +6 QP ~ half the bits) to land at ~0.95x in
			 * ONE sub-window - fixed +1/+2 steps took ~1 s to converge and the 1-s peak read 1.4x the
			 * ceiling on a content change (rig, 1080p 10 Mbps). Capped at +8 per window. */
			int step = (int)ceil(6.0 * log2(rate / ((double)budget_bits * 0.95)));
			q += step < 1 ? 1 : step > 8 ? 8 : step;
			if (under)
				*under = 0;
		} else if (rate < (double)budget_bits * 85 / 100) { /* comfortably under: slow release */
			int u = (under ? *under : 0) + 1;
			if (u >= 4) { /* ~1 s sustained before giving quality back (no pumping) */
				/* release HALF the predicted headroom (to ~0.9x), >= 1: after a proportional raise the
				 * old 1-QP-per-2s release left the route at ~50-60% of the ceiling for many seconds */
				int rel = rate > 0 ? (int)(3.0 * log2((double)budget_bits * 0.9 / rate)) : 6;
				q -= rel < 1 ? 1 : rel > 6 ? 6 : rel;
				u = 0;
			}
			if (under)
				*under = u;
		} else if (under) { /* dead band [0.85, 1.1]x: hold, reset the release streak */
			*under = 0;
		}
	}
	if (q < target_qp)
		q = target_qp;
	if (q > 51)
		q = 51;
	return q;
}

void rva_enc_free(rva_enc *e) {
	if (!e)
		return;
	if (e->dpy) {
		for (int i = 0; i < e->cache_n; i++)
			if (e->cache_surf[i] != VA_INVALID_ID)
				VA.DestroySurfaces(e->dpy, &e->cache_surf[i], 1);
		for (int i = 0; i < 2; i++)
			if (e->coded[i] != VA_INVALID_ID)
				VA.DestroyBuffer(e->dpy, e->coded[i]);
		if (e->enc_ctx != VA_INVALID_ID)
			VA.DestroyContext(e->dpy, e->enc_ctx);
		if (e->enc_cfg != VA_INVALID_ID)
			VA.DestroyConfig(e->dpy, e->enc_cfg);
		if (e->vpp_ctx != VA_INVALID_ID)
			VA.DestroyContext(e->dpy, e->vpp_ctx);
		if (e->vpp_cfg != VA_INVALID_ID)
			VA.DestroyConfig(e->dpy, e->vpp_cfg);
		if (e->recon[0] != VA_INVALID_ID)
			VA.DestroySurfaces(e->dpy, e->recon, 2);
		if (e->nv12[0] != VA_INVALID_ID)
			VA.DestroySurfaces(e->dpy, e->nv12, 2);
		VA.Terminate(e->dpy);
	}
	if (e->fd >= 0)
		close(e->fd);
	free(e->au);
	free(e);
}
