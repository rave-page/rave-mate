// funnel-producer - a headless libfunnel dmabuf video producer for testing the pwcapture DMA-BUF
// path. DEV TOOL, not shipped. Uses libfunnel (the exact library Spout2PW publishes with) so its
// buffer types / modifiers / sync behaviour match the real VRChat node. Surfaceless EGL (no X11
// window) renders a frame-counter pattern straight into libfunnel's dmabuf via an FBO:
//   - background = solid colour encoding the frame counter (R = frame low byte)
//   - four distinct corner squares (TL red, TR green, BL blue, BR white) to pin orientation/flip,
//     channel order and alpha for the consumer's pixel check.
// No shaders: glClearColor + glScissor regions only.
//
// Usage: funnel-producer <width> <height> <fps> <stream-name> [-implicit|-explicit|-hybrid|-either]
//
// Build: contrib/linux-build/testprod/build-testprod.sh (direct gcc against the libfunnel submodule
// vendored in spout2pw).
#define _POSIX_C_SOURCE 200809L
#include <funnel-egl.h>
#include <funnel.h>

#define GL_GLEXT_PROTOTYPES
#include <GL/gl.h>
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#define EGL_EGLEXT_PROTOTYPES
#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <gbm.h>

#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

static EGLDisplay egl_display;
static EGLContext egl_context;
static struct gbm_device *gbm;
static int drm_fd = -1;

static void die(const char *m) {
	fprintf(stderr, "funnel-producer: %s (egl 0x%x)\n", m, eglGetError());
	exit(1);
}

// init_egl_surfaceless brings up a GL context on the render node with no window (GBM platform +
// EGL_KHR_surfaceless_context), so this runs headless over ssh.
static void init_egl_surfaceless(const char *node) {
	drm_fd = open(node, O_RDWR | O_CLOEXEC);
	if (drm_fd < 0)
		die("open render node");
	gbm = gbm_create_device(drm_fd);
	if (!gbm)
		die("gbm_create_device");

	PFNEGLGETPLATFORMDISPLAYEXTPROC getPlatformDisplay =
		(PFNEGLGETPLATFORMDISPLAYEXTPROC)eglGetProcAddress("eglGetPlatformDisplayEXT");
	if (!getPlatformDisplay)
		die("no eglGetPlatformDisplayEXT");
	egl_display = getPlatformDisplay(EGL_PLATFORM_GBM_KHR, gbm, NULL);
	if (egl_display == EGL_NO_DISPLAY)
		die("eglGetPlatformDisplay");
	if (!eglInitialize(egl_display, NULL, NULL))
		die("eglInitialize");
	if (!eglBindAPI(EGL_OPENGL_API))
		die("eglBindAPI");
	// Surfaceless FBO rendering: no drawable, so no config (EGL_KHR_no_config_context) + a
	// surfaceless current (EGL_KHR_surfaceless_context). Both are mesa radeonsi features.
	EGLint ctxattr[] = {EGL_CONTEXT_MAJOR_VERSION, 3, EGL_CONTEXT_MINOR_VERSION, 3, EGL_NONE};
	egl_context = eglCreateContext(egl_display, EGL_NO_CONFIG_KHR, EGL_NO_CONTEXT, ctxattr);
	if (egl_context == EGL_NO_CONTEXT)
		die("eglCreateContext (needs EGL_KHR_no_config_context)");
	if (!eglMakeCurrent(egl_display, EGL_NO_SURFACE, EGL_NO_SURFACE, egl_context))
		die("eglMakeCurrent (needs EGL_KHR_surfaceless_context)");
}

static double now_s(void) {
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return ts.tv_sec + ts.tv_nsec / 1e9;
}

// read_full: read exactly n bytes from fd (looping short reads). Returns 1 ok, 0 on EOF/short.
static int read_full(int fd, unsigned char *buf, size_t n) {
	size_t got = 0;
	while (got < n) {
		ssize_t r = read(fd, buf + got, n - got);
		if (r <= 0)
			return 0;
		got += (size_t)r;
	}
	return 1;
}

