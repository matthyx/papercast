#include "native_capture.h"

#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
#include <stdbool.h>
#include <string.h>
#include <time.h>

#include <pipewire/pipewire.h>
#include <spa/param/video/format-utils.h>
#include <spa/param/video/raw.h>
#include <spa/param/param.h>

#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/hwcontext.h>
#include <libavutil/imgutils.h>
#include <libswscale/swscale.h>

struct native_capture {
    int pw_fd;
    uint32_t node_id;
    int fps;
    int qp;
    uintptr_t go_handle;

    struct pw_thread_loop *thread_loop;
    struct pw_context *context;
    struct pw_core *core;
    struct spa_hook core_listener;
    struct pw_registry *registry;
    struct spa_hook registry_listener;
    struct pw_stream *stream;
    struct spa_hook stream_listener;
    struct spa_video_info_raw video_format;
    bool has_format;
    bool running;
    bool stopping;

    // Encoder
    const AVCodec *codec;
    bool is_hw_vaapi;
    AVBufferRef *hw_device_ctx;
    AVCodecContext *enc_ctx;
    AVFrame *sw_frame;
    AVFrame *hw_frame;
    struct SwsContext *sws;
    AVPacket *pkt;
    int enc_width;
    int enc_height;
    int64_t frame_index;
    int64_t last_encode_ns;
};

static void cleanup_encoder(struct native_capture *cap) {
    if (cap->sws) {
        sws_freeContext(cap->sws);
        cap->sws = NULL;
    }
    if (cap->sw_frame) {
        av_frame_free(&cap->sw_frame);
    }
    if (cap->hw_frame) {
        av_frame_free(&cap->hw_frame);
    }
    if (cap->pkt) {
        av_packet_free(&cap->pkt);
    }
    if (cap->enc_ctx) {
        avcodec_free_context(&cap->enc_ctx);
    }
    if (cap->hw_device_ctx) {
        av_buffer_unref(&cap->hw_device_ctx);
    }
    cap->is_hw_vaapi = false;
}

