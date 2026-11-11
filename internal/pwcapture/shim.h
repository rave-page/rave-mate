//go:build linux && pipewire

/* shim.h - C surface of the PipeWire capture shim (see shim.c). All PipeWire access is via
 * dlopen at runtime, so a binary built with -tags pipewire still launches where libpipewire-0.3
 * is absent (rpw_available() == 0). */
#ifndef RAVE_PWCAPTURE_SHIM_H
#define RAVE_PWCAPTURE_SHIM_H

typedef struct rpw_capture rpw_capture;

/* rpw_available dlopens libpipewire-0.3 and resolves the symbols we need. 1 = usable. */
int rpw_available(void);

/* rpw_start begins capturing the PipeWire node named target_node. Returns a handle, or NULL on
 * failure. The stream auto-connects + auto-reconnects by name, so a producer that restarts
 * (VRChat) is re-captured without a new handle. */
rpw_capture *rpw_start(const char *target_node);

/* rpw_wait_frame blocks up to timeout_ms for the next frame, converts it to RGBA (Go NRGBA order)
 * into dst. Returns bytes written (>0, == *w * *h * 4), 0 on timeout, -1 on fatal stream error,
 * -2 when cap is too small (sets *w,*h to the needed dims; the frame is kept for the next call). */
int rpw_wait_frame(rpw_capture *c, unsigned char *dst, int cap, int *w, int *h, long timeout_ms);

/* rpw_dropped returns the drop-oldest counter (frames the producer delivered that no Next consumed,
 * plus frames skipped because their buffer type isn't supported - e.g. DMA-BUF before EGL). */
long rpw_dropped(rpw_capture *c);

/* rpw_dmabuf_skipped returns how many frames were DMA-BUF (not handled by the SHM path). Nonzero
 * means the producer is DMA-BUF only and the EGL import path is required. */
long rpw_dmabuf_skipped(rpw_capture *c);

/* rpw_stop disconnects + frees the capture. */
void rpw_stop(rpw_capture *c);

#endif