// --- src frame (stdin BGRA) -> GL texture -> blit into the libfunnel dmabuf FBO -----------------
// Mode M_STDIN: raw BGRA frames arrive on stdin (fed by `ffmpeg -hwaccel vaapi -i file -f rawvideo
// -pix_fmt bgra -`). We upload each to a texture once and blit it (Y-flipped so a top-down source is
// published top-down like Spout2PW) into every published dmabuf buffer; frame-doubling re-publishes
// the SAME texture so the 60 Hz cadence is exercised with real 4K content from a 30 fps source.
static GLuint g_srctex, g_readfb;
static int g_flipy = 1;
// Read-ahead: a reader thread fills a 3-slot mailbox from stdin, so the present loop never blocks on
// the (bursty) ffmpeg pipe -> the producer's own cadence stays even (a valid reference for the owner's
// recorded-set test). The present loop paces at the target fps and holds the last frame between source
// frames (so a 30 fps file shown at 60 repeats each frame ~2x, evenly). Triple-buffer: reader owns
// one slot, main owns one, the third holds the latest; swaps keep the three distinct, the lock guards
// only the index swap (never the frame copy).
static unsigned char *g_buf[3];
static int g_bw, g_bh;
static size_t g_fsz;
static int g_reader_i = 0, g_ready_i = 1, g_main_i = 2, g_dirty, g_eof, g_have_frame;
static pthread_mutex_t g_bmu = PTHREAD_MUTEX_INITIALIZER;
static pthread_t g_reader_th;

static void *stdin_reader(void *arg) {
	(void)arg;
	while (read_full(0, g_buf[g_reader_i], g_fsz)) {
		pthread_mutex_lock(&g_bmu);
		int t = g_ready_i;
		g_ready_i = g_reader_i;
		g_reader_i = t;
		g_dirty = 1;
		pthread_mutex_unlock(&g_bmu);
	}
	pthread_mutex_lock(&g_bmu);
	g_eof = 1;
	pthread_mutex_unlock(&g_bmu);
	return NULL;
}

static void stdin_setup(int w, int h) {
	g_bw = w;
	g_bh = h;
	g_fsz = (size_t)w * h * 4;
	for (int i = 0; i < 3; i++) {
		g_buf[i] = malloc(g_fsz);
		if (!g_buf[i])
			die("malloc srcbuf");
	}
	glGenTextures(1, &g_srctex);
	glBindTexture(GL_TEXTURE_2D, g_srctex);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
	glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, w, h, 0, GL_BGRA, GL_UNSIGNED_BYTE, NULL);
	glGenFramebuffers(1, &g_readfb);
	glBindFramebuffer(GL_READ_FRAMEBUFFER, g_readfb);
	glFramebufferTexture2D(GL_READ_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, g_srctex, 0);
	glBindFramebuffer(GL_READ_FRAMEBUFFER, 0);
	if (getenv("RPW_PROD_NOFLIPY"))
		g_flipy = 0;
	pthread_create(&g_reader_th, NULL, stdin_reader, NULL);
}

// stdin_grab: non-blocking - if the reader has a newer frame, swap it in + upload it; else hold the
// current texture (frame-hold). Returns 1 once any frame has arrived, plus whether the reader is done.
static int stdin_grab(int *eof_out) {
	int got = 0;
	pthread_mutex_lock(&g_bmu);
	if (g_dirty) {
		int t = g_main_i;
		g_main_i = g_ready_i;
		g_ready_i = t;
		g_dirty = 0;
		got = 1;
	}
	*eof_out = g_eof && !g_dirty;
	pthread_mutex_unlock(&g_bmu);
	if (got) {
		glBindTexture(GL_TEXTURE_2D, g_srctex);
		glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, g_bw, g_bh, GL_BGRA, GL_UNSIGNED_BYTE, g_buf[g_main_i]);
		g_have_frame = 1;
	}
	return g_have_frame;
}

// blit_src: copy the current src texture into the bound DRAW fbo (the dmabuf), optional Y-flip.
static void blit_src(int w, int h) {
	glBindFramebuffer(GL_READ_FRAMEBUFFER, g_readfb);
	if (g_flipy)
		glBlitFramebuffer(0, 0, w, h, 0, h, w, 0, GL_COLOR_BUFFER_BIT, GL_NEAREST);
	else
		glBlitFramebuffer(0, 0, w, h, 0, 0, w, h, GL_COLOR_BUFFER_BIT, GL_NEAREST);
	glBindFramebuffer(GL_READ_FRAMEBUFFER, 0);
}

