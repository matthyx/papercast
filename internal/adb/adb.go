package adb

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// SetupReverseForwarding ensures adb reverse forwarding is active for the target port.
func SetupReverseForwarding(port int) error {
	cmd := exec.Command("adb", "devices")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[adb] adb not found or error checking devices: %v", err)
		return nil
	}

	lines := strings.Split(string(out), "\n")
	hasDevice := false
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, "device") && !strings.Contains(line, "offline") {
			hasDevice = true
			log.Printf("[adb] Detected connected Android device: %s", strings.Fields(line)[0])
			break
		}
	}

	if !hasDevice {
		log.Printf("[adb] No USB ADB device detected. Tablet can still connect over Wi-Fi/LAN.")
		return nil
	}

	portStr := fmt.Sprintf("tcp:%d", port)
	revCmd := exec.Command("adb", "reverse", portStr, portStr)
	if err := revCmd.Run(); err != nil {
		return fmt.Errorf("failed to run adb reverse %s %s: %w", portStr, portStr, err)
	}

	log.Printf("[adb] Port forwarding active: adb reverse %s %s (Hannsnote2 USB link ready)", portStr, portStr)
	return nil
}

// LaunchTabletBrowser opens Chrome on the connected Android tablet pointing to the Papercast URL.
func LaunchTabletBrowser(port int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := exec.Command("adb", "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", url)
	return cmd.Run()
}