static int init_encoder(struct native_capture *cap, int width, int height, enum AVPixelFormat in_pix_fmt) {
    cleanup_encoder(cap);

    cap->enc_width = width;
    cap->enc_height = height;
    cap->frame_index = 0;
    cap->last_encode_ns = 0;

    int fps = cap->fps > 0 ? cap->fps : 30;
    int gop = fps / 2 >= 10 ? fps / 2 : 15;
    int qp = cap->qp > 0 ? cap->qp : 26;

    // 1. Try VA-API hardware encoder first
    cap->codec = avcodec_find_encoder_by_name("h264_vaapi");
    if (cap->codec != NULL) {
        int ret = av_hwdevice_ctx_create(&cap->hw_device_ctx, AV_HWDEVICE_TYPE_VAAPI, "/dev/dri/renderD128", NULL, 0);
        if (ret >= 0) {
            cap->enc_ctx = avcodec_alloc_context3(cap->codec);
            cap->enc_ctx->width = width;
            cap->enc_ctx->height = height;
            cap->enc_ctx->time_base = (AVRational){1, fps};
            cap->enc_ctx->framerate = (AVRational){fps, 1};
            cap->enc_ctx->pix_fmt = AV_PIX_FMT_VAAPI;
            cap->enc_ctx->gop_size = gop;
            cap->enc_ctx->max_b_frames = 0;
            cap->enc_ctx->flags |= AV_CODEC_FLAG_LOW_DELAY;
            cap->enc_ctx->profile = AV_PROFILE_H264_CONSTRAINED_BASELINE;

            av_opt_set_int(cap->enc_ctx->priv_data, "qp", qp, 0);

            AVBufferRef *hw_frames_ref = av_hwframe_ctx_alloc(cap->hw_device_ctx);
            AVHWFramesContext *frames_ctx = (AVHWFramesContext *)(hw_frames_ref->data);
            frames_ctx->format = AV_PIX_FMT_VAAPI;
            frames_ctx->sw_format = AV_PIX_FMT_NV12;
            frames_ctx->width = width;
            frames_ctx->height = height;
            frames_ctx->initial_pool_size = 8;
            if (av_hwframe_ctx_init(hw_frames_ref) >= 0) {
                cap->enc_ctx->hw_frames_ctx = av_buffer_ref(hw_frames_ref);
                av_buffer_unref(&hw_frames_ref);

                if (avcodec_open2(cap->enc_ctx, cap->codec, NULL) >= 0) {
                    cap->is_hw_vaapi = true;
                    cap->sw_frame = av_frame_alloc();
                    cap->sw_frame->format = AV_PIX_FMT_NV12;
                    cap->sw_frame->width = width;
                    cap->sw_frame->height = height;
                    av_frame_get_buffer(cap->sw_frame, 32);

                    cap->hw_frame = av_frame_alloc();
                    cap->pkt = av_packet_alloc();

                    cap->sws = sws_getContext(
                        width, height, in_pix_fmt,
                        width, height, AV_PIX_FMT_NV12,
                        SWS_FAST_BILINEAR, NULL, NULL, NULL);

                    fprintf(stderr, "[native] Hardware VA-API encoder initialized (%dx%d, QP=%d, Constrained Baseline)\n",
                            width, height, qp);
                    return 0;
                }
            } else {
                av_buffer_unref(&hw_frames_ref);
            }
        }
        cleanup_encoder(cap);
    }

    // 2. Fallback to software libx264
    fprintf(stderr, "[native] Falling back to software libx264 encoder\n");
    cap->codec = avcodec_find_encoder_by_name("libx264");
    if (!cap->codec) {
        fprintf(stderr, "[native] Error: no H.264 encoder available\n");
        return -1;
    }

    cap->enc_ctx = avcodec_alloc_context3(cap->codec);
    cap->enc_ctx->width = width;
    cap->enc_ctx->height = height;
    cap->enc_ctx->time_base = (AVRational){1, fps};
    cap->enc_ctx->framerate = (AVRational){fps, 1};
    cap->enc_ctx->pix_fmt = AV_PIX_FMT_YUV420P;
    cap->enc_ctx->gop_size = fps;
    cap->enc_ctx->max_b_frames = 0;
    cap->enc_ctx->flags |= AV_CODEC_FLAG_LOW_DELAY;
    cap->enc_ctx->profile = AV_PROFILE_H264_CONSTRAINED_BASELINE;

    av_opt_set(cap->enc_ctx->priv_data, "preset", "ultrafast", 0);
    av_opt_set(cap->enc_ctx->priv_data, "tune", "zerolatency", 0);

    if (avcodec_open2(cap->enc_ctx, cap->codec, NULL) < 0) {
        fprintf(stderr, "[native] Error: failed to open libx264\n");
        cleanup_encoder(cap);
        return -1;
    }

    cap->sw_frame = av_frame_alloc();
    cap->sw_frame->format = AV_PIX_FMT_YUV420P;
    cap->sw_frame->width = width;
    cap->sw_frame->height = height;
    av_frame_get_buffer(cap->sw_frame, 32);

    cap->pkt = av_packet_alloc();
    cap->sws = sws_getContext(
        width, height, in_pix_fmt,
        width, height, AV_PIX_FMT_YUV420P,
        SWS_FAST_BILINEAR, NULL, NULL, NULL);

    fprintf(stderr, "[native] Software libx264 encoder initialized (%dx%d, ultrafast, zerolatency)\n", width, height);
    return 0;
}

static enum AVPixelFormat spa_format_to_av(uint32_t spa_format) {
    switch (spa_format) {
        case SPA_VIDEO_FORMAT_RGBA: return AV_PIX_FMT_RGBA;
        case SPA_VIDEO_FORMAT_RGBx: return AV_PIX_FMT_RGB0;
        case SPA_VIDEO_FORMAT_BGRA: return AV_PIX_FMT_BGRA;
        case SPA_VIDEO_FORMAT_BGRx: return AV_PIX_FMT_BGR0;
        case SPA_VIDEO_FORMAT_NV12: return AV_PIX_FMT_NV12;
        default: return AV_PIX_FMT_RGBA;
    }
}

