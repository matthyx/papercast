package capture

import (
	"bytes"
	"testing"
)

func TestIsKeyframe(t *testing.T) {
	// SPS (type 7): 00 00 00 01 67
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e}
	if !IsKeyframe(sps) {
		t.Errorf("Expected sps to be keyframe")
	}

	// IDR Slice (type 5): 00 00 01 65
	idr := []byte{0x00, 0x00, 0x01, 0x65, 0x88, 0x84}
	if !IsKeyframe(idr) {
		t.Errorf("Expected idr to be keyframe")
	}

	// Non-IDR Slice (type 1): 00 00 00 01 41
	pframe := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x02}
	if IsKeyframe(pframe) {
		t.Errorf("Expected pframe to NOT be keyframe")
	}
}

func TestAUSplitter(t *testing.T) {
	splitter := NewAUSplitter()

	// Frame 1: SPS + PPS + IDR
	f1SPS := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x01, 0x02}
	f1PPS := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0x03, 0x04}
	f1IDR := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x05, 0x06, 0x07}
	frame1 := append(append(f1SPS, f1PPS...), f1IDR...)

	// Frame 2: Non-IDR slice
	frame2 := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x10, 0x11, 0x12}

	// Frame 3: Non-IDR slice
	frame3 := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x20, 0x21, 0x22}

	// Feed all together with start of frame 4
	allData := append(append(append(frame1, frame2...), frame3...), []byte{0x00, 0x00, 0x00, 0x01, 0x41}...)

	// Push in chunks to simulate network streaming
	chunk1 := allData[:15]
	chunk2 := allData[15:40]
	chunk3 := allData[40:]

	var extracted [][]byte
	for _, chunk := range [][]byte{chunk1, chunk2, chunk3} {
		frames := splitter.Push(chunk)
		for _, f := range frames {
			t.Logf("Extracted frame: len=%d isKey=%v", len(f), IsKeyframe(f))
			extracted = append(extracted, f)
		}
	}

	if len(extracted) != 3 {
		t.Fatalf("Expected 3 complete frames extracted, got %d", len(extracted))
	}

	if !bytes.Equal(extracted[0], frame1) {
		t.Errorf("Frame 1 mismatch")
	}
	if !bytes.Equal(extracted[1], frame2) {
		t.Errorf("Frame 2 mismatch")
	}
	if !bytes.Equal(extracted[2], frame3) {
		t.Errorf("Frame 3 mismatch")
	}

	if !IsKeyframe(extracted[0]) {
		t.Errorf("Expected extracted[0] to be keyframe")
	}
	if IsKeyframe(extracted[1]) {
		t.Errorf("Expected extracted[1] to not be keyframe")
	}
}

func TestAUSplitterWithAUD(t *testing.T) {
	splitter := NewAUSplitter()

	aud := []byte{0x00, 0x00, 0x00, 0x01, 0x09, 0x10}
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1e}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00}
	expectedIDR := append(append(append(append([]byte{}, aud...), sps...), pps...), idr...)

	p1AUD := []byte{0x00, 0x00, 0x00, 0x01, 0x09, 0x10}
	p1Slice := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x02}
	expectedP1 := append(append([]byte{}, p1AUD...), p1Slice...)

	nextAUD := []byte{0x00, 0x00, 0x00, 0x01, 0x09, 0x10}

	data := append(append(append([]byte{}, expectedIDR...), expectedP1...), nextAUD...)

	frames := splitter.Push(data)
	for i, f := range frames {
		t.Logf("Extracted frame %d: len=%d isKey=%v", i, len(f), IsKeyframe(f))
	}

	if len(frames) != 2 {
		t.Fatalf("Expected 2 complete frames, got %d", len(frames))
	}

	if !bytes.Equal(frames[0], expectedIDR) {
		t.Errorf("Frame 0 mismatch: got len=%d, expected len=%d", len(frames[0]), len(expectedIDR))
	}
	if !IsKeyframe(frames[0]) {
		t.Errorf("Expected Frame 0 to be keyframe")
	}
	if !bytes.Equal(frames[1], expectedP1) {
		t.Errorf("Frame 1 mismatch: got len=%d, expected len=%d", len(frames[1]), len(expectedP1))
	}
	if IsKeyframe(frames[1]) {
		t.Errorf("Expected Frame 1 to not be keyframe")
	}
}