// draw_bar: a smooth horizontal sweep (a bright vertical bar moving L->R, wrapping) on a dark field -
// a synthetic even-motion 4K source where any dropped/duplicated frame is visible to the eye and
// countable from captures. No stdin, no shaders.
static void draw_bar(int w, int h, unsigned frame) {
	glViewport(0, 0, w, h);
	glDisable(GL_SCISSOR_TEST);
	glClearColor(0.05f, 0.05f, 0.08f, 1.0f);
	glClear(GL_COLOR_BUFFER_BIT);
	glEnable(GL_SCISSOR_TEST);
	int bw = w / 48;
	if (bw < 4)
		bw = 4;
	int speed = w / 90; // ~1.5 s per sweep at 60 fps
	if (speed < 1)
		speed = 1;
	int x = (int)((frame * (unsigned)speed) % (unsigned)w);
	glScissor(x, 0, bw, h);
	glClearColor(0.95f, 0.95f, 1.0f, 1.0f);
	glClear(GL_COLOR_BUFFER_BIT);
	if (x + bw > w) { // wrap the leading edge
		glScissor(0, 0, (x + bw) - w, h);
		glClear(GL_COLOR_BUFFER_BIT);
	}
	glDisable(GL_SCISSOR_TEST);
}

// interval stats over a 2 s window: confirms the producer's OWN presentation cadence is even (a valid
// reference) before we blame the consumer.
struct ival {
	double win_t0, last;
	int n;
	double sum, max;
	int over25, over50;
};
static void ival_tick(struct ival *v, double t) {
	if (v->last > 0) {
		double dt = (t - v->last) * 1000.0;
		v->sum += dt;
		v->n++;
		if (dt > v->max)
			v->max = dt;
		if (dt > 25)
			v->over25++;
		if (dt > 50)
			v->over50++;
	}
	v->last = t;
	if (v->win_t0 == 0)
		v->win_t0 = t;
	if (t - v->win_t0 >= 2.0) {
		fprintf(stderr, "funnel-producer: present n=%d mean=%.2fms max=%.2fms >25ms=%d >50ms=%d\n",
			v->n, v->n ? v->sum / v->n : 0, v->max, v->over25, v->over50);
		v->win_t0 = t;
		v->n = 0;
		v->sum = 0;
		v->max = 0;
		v->over25 = 0;
		v->over50 = 0;
	}
}

// noise_enabled: RPW_PROD_NOISE => high-motion, high-entropy frames (a grid of per-cell random
// colours that change every frame) to stress the encoder's bitrate ceiling. Still paints the 4 corners
// so orientation stays checkable.
static int noise_enabled(void) {
	static int v = -1;
	if (v < 0)
		v = getenv("RPW_PROD_NOISE") != NULL;
	return v;
}

