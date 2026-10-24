package vdisplay

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Config holds configuration for the hardware-accelerated DRM virtual display.
type Config struct {
	Connector string  // Optional DRM connector name (e.g., "HDMI-A-1"); auto-detected if empty
	Width     int     // Target width in pixels (default: 1600)
	Height    int     // Target height in pixels (default: 1200)
	Scale     float64 // HiDPI scale factor (default: 1.25)
	MirrorOn  string  // Optional physical output to mirror this virtual display onto (e.g., "eDP-1")
}

// VirtualDisplay manages the lifecycle of a DRM/KMS forced connector with a custom EDID.
type VirtualDisplay struct {
	CardIndex string
	Connector string
	Width     int
	Height    int
	Scale     float64
	MirrorOn  string
	closed    bool
}

// BuildEDID constructs a valid 128-byte EDID 1.4 block advertising the requested
// native tablet resolution (and its rotated counterpart) at 60Hz.
func BuildEDID(width, height int) []byte {
	if width <= 0 {
		width = 1600
	}
	if height <= 0 {
		height = 1200
	}

	edid := make([]byte, 128)

	// 1. Fixed header
	copy(edid[0:8], []byte{0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00})

	// 2. Manufacturer ID: "HSP" (HannStar / Hannspree) -> H=8, S=19, P=16
	mfg := uint16((8 << 10) | (19 << 5) | 16)
	edid[8] = byte(mfg >> 8)
	edid[9] = byte(mfg & 0xff)

	// Product code: 0x1701 (little-endian)
	edid[10] = 0x01
	edid[11] = 0x17

	// Serial number: 1
	edid[12] = 0x01

	// Week 1, Year 2024 (2024 - 1990 = 34)
	edid[16] = 1
	edid[17] = 34

	// EDID version 1.4
	edid[18] = 1
	edid[19] = 4

	// Digital input, 8 bits per color, DisplayPort/HDMI compliant
	edid[20] = 0xa5

	// Physical size in cm/mm proportional to resolution (~200mm on the major axis)
	wMM := 200
	hMM := (200 * height) / width
	if hMM <= 0 {
		hMM = 150
	}
	edid[21] = byte(wMM / 10)
	edid[22] = byte(hMM / 10)

	// Gamma 2.2 ((2.2 * 100) - 100 = 120)
	edid[23] = 120

	// Features: sRGB, preferred timing mode in DTD 1, continuous frequency
	edid[24] = 0x06

	// Standard sRGB chromaticity coordinates
	copy(edid[25:35], []byte{0xee, 0x91, 0xa3, 0x54, 0x4c, 0x99, 0x26, 0x0f, 0x50, 0x54})

	// Established timings (1024x768@60, 800x600@60, 640x480@60)
	copy(edid[35:38], []byte{0x21, 0x08, 0x00})

	// Standard timings: 1600x1200@60, 1440x1080@60, 1280x960@60
	copy(edid[38:44], []byte{0xa9, 0x40, 0x95, 0x40, 0x81, 0x40})
	for i := 44; i < 54; i++ {
		edid[i] = 0x01
	}

	// Detailed Timing Descriptor 1 (Preferred): width x height @ 60Hz
	dtd1 := makeDTD(width, height, 60, wMM, hMM)
	copy(edid[54:72], dtd1)

	// Detailed Timing Descriptor 2 (Rotated): height x width @ 60Hz
	dtd2 := makeDTD(height, width, 60, hMM, wMM)
	copy(edid[72:90], dtd2)

	// Descriptor 3: Monitor Range Limits (48-120 Hz vertical, 30-250 kHz horizontal, max 600 MHz clock)
	copy(edid[90:108], []byte{
		0x00, 0x00, 0x00, 0xfd, 0x00,
		48, 120, 30, 250, 60,
		0x00, 0x0a, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20,
	})

	// Descriptor 4: Monitor Name ("HannsNote2")
	copy(edid[108:126], []byte{
		0x00, 0x00, 0x00, 0xfc, 0x00,
		'H', 'a', 'n', 'n', 's', 'N', 'o', 't', 'e', '2', '\n', ' ', ' ',
	})

	// Checksum
	var sum uint8
	for i := 0; i < 127; i++ {
		sum += edid[i]
	}
	edid[127] = -sum

	return edid
}

