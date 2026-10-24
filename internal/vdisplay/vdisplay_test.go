package vdisplay

import "testing"

func TestBuildEDID(t *testing.T) {
	edid := BuildEDID(1600, 1200)
	if len(edid) != 128 {
		t.Fatalf("expected 128-byte EDID, got %d", len(edid))
	}

	// Verify EDID header
	expectedHeader := []byte{0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00}
	for i, b := range expectedHeader {
		if edid[i] != b {
			t.Fatalf("header mismatch at byte %d: got 0x%02x, want 0x%02x", i, edid[i], b)
		}
	}

	// Verify 8-bit checksum across all 128 bytes equals 0
	var sum uint8
	for _, b := range edid {
		sum += b
	}
	if sum != 0 {
		t.Fatalf("invalid EDID checksum: sum of 128 bytes is 0x%02x (expected 0x00)", sum)
	}
}
