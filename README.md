# 📄 Papercast

Ultra-low-latency desktop streamer for Wayland / COSMIC to paper-like, e-ink, and RLCD tablets (such as the Hannspree HannsNote2) using the browser's native **WebCodecs API** (`VideoDecoder`).

Written in **Go** as a single self-contained binary with embedded assets and zero runtime dependencies.

---

## ⚡ Why Papercast?

Most existing desktop streamers (like Weylus or Deskreen) suffer from severe input lag and high CPU usage on low-power ARM tablets (like Rockchip RK3566 / Cortex-A55 devices):

* **Direct Hardware WebCodecs (Sub-15ms Latency)**: Traditional tools stream fragmented MP4 files into HTML5 `<video>` tags via `MediaSource`, which enforces a 3–5 frame jitter buffer (~100–150ms of artificial latency). Papercast feeds raw H.264 Annex-B Access Units directly into Chrome's **WebCodecs hardware decoder** (`VideoDecoder`), rendering straight to `<canvas>` with zero browser buffer delay.
* **Single Portable Go Binary**: No Python virtual environments, no `pip install`, and no GObject introspection dependency issues. Assets (`index.html`) are baked directly into the binary with `//go:embed`.
* **Pure Go D-Bus (`godbus`)**: ScreenCast portal negotiation is handled natively over the D-Bus unix domain socket without external C libraries.
* **Intel VA-API Hardware Encoding**: Encodes on your laptop GPU (`vaapih264enc`) with zero B-frames and zero reordering latency (with automatic fallback to `x264enc ultrafast`).
* **Single Port & ADB Integration**: Automatically sets up `adb reverse tcp:1701 tcp:1701`. Both HTTP and real-time WebSockets operate over port `1701`.
* **High-Contrast Outdoor Modes**: Instant toggles for high-contrast and grayscale rendering tailored for reading in direct sunlight on reflective LCDs (RLCD) and e-ink displays.
* **Screen WakeLock**: Keeps the tablet screen awake while reading or working outdoors.

---

## 🚀 Quick Start

### 1. Requirements (Host Laptop)
* Linux with Wayland (Pop!_OS / COSMIC, GNOME, KDE, or Sway)
* `gstreamer1.0-pipewire` and `gstreamer1.0-vaapi` (standard on modern Linux)
* `adb` (Android Debug Bridge for USB connection)

### 2. Build or Install
```bash
git clone https://github.com/matthyx/papercast.git
cd papercast
make build
```
*(Or install directly: `go install github.com/matthyx/papercast@latest`)*

### 3. Connect Tablet via USB
Connect your HannsNote2 (or any Android tablet) via USB with USB debugging enabled:
```bash
adb devices
```

### 4. Run Papercast
```bash
./run.sh
```
* Or run `./papercast` directly!
* When the desktop screen-share prompt appears, click **Share**.

### 5. View on Tablet
Open Chrome on your tablet and navigate to:
```
http://127.0.0.1:1701
```
* Tap **Fullscreen** to enter immersive mode without browser chrome.
* Tap **Contrast** to cycle between Normal, High-Contrast, and Grayscale modes for outdoor reading.

---

## ⚙️ Options & Flags

```
Usage of papercast:
  -port int
        Port to serve on (default: 1701)
  -fps int
        Target capture framerate (default: 30)
  -bitrate int
        Encoding bitrate in kbps (default: 3000)
  -cursor uint
        Cursor mode: 1 = Hidden, 2 = Embedded (default: 2)
  -open
        Automatically open Chrome on connected Android tablet via ADB
  -host string
        Host address to bind HTTP/WebSocket server (default: "0.0.0.0")
  -version
        Print Papercast version
```

---

## 🛠️ Architecture

```
[ Laptop: COSMIC Wayland Desktop ]
        │
   (D-Bus ScreenCast Portal / PipeWire Remote)
        │
[ Intel VA-API Hardware H.264 Encoder ]
        │
   (Raw H.264 Annex-B Access Units)
        │
[ Papercast Go Server (Port 1701) ]
   ├── Embedded Web App (//go:embed)
   └── Single-Buffer Non-Blocking WebSocket Broadcaster
        │
   (USB Link via ADB Reverse)
        │
[ Tablet: HannsNote2 Chrome Browser ]
   ├── WebCodecs Hardware VideoDecoder (avc1.42E028)
   └── Zero-Latency <canvas> Display
```

---

## 📜 License
MIT License.