func makeDTD(hActive, vActive, refreshHz, wMM, hMM int) []byte {
	hBlank := 400
	hSyncOffset := 48
	hSyncWidth := 112
	if hActive == 1600 && vActive == 1200 {
		// Exact VESA UXGA 1600x1200 @ 60Hz timing
		hBlank = 560
		hSyncOffset = 64
		hSyncWidth = 192
	}
	vBlank := 50
	vSyncOffset := 1
	vSyncWidth := 3

	totalH := hActive + hBlank
	totalV := vActive + vBlank
	pclk10kHz := (totalH * totalV * refreshHz) / 10000

	b := make([]byte, 18)
	b[0] = byte(pclk10kHz & 0xff)
	b[1] = byte((pclk10kHz >> 8) & 0xff)
	b[2] = byte(hActive & 0xff)
	b[3] = byte(hBlank & 0xff)
	b[4] = byte(((hActive >> 8) & 0x0f) << 4 | ((hBlank >> 8) & 0x0f))
	b[5] = byte(vActive & 0xff)
	b[6] = byte(vBlank & 0xff)
	b[7] = byte(((vActive >> 8) & 0x0f) << 4 | ((vBlank >> 8) & 0x0f))
	b[8] = byte(hSyncOffset & 0xff)
	b[9] = byte(hSyncWidth & 0xff)
	b[10] = byte(((vSyncOffset & 0x0f) << 4) | (vSyncWidth & 0x0f))
	b[11] = byte((((hSyncOffset >> 8) & 0x03) << 6) | (((hSyncWidth >> 8) & 0x03) << 4) | (((vSyncOffset >> 4) & 0x03) << 2) | ((vSyncWidth >> 4) & 0x03))
	b[12] = byte(wMM & 0xff)
	b[13] = byte(hMM & 0xff)
	b[14] = byte((((wMM >> 8) & 0x0f) << 4) | ((hMM >> 8) & 0x0f))
	b[15] = 0
	b[16] = 0
	b[17] = 0x1e // Digital separate sync, +H +V
	return b
}

// findUnusedConnector locates a disconnected DRM connector on the primary GPU card.
func findUnusedConnector(preferred string) (cardIdx string, connector string, err error) {
	matches, err := filepath.Glob("/sys/class/drm/card*-*")
	if err != nil || len(matches) == 0 {
		return "", "", fmt.Errorf("no DRM connectors found in /sys/class/drm")
	}

	re := regexp.MustCompile(`^card([0-9]+)-(.+)$`)

	// Prefer HDMI-A-* first, then DP-*
	var candidates []struct{ card, conn string }
	for _, m := range matches {
		base := filepath.Base(m)
		sub := re.FindStringSubmatch(base)
		if len(sub) != 3 {
			continue
		}
		cIdx, cName := sub[1], sub[2]
		if strings.HasPrefix(cName, "eDP-") || strings.HasPrefix(cName, "Writeback-") {
			continue
		}
		statusBytes, rErr := os.ReadFile(filepath.Join(m, "status"))
		if rErr != nil {
			continue
		}
		status := strings.TrimSpace(string(statusBytes))

		if preferred != "" && cName == preferred {
			return cIdx, cName, nil
		}
		if status == "disconnected" {
			candidates = append(candidates, struct{ card, conn string }{cIdx, cName})
		}
	}

	if preferred != "" {
		return "", "", fmt.Errorf("requested connector %q not found", preferred)
	}
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("no disconnected DRM connectors available for virtual display")
	}

	// Prefer HDMI-A-* if available
	for _, c := range candidates {
		if strings.HasPrefix(c.conn, "HDMI-") {
			return c.card, c.conn, nil
		}
	}
	return candidates[0].card, candidates[0].conn, nil
}

