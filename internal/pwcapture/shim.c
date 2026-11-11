//go:build linux && pipewire

/* shim.c - PipeWire video capture via runtime dlopen (design D8: no go.mod dep, degrade to no-op
 * when libpipewire-0.3 is absent). One capture = one pw_thread_loop + one input pw_stream connected
 * by node name (auto-reconnect). Frames land in a bounded single-slot (drop-oldest + counter); the
 * Go side pulls the latest via rpw_wait_frame. SHM (MemPtr/MemFd, mapped by PipeWire) is converted
 * to RGBA here; DMA-BUF buffers are counted + skipped in this file (EGL import lands in shim_egl.c).
 *
 * Headers are compiled in (SPA POD builders/parsers are header-only inline); only the pw_* symbols
 * are dlopen'd, so nothing is load-time linked against libpipewire. */
#include "shim.h"

#include <dlfcn.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <pipewire/pipewire.h>
#include <spa/param/video/format-utils.h>
#include <spa/param/video/raw.h>

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
static int pw_loaded = -1; /* -1 untried, 0 fail, 1 ok */
static pthread_mutex_t load_mu = PTHREAD_MUTEX_INITIALIZER;

static int resolve_all(void *h) {
	struct {
		const char *name;
		void **slot;
	} tbl[] = {
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
	for (size_t i = 0; i < sizeof(tbl) / sizeof(tbl[0]); i++) {
		void *s = dlsym(h, tbl[i].name);
		if (!s)
			return 0;
		*tbl[i].slot = s;
	}
	return 1;
}

int rpw_available(void) {
	pthread_mutex_lock(&load_mu);
	if (pw_loaded == -1) {
		void *h = dlopen("libpipewire-0.3.so.0", RTLD_NOW | RTLD_GLOBAL);
		if (h && resolve_all(h)) {
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

/* ---- capture state -------------------------------------------------------------------------- */
struct rpw_capture {
	struct pw_thread_loop *loop;
	struct pw_context *context;
	struct pw_core *core;
	struct pw_stream *stream;
	struct spa_hook listener;

	pthread_mutex_t mu;
	pthread_cond_t cv;
	int width, height;      /* negotiated dims */
	enum spa_video_format fmt;
	unsigned char *slot;    /* latest RGBA frame (width*height*4) */
	int slot_cap;           /* bytes allocated for slot */
	int has_frame;          /* slot holds an unconsumed frame */
	int fatal;              /* stream errored */
	long dropped;           /* drop-oldest + unsupported-buffer skips */
	long dmabuf_skipped;    /* frames that were DMA-BUF (SHM path can't read) */
};

/* convert one frame to RGBA (Go NRGBA order: R,G,B,A) handling stride + the 4 offered layouts. */
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
			} else { /* BGRA / BGRx */
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

/* store_frame writes the latest RGBA frame into the bounded slot (drop-oldest). Called on the
 * PipeWire loop thread. */
static void store_frame(struct rpw_capture *c, const unsigned char *src, int stride) {
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
		c->dropped++; /* overwrote an unconsumed frame */
	to_rgba(c->slot, src, c->width, c->height, stride, c->fmt);
	c->has_frame = 1;
	pthread_cond_signal(&c->cv);
	pthread_mutex_unlock(&c->mu);
}

static void on_param_changed(void *data, uint32_t id, const struct spa_pod *param) {
	struct rpw_capture *c = data;
	if (param == NULL || id != SPA_PARAM_Format)
		return;
	uint32_t mt, mst;
	if (spa_format_parse(param, &mt, &mst) < 0)
		return;
	if (mt != SPA_MEDIA_TYPE_video || mst != SPA_MEDIA_SUBTYPE_raw)
		return;
	struct spa_video_info_raw raw;
	spa_zero(raw);
	if (spa_format_video_raw_parse(param, &raw) < 0)
		return;
	pthread_mutex_lock(&c->mu);
	c->width = raw.size.width;
	c->height = raw.size.height;
	c->fmt = raw.format;
	pthread_mutex_unlock(&c->mu);

	/* Ask for SHM buffers (MemFd/MemPtr) we can map; a DMA-BUF-only producer still sends dmabuf,
	 * which on_process detects + counts. 8 buffers, 1 blit plane. */
	uint8_t bb[512];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(bb, sizeof(bb));
	const struct spa_pod *params[1];
	params[0] = spa_pod_builder_add_object(&b,
		SPA_TYPE_OBJECT_ParamBuffers, SPA_PARAM_Buffers,
		SPA_PARAM_BUFFERS_buffers, SPA_POD_CHOICE_RANGE_Int(8, 2, 16),
		SPA_PARAM_BUFFERS_dataType, SPA_POD_CHOICE_FLAGS_Int(
			(1 << SPA_DATA_MemFd) | (1 << SPA_DATA_MemPtr) | (1 << SPA_DATA_DmaBuf)));
	PW.stream_update_params(c->stream, params, 1);
}

static void on_process(void *data) {
	struct rpw_capture *c = data;
	struct pw_buffer *pb;
	/* drain to the most recent queued buffer (keeps latency low; drop-oldest) */
	struct pw_buffer *last = NULL;
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
			c->dmabuf_skipped++;
			c->dropped++; /* EGL import not built (shim_egl.c) */
		} else if (d->data && d->chunk && d->chunk->size > 0 && c->width > 0) {
			uint32_t stride = d->chunk->stride ? d->chunk->stride : (uint32_t)(c->width * 4);
			store_frame(c, (const unsigned char *)d->data + d->chunk->offset, (int)stride);
		} else {
			c->dropped++;
		}
	}
	PW.stream_queue(c->stream, last);
}

static void on_state_changed(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
	(void)old;
	(void)error;
	struct rpw_capture *c = data;
	if (state == PW_STREAM_STATE_ERROR) {
		pthread_mutex_lock(&c->mu);
		c->fatal = 1;
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

/* build_format offers RGBA-family raw video at any size/framerate (SHM + DMA-BUF allowed). */
static const struct spa_pod *build_format(struct spa_pod_builder *b) {
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
			&SPA_FRACTION(30, 1), &SPA_FRACTION(0, 1), &SPA_FRACTION(240, 1)));
}

rpw_capture *rpw_start(const char *target_node) {
	if (!rpw_available())
		return NULL;
	struct rpw_capture *c = calloc(1, sizeof(*c));
	if (!c)
		return NULL;
	pthread_mutex_init(&c->mu, NULL);
	pthread_cond_init(&c->cv, NULL);

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

	struct pw_properties *props = PW.props_new(
		PW_KEY_MEDIA_TYPE, "Video",
		PW_KEY_MEDIA_CATEGORY, "Capture",
		PW_KEY_MEDIA_ROLE, "Camera",
		PW_KEY_TARGET_OBJECT, target_node,
		NULL);
	c->stream = PW.stream_new(c->core, "rave-mate-capture", props);
	if (!c->stream)
		goto fail_unlock;
	PW.stream_add_listener(c->stream, &c->listener, &stream_events, c);

	uint8_t fb[1024];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(fb, sizeof(fb));
	const struct spa_pod *params[1];
	params[0] = build_format(&b);
	if (PW.stream_connect(c->stream, PW_DIRECTION_INPUT, PW_ID_ANY,
		PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS, params, 1) < 0)
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

int rpw_wait_frame(rpw_capture *c, unsigned char *dst, int cap, int *w, int *h, long timeout_ms) {
	struct timespec ts;
	clock_gettime(CLOCK_REALTIME, &ts);
	ts.tv_sec += timeout_ms / 1000;
	ts.tv_nsec += (timeout_ms % 1000) * 1000000L;
	if (ts.tv_nsec >= 1000000000L) {
		ts.tv_sec++;
		ts.tv_nsec -= 1000000000L;
	}
	pthread_mutex_lock(&c->mu);
	while (!c->has_frame && !c->fatal) {
		if (pthread_cond_timedwait(&c->cv, &c->mu, &ts) != 0)
			break;
	}
	if (c->fatal && !c->has_frame) {
		pthread_mutex_unlock(&c->mu);
		return -1;
	}
	if (!c->has_frame) {
		pthread_mutex_unlock(&c->mu);
		return 0;
	}
	int need = c->width * c->height * 4;
	*w = c->width;
	*h = c->height;
	if (need > cap) {
		pthread_mutex_unlock(&c->mu); /* keep the frame; caller reallocs + retries */
		return -2;
	}
	memcpy(dst, c->slot, need);
	c->has_frame = 0;
	pthread_mutex_unlock(&c->mu);
	return need;
}

long rpw_dropped(rpw_capture *c) { return c ? c->dropped : 0; }
long rpw_dmabuf_skipped(rpw_capture *c) { return c ? c->dmabuf_skipped : 0; }

void rpw_stop(rpw_capture *c) {
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
	free(c->slot);
	pthread_mutex_destroy(&c->mu);
	pthread_cond_destroy(&c->cv);
	free(c);
}
