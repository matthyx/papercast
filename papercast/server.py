"""
server.py: Async HTTP & WebSocket server for Papercast
"""
import asyncio
import logging
from http import HTTPStatus
import os
import websockets
from .portal import ScreenCastPortal
from .pipeline import ScreenCapturePipeline

logger = logging.getLogger("papercast.server")


class PapercastServer:
    def __init__(self, host="0.0.0.0", port=1701, fps=30, bitrate_kbps=3000):
        self.host = host
        self.port = port
        self.fps = fps
        self.bitrate_kbps = bitrate_kbps
        self.portal = None
        self.pipeline = None
        self.client_queues = {}  # websocket -> asyncio.Queue
        self.loop = None
        self.static_dir = os.path.join(os.path.dirname(__file__), "static")
        self._capture_lock = asyncio.Lock()

    def _on_frame(self, data: bytes):
        if not self.client_queues or not self.loop:
            return

        self.loop.call_soon_threadsafe(self._broadcast_frame, data)

    def _broadcast_frame(self, data: bytes):
        for ws, q in list(self.client_queues.items()):
            if not ws.open:
                continue
            # Drop older frame if client is slow to keep latency minimal
            if q.full():
                try:
                    q.get_nowait()
                except asyncio.QueueEmpty:
                    pass
            try:
                q.put_nowait(data)
            except asyncio.QueueFull:
                pass

    def start_capture(self):
        if self.pipeline and self.pipeline.is_running:
            return

        logger.info("Initializing ScreenCast portal...")
        self.portal = ScreenCastPortal(cursor_mode=2)  # cursor embedded
        fd, node_id = self.portal.request_screencast()

        logger.info("Setting up GStreamer pipeline (node_id=%d, fd=%d)...", node_id, fd)
        self.pipeline = ScreenCapturePipeline(
            fd=fd,
            node_id=node_id,
            on_frame_callback=self._on_frame,
            bitrate_kbps=self.bitrate_kbps,
            fps=self.fps,
        )
        self.pipeline.start()

    def stop_capture(self):
        if self.pipeline:
            self.pipeline.stop()
            self.pipeline = None
        if self.portal:
            self.portal.close()
            self.portal = None

    async def _process_http_request(self, path, request_headers):
        # WebSocket upgrade request
        if path == "/ws":
            return None

        # Clean path
        clean_path = path.split("?")[0].lstrip("/")
        if not clean_path:
            clean_path = "index.html"

        file_path = os.path.join(self.static_dir, clean_path)
        if os.path.isfile(file_path):
            content_type = "text/html"
            if file_path.endswith(".css"):
                content_type = "text/css"
            elif file_path.endswith(".js"):
                content_type = "application/javascript"

            with open(file_path, "rb") as f:
                content = f.read()

            return (
                HTTPStatus.OK,
                [("Content-Type", content_type), ("Content-Length", str(len(content)))],
                content,
            )

        return (HTTPStatus.NOT_FOUND, [("Content-Type", "text/plain")], b"404 Not Found")

    async def _ws_handler(self, websocket, path=None):
        logger.info("Client connected to stream: %s", websocket.remote_address)
        queue = asyncio.Queue(maxsize=1)
        self.client_queues[websocket] = queue

        # Ensure capture pipeline is running
        async with self._capture_lock:
            if not self.pipeline or not self.pipeline.is_running:
                try:
                    loop = asyncio.get_running_loop()
                    await loop.run_in_executor(None, self.start_capture)
                except Exception as e:
                    logger.error("Failed to start screen capture: %s", e)
                    await websocket.send(f'{{"error": "{str(e)}"}}')

        async def sender():
            try:
                while websocket.open:
                    data = await queue.get()
                    await websocket.send(data)
            except asyncio.CancelledError:
                pass
            except Exception as err:
                logger.debug("Sender ended for %s: %s", websocket.remote_address, err)

        sender_task = asyncio.create_task(sender())

        try:
            async for message in websocket:
                # Handle future client input messages (mouse, touch, key)
                pass
        except Exception as e:
            logger.debug("Client connection ended: %s", e)
        finally:
            sender_task.cancel()
            self.client_queues.pop(websocket, None)
            logger.info("Client disconnected: %s. Active clients: %d", websocket.remote_address, len(self.client_queues))

    async def run(self, auto_capture=True):
        self.loop = asyncio.get_running_loop()
        logger.info("Starting Papercast server on http://%s:%d...", self.host, self.port)
        async with websockets.serve(
            self._ws_handler,
            self.host,
            self.port,
            process_request=self._process_http_request,
            max_size=10 * 1024 * 1024,
        ):
            if auto_capture:
                asyncio.create_task(self._auto_start_capture())
            await asyncio.Future()  # run forever

    async def _auto_start_capture(self):
        try:
            logger.info("Requesting screen capture permission from desktop...")
            async with self._capture_lock:
                loop = asyncio.get_running_loop()
                await loop.run_in_executor(None, self.start_capture)
            logger.info("==> Desktop streaming ready! Open http://127.0.0.1:%d on your HannsNote2 tablet.", self.port)
        except Exception as e:
            logger.warning("Auto capture prompt cancelled or failed (will retry on client connect): %s", e)
