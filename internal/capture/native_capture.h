#ifndef NATIVE_CAPTURE_H
#define NATIVE_CAPTURE_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct native_capture native_capture_t;

// Callback signature implemented in Go (via cgo export)
extern void goOnNativeFrame(uint8_t *data, size_t size, bool is_key, uintptr_t go_handle);

native_capture_t *native_capture_create(int pw_fd, uint32_t node_id, int fps, int qp, uintptr_t go_handle);
int native_capture_start(native_capture_t *cap);
void native_capture_stop(native_capture_t *cap);
void native_capture_free(native_capture_t *cap);

#ifdef __cplusplus
}
#endif

#endif // NATIVE_CAPTURE_H
