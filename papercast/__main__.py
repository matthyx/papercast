import argparse
import asyncio
import logging
import sys
from .server import PapercastServer


def main():
    parser = argparse.ArgumentParser(
        prog="papercast",
        description="Ultra-low-latency desktop streamer for Wayland/COSMIC to e-ink/RLCD tablets using WebCodecs.",
    )
    parser.add_argument("--host", default="0.0.0.0", help="Host bind address (default: 0.0.0.0)")
    parser.add_argument("--port", "-p", type=int, default=1701, help="Port to serve on (default: 1701)")
    parser.add_argument("--fps", type=int, default=30, help="Target framerate (default: 30)")
    parser.add_argument("--bitrate", type=int, default=3000, help="Bitrate in kbps (default: 3000)")
    parser.add_argument("-v", "--verbose", action="store_true", help="Enable verbose debug logging")

    args = parser.parse_args()

    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    )

    server = PapercastServer(
        host=args.host,
        port=args.port,
        fps=args.fps,
        bitrate_kbps=args.bitrate,
    )

    try:
        asyncio.run(server.run())
    except KeyboardInterrupt:
        print("\nStopping Papercast...")
        server.stop_capture()


if __name__ == "__main__":
    main()
