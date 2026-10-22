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
        self.clients = set()
        self.loop = None
        self.static_dir = os.path.join(os.path.dirname(__file__), "static")

    def _on_frame(self, data: bytes):
        if not self.clients or not self.loop:
            return

        # Schedule async broadcast on event loop
        self.loop.call_soon_threadsafe(self._broadcast_frame, data)

    def _broadcast_frame(self, data: bytes):
        for ws in list(self.clients):
            if ws.open:
                asyncio.create_task(self._send_frame_safe(ws, data))

    async def _send_frame_safe(self, ws, data: bytes):
        try:
            await ws.send(data)
        except Exception:
            self.clients.discard(ws)

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

    async def _ws_handler(self, websocket, path):
        logger.info("Client connected to stream: %s", websocket.remote_address)
        self.clients.add(websocket)

        # Start capturing when first client connects
        if len(self.clients) == 1:
            try:
                self.start_capture()
            except Exception as e:
                logger.error("Failed to start screen capture: %s", e)
                await websocket.send(f'{{"error": "{str(e)}"}}')

        try:
            async for message in websocket:
                # Handle future client input messages (mouse, touch, key)
                pass
        except Exception as e:
            logger.debug("Client connection ended: %s", e)
        finally:
            self.clients.discard(websocket)
            logger.info("Client disconnected. Active clients: %d", len(self.clients))
            if not self.clients:
                logger.info("All clients disconnected. Stopping capture pipeline...")
                self.stop_capture()

    async def run(self):
        self.loop = asyncio.get_running_loop()
        logger.info("Starting Papercast server on http://%s:%d...", self.host, self.port)
        async with websockets.serve(
            self._ws_handler,
            self.host,
            self.port,
            process_request=self._process_http_request,
            max_size=10 * 1024 * 1024,
        ):
            await asyncio.Future()  # run forever