// Enable provisions the virtual display via kernel DRM debugfs/sysfs and configures mode/scale via cosmic-randr.
func Enable(cfg Config) (*VirtualDisplay, error) {
	if cfg.Width <= 0 {
		cfg.Width = 1600
	}
	if cfg.Height <= 0 {
		cfg.Height = 1200
	}
	if cfg.Scale <= 0 {
		cfg.Scale = 1.25
	}

	cardIdx, connName, err := findUnusedConnector(cfg.Connector)
	if err != nil {
		return nil, err
	}

	edid := BuildEDID(cfg.Width, cfg.Height)
	tmpEDID := filepath.Join(os.TempDir(), fmt.Sprintf("papercast-%s.edid", connName))
	if err := os.WriteFile(tmpEDID, edid, 0644); err != nil {
		return nil, fmt.Errorf("failed to write temporary EDID file: %w", err)
	}
	defer os.Remove(tmpEDID)

	debugEDIDPath := fmt.Sprintf("/sys/kernel/debug/dri/%s/%s/edid_override", cardIdx, connName)
	sysStatusPath := fmt.Sprintf("/sys/class/drm/card%s-%s/status", cardIdx, connName)

	log.Printf("[vdisplay] Creating %dx%d (scale %.0f%%) virtual display on %s (card%s)...",
		cfg.Width, cfg.Height, cfg.Scale*100, connName, cardIdx)

	// Inject custom EDID and force connector status to 'on'
	script := fmt.Sprintf("cat %q > %q && echo on > %q", tmpEDID, debugEDIDPath, sysStatusPath)
	cmd := exec.Command("sudo", "-n", "sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to activate DRM connector %s: %v (%s)", connName, err, strings.TrimSpace(string(out)))
	}

	vd := &VirtualDisplay{
		CardIndex: cardIdx,
		Connector: connName,
		Width:     cfg.Width,
		Height:    cfg.Height,
		Scale:     cfg.Scale,
		MirrorOn:  cfg.MirrorOn,
	}

	// Wait for COSMIC compositor (cosmic-randr) to detect the new output head
	deadline := time.Now().Add(3 * time.Second)
	detected := false
	for time.Now().Before(deadline) {
		out, _ := exec.Command("cosmic-randr", "list").CombinedOutput()
		if strings.Contains(string(out), connName) {
			detected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !detected {
		_ = vd.Close()
		return nil, fmt.Errorf("compositor did not detect virtual output %s within 3s", connName)
	}

	// Configure resolution and pixel scale via cosmic-randr
	scaleStr := fmt.Sprintf("%.2f", cfg.Scale)
	wStr := fmt.Sprintf("%d", cfg.Width)
	hStr := fmt.Sprintf("%d", cfg.Height)

	modeCmd := exec.Command("cosmic-randr", "mode", "--scale", scaleStr, connName, wStr, hStr)
	if out, err := modeCmd.CombinedOutput(); err != nil {
		log.Printf("[vdisplay] Warning: cosmic-randr mode returned: %v (%s)", err, strings.TrimSpace(string(out)))
	} else {
		log.Printf("[vdisplay] Configured %s to %dx%d @ %.0f%% scale", connName, cfg.Width, cfg.Height, cfg.Scale*100)
	}

	if cfg.MirrorOn != "" {
		mirrorCmd := exec.Command("cosmic-randr", "mirror", cfg.MirrorOn, connName)
		if out, err := mirrorCmd.CombinedOutput(); err != nil {
			log.Printf("[vdisplay] Warning: cosmic-randr mirror %s -> %s failed: %v (%s)", cfg.MirrorOn, connName, err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("[vdisplay] Mirroring virtual display %s onto %s", connName, cfg.MirrorOn)
		}
	}

	// Give compositor a brief moment to settle the output configuration
	time.Sleep(250 * time.Millisecond)
	return vd, nil
}

// Close disconnects the virtual DRM connector and restores the desktop display layout.
func (v *VirtualDisplay) Close() error {
	if v == nil || v.closed {
		return nil
	}
	v.closed = true

	if v.MirrorOn != "" {
		_ = exec.Command("cosmic-randr", "enable", v.MirrorOn).Run()
	}

	sysStatusPath := fmt.Sprintf("/sys/class/drm/card%s-%s/status", v.CardIndex, v.Connector)
	debugForcePath := fmt.Sprintf("/sys/kernel/debug/dri/%s/%s/force", v.CardIndex, v.Connector)

	script := fmt.Sprintf("echo off > %q; echo detect > %q; echo unspecified > %q 2>/dev/null || true",
		sysStatusPath, sysStatusPath, debugForcePath)
	_ = exec.Command("sudo", "-n", "sh", "-c", script).Run()

	log.Printf("[vdisplay] Virtual display %s disconnected cleanly", v.Connector)
	return nil
}
