"""
portal.py: Handles xdg-desktop-portal ScreenCast session over D-Bus
"""
import dbus
from dbus.mainloop.glib import DBusGMainLoop
from gi.repository import GLib
import random
import threading
import logging

logger = logging.getLogger("papercast.portal")


class ScreenCastPortal:
    def __init__(self, cursor_mode=2):
        """
        cursor_mode:
            1 = Hidden
            2 = Embedded into video
            4 = Metadata
        """
        self.cursor_mode = cursor_mode
        self.bus = None
        self.portal = None
        self.screencast = None
        self.session_path = None
        self.loop = None
        self.thread = None

    def _init_dbus(self):
        DBusGMainLoop(set_as_default=True)
        self.bus = dbus.SessionBus()
        self.portal = self.bus.get_object(
            "org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop"
        )
        self.screencast = dbus.Interface(
            self.portal, "org.freedesktop.portal.ScreenCast"
        )

    def request_screencast(self, timeout_sec=60):
        """
        Synchronously requests screen cast (runs GLib loop in thread or wait).
        Returns (fd, node_id).
        """
        self._init_dbus()

        token = f"pc_{random.randint(100000, 999999)}"
        session_token = f"s_{token}"
        result_container = {"fd": None, "node_id": None, "error": None}
        loop = GLib.MainLoop()

        def on_start_response(response, results):
            logger.info("Portal Start response code: %s", response)
            if response == 0:
                streams = results.get("streams", [])
                if not streams:
                    result_container["error"] = "No streams returned by portal"
                    loop.quit()
                    return
                # streams is an array of (node_id, dict)
                node_id = int(streams[0][0])
                try:
                    pw_fd = self.screencast.OpenPipeWireRemote(self.session_path, {})
                    fd = pw_fd.take()
                    result_container["fd"] = fd
                    result_container["node_id"] = node_id
                    logger.info("PipeWire stream ready: node_id=%d, fd=%d", node_id, fd)
                except Exception as e:
                    result_container["error"] = f"Failed to open PipeWire remote: {e}"
            else:
                result_container["error"] = "User cancelled or denied screen cast."
            loop.quit()

        def on_select_response(response, results):
            logger.info("Portal SelectSources response code: %s", response)
            if response == 0:
                t3 = f"t_{random.randint(100000, 999999)}"
                req3 = self.screencast.Start(self.session_path, "", {"handle_token": t3})
                self.bus.add_signal_receiver(
                    on_start_response,
                    "Response",
                    "org.freedesktop.portal.Request",
                    path=req3,
                )
            else:
                result_container["error"] = "SelectSources failed or was cancelled."
                loop.quit()

        def on_create_session(response, results):
            logger.info("Portal CreateSession response code: %s", response)
            if response == 0:
                self.session_path = results.get("session_handle")
                t2 = f"t_{random.randint(100000, 999999)}"
                opts = {
                    "handle_token": t2,
                    "types": dbus.UInt32(1),  # Monitor
                    "multiple": False,
                    "cursor_mode": dbus.UInt32(self.cursor_mode),
                }
                req2 = self.screencast.SelectSources(self.session_path, opts)
                self.bus.add_signal_receiver(
                    on_select_response,
                    "Response",
                    "org.freedesktop.portal.Request",
                    path=req2,
                )
            else:
                result_container["error"] = "CreateSession failed."
                loop.quit()

        req1 = self.screencast.CreateSession(
            {"session_handle_token": session_token, "handle_token": token}
        )
        self.bus.add_signal_receiver(
            on_create_session,
            "Response",
            "org.freedesktop.portal.Request",
            path=req1,
        )

        GLib.timeout_add_seconds(
            timeout_sec,
            lambda: (
                result_container.update({"error": "Portal prompt timed out"}),
                loop.quit(),
            ),
        )

        loop.run()

        if result_container["error"]:
            raise RuntimeError(result_container["error"])

        return result_container["fd"], result_container["node_id"]

    def close(self):
        if self.session_path and self.bus:
            try:
                session_obj = self.bus.get_object(
                    "org.freedesktop.portal.Desktop", self.session_path
                )
                session_iface = dbus.Interface(
                    session_obj, "org.freedesktop.portal.Session"
                )
                session_iface.Close()
                logger.info("Portal session closed.")
            except Exception as e:
                logger.debug("Error closing portal session: %s", e)
            self.session_path = None