static void draw_pattern(int w, int h, unsigned frame) {
	glViewport(0, 0, w, h);
	glDisable(GL_SCISSOR_TEST);
	if (noise_enabled()) {
		// coarse random-colour grid, reseeded per frame: defeats intra+inter compression => near-max
		// bitrate, the high-motion case for ceiling testing.
		glEnable(GL_SCISSOR_TEST);
		int cs = 64; /* cell size */
		unsigned seed = frame * 2654435761u + 12345u;
		for (int y = 0; y < h; y += cs) {
			for (int x = 0; x < w; x += cs) {
				seed = seed * 1103515245u + 12345u;
				float r = ((seed >> 16) & 0xff) / 255.0f;
				float g = ((seed >> 8) & 0xff) / 255.0f;
				float b = (seed & 0xff) / 255.0f;
				int ch = (y + cs <= h) ? cs : (h - y);
				int cw = (x + cs <= w) ? cs : (w - x);
				glScissor(x, y, cw, ch);
				glClearColor(r, g, b, 1.0f);
				glClear(GL_COLOR_BUFFER_BIT);
			}
		}
		glDisable(GL_SCISSOR_TEST);
		goto corners;
	}
	// background encodes the frame counter in the red channel (low byte) so the consumer can
	// confirm frames advance; green/blue fixed so channel order is checkable.
	glClearColor((frame & 0xff) / 255.0f, 0.20f, 0.60f, 1.0f);
	glClear(GL_COLOR_BUFFER_BIT);
corners:
	glEnable(GL_SCISSOR_TEST);
	int s = w < h ? w / 8 : h / 8;
	if (s < 8)
		s = 8;
	// GL scissor origin is bottom-left. TL = top-left in image space = top in GL = y=h-s.
	struct {
		int x, y;
		float r, g, b;
	} corners[] = {
		{0, h - s, 1, 0, 0},     // image top-left  -> red
		{w - s, h - s, 0, 1, 0}, // image top-right -> green
		{0, 0, 0, 0, 1},         // image bottom-left -> blue
		{w - s, 0, 1, 1, 1},     // image bottom-right -> white
	};
	for (int i = 0; i < 4; i++) {
		glScissor(corners[i].x, corners[i].y, s, s);
		glClearColor(corners[i].r, corners[i].g, corners[i].b, 1.0f);
		glClear(GL_COLOR_BUFFER_BIT);
	}
	glDisable(GL_SCISSOR_TEST);
}

