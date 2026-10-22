# 📄 Papercast

Ultra-low-latency desktop streamer for Wayland/COSMIC to paper-like / e-ink / RLCD tablets (such as Hannspree Hannsnote2) using the modern browser **WebCodecs API** (`VideoDecoder`).

---

## ⚡ Why Papercast?

Most existing desktop streamers (like Weylus or Deskreen) suffer from severe input lag and high CPU usage on low-power ARM tablets (like Rockchip RK3566 / Cortex-A55 devices):
* **No `<video>` buffer**: Traditional tools feed fragmented MP4 files into HTML5 `<video>` tags via `MediaSource`, which forces a 3–5 frame jitter buffer (~100–150ms of artificial latency). Papercast feeds raw H.264 Annex-B packets directly into the browser's native **WebCodecs hardware decoder** (`VideoDecoder`), rendering straight to `<canvas>` in **sub-20ms**.
* **Intel VA-API Hardware Encoding**: Captures and encodes on your laptop GPU (`vaapih264enc`) with `tune=zerolatency` in ~1–2ms without eating battery or CPU.
* **Single Port**: Serves both HTTP and WebSockets over a single port (`1701`). Only one ADB reverse command is needed (`adb reverse tcp:1701 tcp:1701`).
* **COSMIC Wayland Native**: Integrates cleanly with Pop!_OS / COSMIC desktop through `xdg-desktop-portal` and PipeWire without crashing or leaking sessions.
* **High-Contrast Outdoor Modes**: Includes instant toggles for high-contrast and grayscale rendering tailored for reading in direct sunlight on reflective LCDs (RLCD) and e-ink displays.

---

## 🚀 Quick Start

### 1. Requirements (Host Laptop)
On Pop!_OS / Ubuntu:
```bash
sudo apt install -y python3-websockets python3-gi python3-dbus gstreamer1.0-pipewire gstreamer1.0-vaapi
```

### 2. Connect Your Tablet
Connect your tablet via USB with USB debugging enabled:
```bash
adb devices
```

### 3. Run Papercast
```bash
./run.sh
```

### 4. View on Tablet
Open Chrome on your tablet and navigate to:
```
http://127.0.0.1:1701
```
* Tap **Fullscreen** to enter immersive mode without browser chrome.
* Tap **Contrast** to toggle between Normal, High-Contrast, and Grayscale modes.

---

## 🛠️ Architecture

```
[ Laptop: COSMIC Desktop ]
        │
   (PipeWire)
        │
[ GStreamer: VA-API Hardware H.264 Encoder (zerolatency) ]
        │
   (Raw Annex-B NALUs)
        │
[ Python Async WebSocket Server (Port 1701) ]
        │
   (USB Cable via ADB Reverse)
        │
[ Tablet: Chrome Browser ]
        │
   (WebCodecs API: Hardware VideoDecoder)
        │
   (OffscreenCanvas / WebGL <canvas>)
```

---

## 📜 License
MIT License.
