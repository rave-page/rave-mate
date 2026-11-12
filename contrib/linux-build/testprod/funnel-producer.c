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

static void draw_pattern(int w, int h, unsigned frame) {
	glViewport(0, 0, w, h);
	glDisable(GL_SCISSOR_TEST);
	// background encodes the frame counter in the red channel (low byte) so the consumer can
	// confirm frames advance; green/blue fixed so channel order is checkable.
	glClearColor((frame & 0xff) / 255.0f, 0.20f, 0.60f, 1.0f);
	glClear(GL_COLOR_BUFFER_BIT);
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
	for (int i = 5; i < argc; i++) {
		if (!strcmp(argv[i], "-implicit"))
			fe = be = FUNNEL_SYNC_IMPLICIT;
		else if (!strcmp(argv[i], "-explicit"))
			fe = be = FUNNEL_SYNC_EXPLICIT;
		else if (!strcmp(argv[i], "-hybrid"))
			fe = FUNNEL_SYNC_EXPLICIT, be = FUNNEL_SYNC_BOTH;
		else if (!strcmp(argv[i], "-either"))
			fe = be = FUNNEL_SYNC_BOTH;
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
	fprintf(stderr, "funnel-producer: streaming '%s' %dx%d @%.0f\n", name, w, h, fps);

	unsigned frame = 0;
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
	funnel_stream_stop(stream);
	funnel_stream_destroy(stream);
	funnel_shutdown(ctx);
	return 0;
}
