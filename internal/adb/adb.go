package adb

import (
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var wmSizeRe = regexp.MustCompile(`(?i)(Physical|Override)\s+size:\s*([0-9]+)x([0-9]+)`)

// HasConnectedDevice returns the serial of the first online USB ADB device, if any.
func HasConnectedDevice() (string, bool) {
	cmd := exec.Command("adb", "devices")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 2 && fields[1] == "device" {
			return fields[0], true
		}
	}
	return "", false
}

// SetupReverseForwarding ensures adb reverse forwarding is active for the target port.
func SetupReverseForwarding(port int) error {
	serial, ok := HasConnectedDevice()
	if !ok {
		log.Printf("[adb] No USB ADB device detected. Tablet can still connect over Wi-Fi/LAN.")
		return nil
	}
	log.Printf("[adb] Detected connected Android device: %s", serial)

	portStr := fmt.Sprintf("tcp:%d", port)
	revCmd := exec.Command("adb", "reverse", portStr, portStr)
	if err := revCmd.Run(); err != nil {
		return fmt.Errorf("failed to run adb reverse %s %s: %w", portStr, portStr, err)
	}

	log.Printf("[adb] Port forwarding active: adb reverse %s %s (Hannsnote2 USB link ready)", portStr, portStr)
	return nil
}

// ParseWMSize extracts the tablet resolution from `adb shell wm size` output
// and normalizes it to landscape (width >= height) for desktop extension.
func ParseWMSize(output string) (int, int, bool) {
	matches := wmSizeRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return 0, 0, false
	}
	// Prefer "Override size" if present (last match), otherwise "Physical size"
	chosen := matches[len(matches)-1]
	w, err1 := strconv.Atoi(chosen[2])
	h, err2 := strconv.Atoi(chosen[3])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	if w < h {
		w, h = h, w
	}
	return w, h, true
}

// DetectTabletResolution queries the connected Android tablet via `adb shell wm size`
// and returns its landscape resolution (e.g., 1600, 1200, true).
func DetectTabletResolution() (int, int, bool) {
	if _, ok := HasConnectedDevice(); !ok {
		return 0, 0, false
	}
	out, err := exec.Command("adb", "shell", "wm", "size").CombinedOutput()
	if err != nil {
		return 0, 0, false
	}
	w, h, ok := ParseWMSize(string(out))
	if ok {
		log.Printf("[adb] Auto-detected tablet resolution: %dx%d", w, h)
	}
	return w, h, ok
}

// LaunchTabletBrowser opens Chrome on the connected Android tablet pointing to the Papercast URL.
func LaunchTabletBrowser(port int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := exec.Command("adb", "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", url)
	return cmd.Run()
}
