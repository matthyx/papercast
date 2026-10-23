# 📄 Papercast

Ultra-low-latency desktop streamer for Wayland / COSMIC to paper-like, e-ink, and RLCD tablets (such as the Hannspree HannsNote2) using the browser's native **WebCodecs API** (`VideoDecoder`).

Written in **Go** with a high-performance native `libpipewire-0.3` + hardware VA-API capture engine, embedded web assets (`//go:embed`), and zero external process dependencies.

---

## ⚡ Why Papercast?

Existing desktop streamers (like Weylus or Deskreen) suffer from severe input lag and high CPU usage on low-power ARM tablets (like Rockchip RK3566 / Cortex-A55 devices):

* **Direct Hardware WebCodecs (Sub-15ms Latency)**: Traditional tools stream fragmented MP4 files into HTML5 `<video>` tags via `MediaSource`, which enforces a 3–5 frame jitter buffer (~100–150ms of artificial latency). Papercast feeds raw H.264 Annex-B Access Units directly into Chrome's **WebCodecs hardware decoder** (`VideoDecoder`), rendering straight to `<canvas>` with zero browser buffer delay.
* **Native PipeWire & VA-API Engine**: Captures directly from PipeWire using `libpipewire-0.3` and encodes with Intel VA-API (`h264_vaapi`) in hardware (~1ms per frame), bypassing GStreamer design flaws on Wayland Direct Scanout. Automatic fallback to software `libx264 ultrafast` if hardware encoding is unavailable.
* **Headless ScreenCast via Persistent Restore Tokens**: Authorize screen sharing once via the desktop portal dialog. Papercast permanently saves a cryptographically secure restore token (`~/.config/papercast/restore_token`). Subsequent launches start **headlessly with zero screen-picker prompts**.
* **Self-Healing Supervisor Loop**: Pop!_OS COSMIC and other Wayland compositors often reset screencast nodes during fullscreen toggling (e.g. YouTube fullscreen) or mode switches. Papercast's supervisor detects node detaches via PipeWire registry events and revives the session headlessly in **<100ms** without freezing the tablet.
* **Synchronous Atomic WebCodecs Decoder**: Android's `MediaCodec` pipeline crashes when fed delta frames while an async decoder configuration is in flight. Papercast's client uses synchronous decoder resets and keyframe-gated decoding, ensuring glitch-free playback through dynamic resolution switches.
* **Immediate PipeWire Buffer Recycling**: Returns buffers to the compositor in <1ms immediately after scaling, preventing compositor frame drops and upstream portal buffer starvation.
* **Single Port & ADB Integration**: Automatically provisions `adb reverse tcp:1701 tcp:1701`. Both HTTP assets and the real-time WebSocket operate over port `1701`.
* **High-Contrast Outdoor Modes**: Instant one-tap toggles for high-contrast and grayscale rendering tailored for reading in direct sunlight on reflective LCDs (RLCD) and e-ink displays.
* **Screen WakeLock**: Prevents the tablet from sleeping while reading or working outdoors.

---

## 🚀 Quick Start

### 1. Requirements (Host Laptop)
* **Linux with Wayland** (Pop!_OS 24.04 / COSMIC, GNOME, KDE, or Sway)
* **Build Libraries & ADB** (Ubuntu / Pop!_OS / Debian):
  ```bash
  sudo apt install -y libpipewire-0.3-dev libspa-0.2-dev libva-dev libdrm-dev \
                      libavcodec-dev libavutil-dev libswscale-dev adb
  ```

### 2. Build & Install
```bash
git clone https://github.com/matthyx/papercast.git
cd papercast
make install
```
*(Installs the single binary to `~/.local/bin/papercast`)*

### 3. Connect Tablet via USB
Connect your HannsNote2 (or any Android tablet) via USB with USB debugging enabled:
```bash
adb devices
```

### 4. Run Papercast
```bash
papercast
```
* **First run**: Click **Share** on the desktop screen-share prompt.
* **Subsequent runs**: Launches completely **headlessly** without prompting!
* Pass `-open` to automatically launch Chrome on the connected tablet.

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
  -host string
        Host address to bind HTTP/WebSocket server (default: "0.0.0.0")
  -fps int
        Target capture framerate (default: 30)
  -qp int
        VA-API Quantization Parameter: lower = higher quality, higher = lower bitrate (default: 26)
  -bitrate int
        Encoding bitrate in kbps for software fallback (default: 3000)
  -cursor uint
        Cursor mode: 1 = Hidden, 2 = Embedded (default: 2)
  -open
        Automatically launch Chrome on connected Android tablet via ADB
  -test
        Run in autonomous test mode with synthetic SMPTE pattern (no desktop screen authorization needed)
  -gst
        Use legacy GStreamer capture pipeline instead of native libpipewire
  -version
        Print Papercast version
```

---

## 🛠️ Architecture

```
[ Laptop: COSMIC Wayland Desktop ]
        │
   (xdg-desktop-portal ScreenCast / Headless Restore Token)
        │
   (Native libpipewire-0.3 Thread Loop)
        │
   (libswscale Fast NV12 Conversion & Immediate Buffer Return)
        │
[ Intel VA-API Hardware H.264 Encoder (libavcodec) ]
        │
   (Raw H.264 Annex-B NAL Units via Cgo)
        │
[ Papercast Go Server (Port 1701) ]
   ├── Embedded Web App (//go:embed)
   ├── Self-Healing Supervisor & Frame Watchdog
   └── GOP-Aware Non-Blocking WebSocket Broadcaster
        │
   (USB Link via ADB Reverse)
        │
[ Tablet: HannsNote2 Chrome Browser ]
   ├── Synchronous WebCodecs VideoDecoder (avc1.42E028)
   └── Zero-Latency <canvas> Display + Hardware Contrast Filters
```

---

## 📜 License
MIT License.
