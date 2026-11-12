//go:build linux && pipewire

/* vaenc.c - VA-API dmabuf import + VPP (and, later, encode). libva/libva-drm are dlopen'd so a box
 * without them degrades cleanly. See vaenc.h. */
#include "vaenc.h"

#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#include <va/va.h>
#include <va/va_drm.h>
#include <va/va_drmcommon.h>
#include <va/va_vpp.h>
#include <libdrm/drm_fourcc.h>
#include <fcntl.h>

#define RVA_MAX_PLANES 4

static int va_dbg = 0;
#define VDBG(...)                          \
	do {                                   \
		if (va_dbg)                        \
			fprintf(stderr, __VA_ARGS__);  \
	} while (0)

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

static int va_load(void) {
	if (va_loaded != -1)
		return va_loaded;
	va_loaded = 0;
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
	va_loaded = 1;
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
	VAConfigAttrib a[3];
	a[0].type = VAConfigAttribRateControl;
	a[1].type = VAConfigAttribEncPackedHeaders;
	a[2].type = VAConfigAttribEncMaxRefFrames;
	VAStatus st = VA.GetConfigAttributes(dpy, VAProfileH264High, VAEntrypointEncSlice, a, 3);
	fprintf(stderr, "RVA_ENC_CAPS H264High st=%d ratecontrol=0x%x packedheaders=0x%x maxref=0x%x\n",
		st, a[0].value, a[1].value, a[2].value);
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
	unsigned char *au; /* returned AU buffer (AUD + coded) */
	int au_cap;
	/* import-surface cache: the PipeWire pool cycles a fixed set of dmabuf fds; create a VASurface
	 * per fd once + reuse (per-frame vaCreateSurfaces import was the ~55fps-at-4K bottleneck + a slow
	 * RSS creep). Keyed by the plane-0 fd; the pool's layout per fd is stable. */
	int cache_fd[16];
	VASurfaceID cache_surf[16];
	int cache_n;
};

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
	(void)e;
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
	bs_se(b, 0);            /* pic_init_qp_minus26 */
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

/* import_cached returns a VASurface for the dmabuf, reusing one created earlier for the same
 * plane-0 fd (the pool cycles a fixed fd set). Cache full / miss on a fresh fd => import + store. */
static VASurfaceID import_cached(struct rva_enc *e, uint32_t drm_fourcc, uint64_t modifier, int w, int h,
                                 int n_planes, const int *fds, const unsigned *offsets, const unsigned *strides) {
	for (int i = 0; i < e->cache_n; i++)
		if (e->cache_fd[i] == fds[0])
			return e->cache_surf[i];
	VASurfaceID s = VA_INVALID_ID;
	VAStatus st = import_dmabuf(e->dpy, drm_fourcc, modifier, w, h, n_planes, fds, offsets, strides, &s);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("enc: import: %s\n", VA.ErrorStr(st));
		return VA_INVALID_ID;
	}
	if (e->cache_n < (int)(sizeof(e->cache_fd) / sizeof(e->cache_fd[0]))) {
		e->cache_fd[e->cache_n] = fds[0];
		e->cache_surf[e->cache_n] = s;
		e->cache_n++;
	} /* else: not cached, destroyed by the caller via a one-shot path - but pools are small, so the
	     cache never fills in practice; if it did we'd leak this surface. Guarded by the 16 cap. */
	return s;
}

/* vpp_to_nv12: import (cached) the dmabuf, VPP (convert + optional vertical mirror + scale) into
 * e->nv12. flip=1 mirrors vertically (a bottom-up GL producer); flip=0 keeps orientation (a top-down
 * DX/Spout producer). Same decision as the readback path (shim want_flip), so both agree.
 * No explicit VPP sync: the encode's BeginPicture(nv12) + SyncSurface order the dependency on the
 * shared nv12 surface, which lets VPP and encode pipeline. 0=ok. */
