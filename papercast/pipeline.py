"""
pipeline.py: GStreamer capture and H.264 low-latency hardware encoding pipeline
"""
import gi
gi.require_version("Gst", "1.0")
gi.require_version("GstApp", "1.0")
from gi.repository import Gst, GstApp, GLib
import logging
import threading

logger = logging.getLogger("papercast.pipeline")


class ScreenCapturePipeline:
    def __init__(self, fd: int, node_id: int, on_frame_callback, bitrate_kbps=3000, fps=30):
        Gst.init(None)
        self.fd = fd
        self.node_id = node_id
        self.on_frame_callback = on_frame_callback
        self.bitrate_kbps = bitrate_kbps
        self.fps = fps
        self.pipeline = None
        self.appsink = None
        self.bus = None
        self.is_running = False

    def build_pipeline(self):
        # Try VA-API hardware encoder first, then x264enc fallback
        encoders = [
            # 1. Intel VA-API hardware encoder (CQP mode for zero latency)
            (
                "video/x-raw,format=NV12 "
                f"! vaapih264enc rate-control=cqp init-qp=24 max-bframes=0 keyframe-period={self.fps} "
                "! video/x-h264,profile=constrained-baseline"
            ),
            # 2. Software fallback
            (
                f"x264enc tune=zerolatency speed-preset=ultrafast bitrate={self.bitrate_kbps} key-int-max={self.fps} "
                "! video/x-h264,profile=constrained-baseline"
            ),
        ]

        for enc in encoders:
            pipeline_desc = (
                f"pipewiresrc fd={self.fd} target-object={self.node_id} do-timestamp=true keepalive-time=1000 ! "
                f"videoconvert ! "
                f"video/x-raw,framerate={self.fps}/1 ! "
                f"{enc} ! "
                f"h264parse config-interval=-1 ! "
                f"video/x-h264,stream-format=byte-stream,alignment=au ! "
                f"appsink name=sink emit-signals=true drop=true max-buffers=1 sync=false"
            )
            logger.info("Trying pipeline: %s", pipeline_desc)
            try:
                pipeline = Gst.parse_launch(pipeline_desc)
                appsink = pipeline.get_by_name("sink")
                appsink.connect("new-sample", self._on_new_sample)
                ret = pipeline.set_state(Gst.State.READY)
                if ret != Gst.StateChangeReturn.FAILURE:
                    self.pipeline = pipeline
                    self.appsink = appsink
                    logger.info("Successfully initialized pipeline with encoder: %s", enc.split()[0])
                    return
            except Exception as e:
                logger.warning("Pipeline with encoder %s failed: %s", enc.split()[0], e)

        raise RuntimeError("Failed to build any working GStreamer pipeline.")

    def _on_new_sample(self, sink):
        sample = sink.emit("pull-sample")
        if not sample:
            return Gst.FlowReturn.OK

        buf = sample.get_buffer()
        if not buf:
            return Gst.FlowReturn.OK

        # Extract buffer bytes
        success, map_info = buf.map(Gst.MapFlags.READ)
        if success:
            try:
                data = bytes(map_info.data)
                if self.on_frame_callback:
                    self.on_frame_callback(data)
            finally:
                buf.unmap(map_info)

        return Gst.FlowReturn.OK

    def start(self):
        if not self.pipeline:
            self.build_pipeline()

        logger.info("Starting GStreamer pipeline...")
        ret = self.pipeline.set_state(Gst.State.PLAYING)
        if ret == Gst.StateChangeReturn.FAILURE:
            raise RuntimeError("GStreamer pipeline failed to enter PLAYING state.")
        self.is_running = True

    def stop(self):
        if self.pipeline:
            logger.info("Stopping GStreamer pipeline...")
            self.pipeline.set_state(Gst.State.NULL)
            self.is_running = False
            self.pipeline = None
            self.appsink = None