int main(int argc, char **argv) {
	if (argc < 5) {
		fprintf(stderr, "usage: %s <w> <h> <fps> <name> [-implicit|-explicit|-hybrid|-either]\n", argv[0]);
		return 2;
	}
	int w = atoi(argv[1]), h = atoi(argv[2]);
	double fps = atof(argv[3]);
	const char *name = argv[4];
	enum funnel_sync fe = FUNNEL_SYNC_BOTH, be = FUNNEL_SYNC_BOTH;
	enum { M_PATTERN, M_BAR, M_STDIN } mode = M_PATTERN;
	int dbl = 1; // frame-doubling: publish each SOURCE frame `dbl` times (stdin mode)
	for (int i = 5; i < argc; i++) {
		if (!strcmp(argv[i], "-implicit"))
			fe = be = FUNNEL_SYNC_IMPLICIT;
		else if (!strcmp(argv[i], "-explicit"))
			fe = be = FUNNEL_SYNC_EXPLICIT;
		else if (!strcmp(argv[i], "-hybrid"))
			fe = FUNNEL_SYNC_EXPLICIT, be = FUNNEL_SYNC_BOTH;
		else if (!strcmp(argv[i], "-either"))
			fe = be = FUNNEL_SYNC_BOTH;
		else if (!strcmp(argv[i], "-stdin"))
			mode = M_STDIN;
		else if (!strcmp(argv[i], "-bar"))
			mode = M_BAR;
		else if (!strcmp(argv[i], "-double"))
			dbl = 2;
		else if (!strncmp(argv[i], "-double=", 8))
			dbl = atoi(argv[i] + 8) > 0 ? atoi(argv[i] + 8) : 1;
	}

	init_egl_surfaceless("/dev/dri/renderD128");

	struct funnel_ctx *ctx;
	struct funnel_stream *stream;
	if (funnel_new(&ctx))
		die("funnel_new");
	funnel_set_app_name(ctx, "rave-mate test producer");
	funnel_set_app_id(ctx, "page.rave.mate.testprod");
	funnel_set_app_version(ctx, "0.1");
	if (funnel_connect(ctx))
		die("funnel_connect");
	if (funnel_stream_create(ctx, name, &stream))
		die("funnel_stream_create");
	if (funnel_stream_init_egl(stream, egl_display))
		die("funnel_stream_init_egl");
	funnel_stream_set_size(stream, w, h);
	funnel_stream_set_mode(stream, FUNNEL_ASYNC);
	funnel_stream_set_sync(stream, fe, be);
	funnel_stream_set_rate(stream, FUNNEL_FRACTION((uint32_t)fps, 1), FUNNEL_FRACTION((uint32_t)fps, 1), FUNNEL_FRACTION((uint32_t)fps, 1));
	funnel_stream_egl_add_format(stream, FUNNEL_EGL_FORMAT_RGBA8888);
	funnel_stream_egl_add_format(stream, FUNNEL_EGL_FORMAT_RGB888);
	if (funnel_stream_configure(stream))
		die("funnel_stream_configure");
	if (funnel_stream_start(stream))
		die("funnel_stream_start");

	GLuint fb = 0;
	glGenFramebuffers(1, &fb);
	if (mode == M_STDIN)
		stdin_setup(w, h);
	fprintf(stderr, "funnel-producer: streaming '%s' %dx%d @%.0f mode=%s double=%d\n", name, w, h, fps,
		mode == M_STDIN ? "stdin" : mode == M_BAR ? "bar" : "pattern", dbl);

	unsigned frame = 0;
	int eof = 0;
	struct ival iv = {0};
	double period = 1.0 / fps;
	double next = now_s();
	double fps_t0 = now_s();
	unsigned fps_f0 = 0;
	for (;;) {
		double tn = now_s();
		if (tn - fps_t0 >= 2.0) {
			fprintf(stderr, "funnel-producer: render %.1f fps\n", (frame - fps_f0) / (tn - fps_t0));
			fps_t0 = tn;
			fps_f0 = frame;
		}
		// stdin mode: grab the latest decoded frame (non-blocking; the reader thread fills the mailbox).
		// The present cadence stays even regardless of ffmpeg's bursts - frame-hold between source frames.
		if (mode == M_STDIN) {
			int done = 0;
			int ready = stdin_grab(&done);
			if (!ready) {
				if (done) {
					eof = 1;
					break;
				}
				next += period; /* no frame yet: pace + retry, never publish an empty texture */
				double dt = next - now_s();
				if (dt > 0) {
					struct timespec ts = {(time_t)dt, (long)((dt - (time_t)dt) * 1e9)};
					nanosleep(&ts, NULL);
				} else {
					next = now_s();
				}
				continue;
			}
		}
		struct funnel_buffer *buf = NULL;
		if (funnel_stream_dequeue(stream, &buf) < 0)
			break;
		if (buf) {
			EGLImage image;
			if (funnel_buffer_get_egl_image(buf, &image) == 0) {
				if (funnel_buffer_has_sync(buf)) {
					EGLSync acq;
					if (funnel_buffer_get_acquire_egl_sync(buf, &acq) == 0) {
						eglWaitSync(egl_display, acq, 0);
						eglDestroySync(egl_display, acq);
					}
				}
				GLuint tex;
				glGenTextures(1, &tex);
				glBindTexture(GL_TEXTURE_2D, tex);
				glEGLImageTargetTexture2DOES(GL_TEXTURE_2D, image);
				glBindFramebuffer(GL_DRAW_FRAMEBUFFER, fb);
				glFramebufferTexture2D(GL_DRAW_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, tex, 0);
				if (mode == M_STDIN)
					blit_src(w, h);
				else if (mode == M_BAR)
					draw_bar(w, h, frame);
				else
					draw_pattern(w, h, frame);
				glBindFramebuffer(GL_DRAW_FRAMEBUFFER, 0);
				glDeleteTextures(1, &tex);
				if (funnel_buffer_has_sync(buf)) {
					EGLSync rel = eglCreateSync(egl_display, EGL_SYNC_NATIVE_FENCE_ANDROID, NULL);
					funnel_buffer_set_release_egl_sync(buf, rel);
					eglDestroySync(egl_display, rel);
				} else {
					glFlush();
				}
			}
			funnel_stream_enqueue(stream, buf);
			ival_tick(&iv, now_s());
			frame++;
		}
		next += period;
		double dt = next - now_s();
		if (dt > 0) {
			struct timespec ts = {(time_t)dt, (long)((dt - (time_t)dt) * 1e9)};
			nanosleep(&ts, NULL);
		} else {
			next = now_s();
		}
	}
	if (eof)
		fprintf(stderr, "funnel-producer: stdin EOF after %u frames\n", frame);
	funnel_stream_stop(stream);
	funnel_stream_destroy(stream);
	funnel_shutdown(ctx);
	return 0;
}
