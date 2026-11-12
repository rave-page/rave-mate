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