static void on_process(void *userdata) {
    struct native_capture *cap = userdata;
    struct pw_buffer *b = pw_stream_dequeue_buffer(cap->stream);
    if (!b) return;

    struct spa_buffer *buf = b->buffer;
    uint8_t *src_data = buf->datas[0].data;

    if (src_data != NULL && cap->enc_ctx != NULL && cap->sws != NULL && cap->sw_frame != NULL) {
        if (cap->fps > 0 && cap->fps < 30) {
            struct timespec ts;
            clock_gettime(CLOCK_MONOTONIC, &ts);
            int64_t now_ns = (int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec;
            int64_t min_interval_ns = (1000000000LL / cap->fps) - 2000000LL;
            if (cap->last_encode_ns > 0 && (now_ns - cap->last_encode_ns) < min_interval_ns) {
                pw_stream_queue_buffer(cap->stream, b);
                return;
            }
            cap->last_encode_ns = now_ns;
        }

        uint8_t *src_slices[1] = { src_data };
        int src_stride = buf->datas[0].chunk->stride;
        if (src_stride <= 0) {
            src_stride = cap->enc_width * 4;
        }
        int src_strides[1] = { src_stride };

        // Scale/convert into encoder input frame
        sws_scale(cap->sws, (const uint8_t * const*)src_slices, src_strides, 0, cap->enc_height,
                  cap->sw_frame->data, cap->sw_frame->linesize);

        cap->sw_frame->pts = cap->frame_index++;

        // Return PipeWire buffer back IMMEDIATELY so compositor/portal never starves or crashes
        pw_stream_queue_buffer(cap->stream, b);
        b = NULL;

        if (cap->is_hw_vaapi) {
            if (av_hwframe_get_buffer(cap->enc_ctx->hw_frames_ctx, cap->hw_frame, 0) == 0) {
                av_hwframe_transfer_data(cap->hw_frame, cap->sw_frame, 0);
                cap->hw_frame->pts = cap->sw_frame->pts;

                if (avcodec_send_frame(cap->enc_ctx, cap->hw_frame) == 0) {
                    while (avcodec_receive_packet(cap->enc_ctx, cap->pkt) == 0) {
                        bool is_key = (cap->pkt->flags & AV_PKT_FLAG_KEY) != 0;
                        goOnNativeFrame(cap->pkt->data, cap->pkt->size, is_key, cap->go_handle);
                        av_packet_unref(cap->pkt);
                    }
                }
                av_frame_unref(cap->hw_frame);
            }
        } else {
            if (avcodec_send_frame(cap->enc_ctx, cap->sw_frame) == 0) {
                while (avcodec_receive_packet(cap->enc_ctx, cap->pkt) == 0) {
                    bool is_key = (cap->pkt->flags & AV_PKT_FLAG_KEY) != 0;
                    goOnNativeFrame(cap->pkt->data, cap->pkt->size, is_key, cap->go_handle);
                    av_packet_unref(cap->pkt);
                }
            }
        }
    }

    if (b) {
        pw_stream_queue_buffer(cap->stream, b);
    }
}

static void on_param_changed(void *userdata, uint32_t id, const struct spa_pod *param) {
    struct native_capture *cap = userdata;
    if (id != SPA_PARAM_Format) return;

    if (!param) {
        cap->has_format = false;
        return;
    }

    if (spa_format_video_raw_parse(param, &cap->video_format) < 0) {
        fprintf(stderr, "[native] Error: failed to parse raw video format\n");
        return;
    }

    int width = cap->video_format.size.width;
    int height = cap->video_format.size.height;
    enum AVPixelFormat av_fmt = spa_format_to_av(cap->video_format.format);

    fprintf(stderr, "[native] Mode negotiation: %dx%d (SPA format %d) @ %d/%d fps\n",
            width, height, cap->video_format.format,
            cap->video_format.framerate.num, cap->video_format.framerate.denom);

    if (init_encoder(cap, width, height, av_fmt) < 0) {
        fprintf(stderr, "[native] Error: Failed to configure encoder for %dx%d\n", width, height);
        return;
    }

    cap->has_format = true;

    // Acknowledge parameters and request buffers (support both MemPtr and MemFd)
    // Enforce strict double-buffering (min 1, default 2, max 2) so xdg-desktop-portal-cosmic
    // never queues concurrent create_frame requests that violate ext_image_copy_capture_v1 protocol.
    uint8_t buffer[1024];
    struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
    const struct spa_pod *params[1];
    params[0] = spa_pod_builder_add_object(&b,
        SPA_TYPE_OBJECT_ParamBuffers, SPA_PARAM_Buffers,
        SPA_PARAM_BUFFERS_buffers, SPA_POD_CHOICE_RANGE_Int(2, 1, 2),
        SPA_PARAM_BUFFERS_blocks,  SPA_POD_Int(1),
        SPA_PARAM_BUFFERS_dataType, SPA_POD_CHOICE_FLAGS_Int((1<<SPA_DATA_MemPtr) | (1<<SPA_DATA_MemFd))
    );
    pw_stream_update_params(cap->stream, params, 1);
}

static void on_state_changed(void *userdata, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
    struct native_capture *cap = userdata;
    if (cap->stopping) return;
    fprintf(stderr, "[native] PipeWire stream state: %s -> %s (err: %s)\n",
            pw_stream_state_as_string(old), pw_stream_state_as_string(state), error ? error : "none");
    if (state == PW_STREAM_STATE_PAUSED) {
        pw_stream_set_active(cap->stream, true);
    }
    goOnNativeState((int)old, (int)state, cap->go_handle);
}

static const struct pw_stream_events stream_events = {
    .version = PW_VERSION_STREAM_EVENTS,
    .process = on_process,
    .param_changed = on_param_changed,
    .state_changed = on_state_changed,
};

static void on_core_error(void *data, uint32_t id, int seq, int res, const char *message) {
    struct native_capture *cap = data;
    if (cap->stopping) return;
    fprintf(stderr, "[native] PipeWire core error (id=%u, res=%d): %s\n", id, res, message ? message : "none");
    goOnNativeState(3, -1, cap->go_handle);
}

static const struct pw_core_events core_events = {
    .version = PW_VERSION_CORE_EVENTS,
    .error = on_core_error,
};

static void on_registry_global(void *data, uint32_t id, uint32_t permissions, const char *type, uint32_t version, const struct spa_dict *props) {
    // Unused
}

static void on_registry_global_remove(void *data, uint32_t id) {
    struct native_capture *cap = data;
    if (cap->stopping) return;
    if (id == cap->node_id) {
        fprintf(stderr, "[native] ScreenCast source node %u removed from PipeWire registry! Reviving immediately...\n", id);
        goOnNativeState(3, -1, cap->go_handle);
    }
}

static const struct pw_registry_events registry_events = {
    .version = PW_VERSION_REGISTRY_EVENTS,
    .global = on_registry_global,
    .global_remove = on_registry_global_remove,
};

native_capture_t *native_capture_create(int pw_fd, uint32_t node_id, int fps, int qp, uintptr_t go_handle) {
    pw_init(NULL, NULL);

    struct native_capture *cap = calloc(1, sizeof(struct native_capture));
    cap->pw_fd = pw_fd;
    cap->node_id = node_id;
    cap->fps = fps;
    cap->qp = qp;
    cap->go_handle = go_handle;

    return cap;
}

int native_capture_start(native_capture_t *cap) {
    if (!cap) return -1;

    cap->thread_loop = pw_thread_loop_new("native-cap", NULL);
    cap->context = pw_context_new(pw_thread_loop_get_loop(cap->thread_loop), NULL, 0);

    pw_thread_loop_start(cap->thread_loop);
    pw_thread_loop_lock(cap->thread_loop);

    cap->core = pw_context_connect_fd(cap->context, cap->pw_fd, NULL, 0);
    if (!cap->core) {
        fprintf(stderr, "[native] Failed to connect to PipeWire fd %d\n", cap->pw_fd);
        pw_thread_loop_unlock(cap->thread_loop);
        return -1;
    }
    pw_core_add_listener(cap->core, &cap->core_listener, &core_events, cap);

    cap->registry = pw_core_get_registry(cap->core, PW_VERSION_REGISTRY, 0);
    if (cap->registry) {
        pw_registry_add_listener(cap->registry, &cap->registry_listener, &registry_events, cap);
    }

    struct pw_properties *props = pw_properties_new(
        PW_KEY_MEDIA_TYPE, "Video",
        PW_KEY_MEDIA_CATEGORY, "Capture",
        PW_KEY_MEDIA_ROLE, "Screen",
        NULL
    );

    cap->stream = pw_stream_new(cap->core, "papercast-native", props);
    pw_stream_add_listener(cap->stream, &cap->stream_listener, &stream_events, cap);

    uint8_t buffer[1024];
    struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
    struct spa_pod_frame f[2];
    const struct spa_pod *params[1];

    spa_pod_builder_push_object(&b, &f[0], SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat);
    spa_pod_builder_add(&b,
        SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video),
        SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw),
        0);
    spa_pod_builder_prop(&b, SPA_FORMAT_VIDEO_format, 0);
    spa_pod_builder_push_choice(&b, &f[1], SPA_CHOICE_Enum, 0);
    spa_pod_builder_id(&b, SPA_VIDEO_FORMAT_RGBA);
    spa_pod_builder_id(&b, SPA_VIDEO_FORMAT_RGBA);
    spa_pod_builder_id(&b, SPA_VIDEO_FORMAT_BGRA);
    spa_pod_builder_id(&b, SPA_VIDEO_FORMAT_BGRx);
    spa_pod_builder_id(&b, SPA_VIDEO_FORMAT_RGBx);
    spa_pod_builder_id(&b, SPA_VIDEO_FORMAT_NV12);
    spa_pod_builder_pop(&b, &f[1]);

    int target_fps = cap->fps > 0 ? cap->fps : 30;
    spa_pod_builder_add(&b,
        SPA_FORMAT_VIDEO_size, SPA_POD_CHOICE_RANGE_Rectangle(
            &SPA_RECTANGLE(1920, 1080),
            &SPA_RECTANGLE(1, 1),
            &SPA_RECTANGLE(7680, 4320)),
        SPA_FORMAT_VIDEO_framerate, SPA_POD_CHOICE_RANGE_Fraction(
            &SPA_FRACTION(target_fps, 1),
            &SPA_FRACTION(1, 1),
            &SPA_FRACTION(60, 1)),
        0);
    params[0] = spa_pod_builder_pop(&b, &f[0]);

    // Connect directly to target node with AUTOCONNECT (links to node) and DONT_RECONNECT (avoids 30s limbo if node dies)
    int res = pw_stream_connect(cap->stream,
        PW_DIRECTION_INPUT,
        cap->node_id,
        PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_DONT_RECONNECT | PW_STREAM_FLAG_MAP_BUFFERS | PW_STREAM_FLAG_ASYNC,
        params, 1);

    pw_thread_loop_unlock(cap->thread_loop);

    if (res < 0) {
        fprintf(stderr, "[native] Failed to connect stream: %d\n", res);
        return -1;
    }

    cap->running = true;
    fprintf(stderr, "[native] Native capture pipeline started successfully\n");
    return 0;
}

void native_capture_stop(native_capture_t *cap) {
    if (!cap || !cap->running) return;
    cap->stopping = true;
    cap->running = false;

    if (cap->thread_loop) {
        pw_thread_loop_stop(cap->thread_loop);
    }
    if (cap->stream) {
        pw_stream_destroy(cap->stream);
        cap->stream = NULL;
    }
    if (cap->registry) {
        spa_hook_remove(&cap->registry_listener);
        pw_proxy_destroy((struct pw_proxy *)cap->registry);
        cap->registry = NULL;
    }
    if (cap->core) {
        spa_hook_remove(&cap->core_listener);
        cap->core = NULL;
    }
    if (cap->context) {
        pw_context_destroy(cap->context);
        cap->context = NULL;
    }
    if (cap->thread_loop) {
        pw_thread_loop_destroy(cap->thread_loop);
        cap->thread_loop = NULL;
    }
    cleanup_encoder(cap);
    fprintf(stderr, "[native] Native capture pipeline stopped cleanly\n");
}

void native_capture_free(native_capture_t *cap) {
    if (!cap) return;
    native_capture_stop(cap);
    free(cap);
}