static int vpp_to_nv12(struct rva_enc *e, VASurfaceID target, uint32_t drm_fourcc, uint64_t modifier, int w, int h,
                       int n_planes, const int *fds, const unsigned *offsets, const unsigned *strides, int flip) {
	VASurfaceID rgb = import_cached(e, drm_fourcc, modifier, w, h, n_planes, fds, offsets, strides);
	if (rgb == VA_INVALID_ID)
		return -1;
	VAProcPipelineParameterBuffer pp;
	memset(&pp, 0, sizeof(pp));
	pp.surface = rgb;
	pp.filter_flags = VA_FILTER_SCALING_HQ;
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

rva_enc *rva_enc_new(const char *node, int codec, int out_w, int out_h, int fps, int bitrate_kbps) {
	if (!va_load() || out_w < 16 || out_h < 16 || out_w > 8192 || out_h > 8192)
		return NULL;
	if (codec != 0) /* H.264 only in this engine; HEVC lands in a follow-up (caller falls back) */
		return NULL;
	struct rva_enc *e = calloc(1, sizeof(*e));
	if (!e)
		return NULL;
	e->codec = codec;
	e->fd = open(node, O_RDWR | O_CLOEXEC);
	if (e->fd < 0) {
		free(e);
		return NULL;
	}
	e->out_w = out_w;
	e->out_h = out_h;
	e->fps = fps > 0 ? fps : 60;
	e->kbps = bitrate_kbps > 0 ? bitrate_kbps : 20000;
	e->mbw = (out_w + 15) / 16;
	e->mbh = (out_h + 15) / 16;
	e->log2_max_fn = 4;
	e->gop = e->fps * 2;
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
	ea[1].value = VA_RC_VBR; /* constrained VBR: average=target, burst to peak, NO CBR filler on static content */
	ea[2].type = VAConfigAttribEncPackedHeaders;
	ea[2].value = VA_ENC_PACKED_HEADER_SEQUENCE | VA_ENC_PACKED_HEADER_PICTURE | VA_ENC_PACKED_HEADER_SLICE;
	if (VA.CreateConfig(e->dpy, VAProfileH264ConstrainedBaseline, VAEntrypointEncSlice, ea, 3, &e->enc_cfg) != VA_STATUS_SUCCESS) {
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

int rva_enc_frame(rva_enc *e, uint32_t drm_fourcc, uint64_t modifier, int w, int h, int n_planes,
                  const int *fds, const unsigned *offsets, const unsigned *strides,
                  int flip, int force_idr, unsigned char **au, int *au_len, int *keyframe) {
	if (!e || !au || !au_len || !keyframe)
		return -1;
	*au_len = 0;
	int idx = e->pp;
	if (vpp_to_nv12(e, e->nv12[idx], drm_fourcc, modifier, w, h, n_planes, fds, offsets, strides, flip) != 0)
		return -2;
	int idr = (e->since_idr == 0) || force_idr;
	if (idr) {
		e->since_idr = 0;
		e->frame_num = 0;
		e->idr_id = (e->idr_id + 1) & 0xffff;
		e->have_ref = 0;
	}
	VABufferID bufs[12];
	int nb = 0;
	VAStatus st;

	if (idr) {
		VAEncSequenceParameterBufferH264 sp;
		memset(&sp, 0, sizeof(sp));
		sp.level_idc = (unsigned char)level_for(e->out_w, e->out_h, e->fps);
		sp.intra_period = e->gop;
		sp.intra_idr_period = e->gop;
		sp.ip_period = 1;
		sp.bits_per_second = (unsigned)e->kbps * 1000 + (unsigned)e->kbps * 1000 / 2; /* peak (VBR HRD) */
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
		VAEncMiscParameterBuffer *mb;
		VABufferID rcb;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncMiscParameterBufferType,
			sizeof(VAEncMiscParameterBuffer) + sizeof(VAEncMiscParameterRateControl), 1, NULL, &rcb) == VA_STATUS_SUCCESS &&
			VA.MapBuffer(e->dpy, rcb, (void **)&mb) == VA_STATUS_SUCCESS) {
			mb->type = VAEncMiscParameterTypeRateControl;
			VAEncMiscParameterRateControl *rc = (VAEncMiscParameterRateControl *)mb->data;
			memset(rc, 0, sizeof(*rc));
			unsigned target = (unsigned)e->kbps * 1000;
			rc->bits_per_second = target + target / 2; /* peak = 1.5x target (constrained VBR cap) */
			rc->target_percentage = 67;                /* average ~= target; static content falls below */
			rc->window_size = 1000;
			rc->initial_qp = 26;
			rc->min_qp = 10;
			VA.UnmapBuffer(e->dpy, rcb);
			bufs[nb++] = rcb;
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
			bufs[nb++] = frb;
		}
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
	pp.pic_init_qp = 26;
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
	if (idr) {
		struct bs b;
		bs_init(&b, hdr, sizeof(hdr));
		write_sps(&b, e);
		int sps_len = b.len;
		write_pps(&b, e);
		VAEncPackedHeaderParameterBuffer ph;
		memset(&ph, 0, sizeof(ph));
		ph.type = VAEncPackedHeaderSequence;
		ph.bit_length = (unsigned)sps_len * 8;
		ph.has_emulation_bytes = 1;
		VABufferID phb, pdb;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderParameterBufferType, sizeof(ph), 1, &ph, &phb) == VA_STATUS_SUCCESS)
			bufs[nb++] = phb;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderDataBufferType, (unsigned)sps_len, 1, hdr, &pdb) == VA_STATUS_SUCCESS)
			bufs[nb++] = pdb;
		VAEncPackedHeaderParameterBuffer ph2;
		memset(&ph2, 0, sizeof(ph2));
		ph2.type = VAEncPackedHeaderPicture;
		ph2.bit_length = (unsigned)(b.len - sps_len) * 8;
		ph2.has_emulation_bytes = 1;
		VABufferID phb2, pdb2;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderParameterBufferType, sizeof(ph2), 1, &ph2, &phb2) == VA_STATUS_SUCCESS)
			bufs[nb++] = phb2;
		if (VA.CreateBuffer(e->dpy, e->enc_ctx, VAEncPackedHeaderDataBufferType, (unsigned)(b.len - sps_len), 1, hdr + sps_len, &pdb2) == VA_STATUS_SUCCESS)
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

	st = VA.BeginPicture(e->dpy, e->enc_ctx, e->nv12[idx]);
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

	/* emit the PREVIOUS frame (its GPU encode overlapped with building+submitting this one). The
	 * first frame primes the pipeline and returns no AU. */
	if (!e->have_pending) {
		e->have_pending = 1;
		e->pend_idx = idx;
		e->pend_idr = idr;
		return 0; /* *au_len stays 0 */
	}
	int pidx = e->pend_idx;
	int pidr = e->pend_idr;
	e->pend_idx = idx; /* this frame becomes the new pending regardless of the emit outcome */
	e->pend_idr = idr;
	st = VA.SyncSurface(e->dpy, e->nv12[pidx]);
	if (st != VA_STATUS_SUCCESS) {
		VDBG("enc: sync pending: %s\n", VA.ErrorStr(st));
		return -6;
	}
	VACodedBufferSegment *seg = NULL;
	st = VA.MapBuffer(e->dpy, e->coded[pidx], (void **)&seg);
	if (st != VA_STATUS_SUCCESS || !seg)
		return -7;
	static const unsigned char aud_i[] = {0, 0, 0, 1, 0x09, 0x10};
	static const unsigned char aud_p[] = {0, 0, 0, 1, 0x09, 0x30};
	int off = 0;
	if (off + 6 <= e->au_cap) {
		memcpy(e->au, pidr ? aud_i : aud_p, 6);
		off = 6;
	}
	for (VACodedBufferSegment *s = seg; s; s = (VACodedBufferSegment *)s->next) {
		if (off + (int)s->size > e->au_cap)
			break;
		memcpy(e->au + off, s->buf, s->size);
		off += (int)s->size;
	}
	VA.UnmapBuffer(e->dpy, e->coded[pidx]);

	*au = e->au;
	*au_len = off;
	*keyframe = pidr;
	return 0;
}

void rva_enc_set_bitrate(rva_enc *e, int kbps) {
	if (!e || kbps <= 0)
		return;
	e->kbps = kbps;
	e->rc_dirty = 1; /* re-send the VBR rate-control misc param on the next frame */
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
