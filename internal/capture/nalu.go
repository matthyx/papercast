package capture

// NAL unit types in H.264
const (
	NALTypeSliceNonIDR = 1 // P / B frame slice
	NALTypeDataPartA   = 2
	NALTypeDataPartB   = 3
	NALTypeDataPartC   = 4
	NALTypeSliceIDR    = 5 // Keyframe slice
	NALTypeSEI         = 6 // Supplemental Enhancement Information
	NALTypeSPS         = 7 // Sequence Parameter Set
	NALTypePPS         = 8 // Picture Parameter Set
	NALTypeAUD         = 9 // Access Unit Delimiter
)

// IsKeyframe checks whether an Annex B buffer contains SPS (7) or IDR (5) NAL units.
func IsKeyframe(data []byte) bool {
	for i := 0; i < len(data)-4; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			offset := -1
			if data[i+2] == 1 {
				offset = i + 3
			} else if data[i+2] == 0 && data[i+3] == 1 {
				offset = i + 4
			}
			if offset != -1 && offset < len(data) {
				nalType := data[offset] & 0x1F
				if nalType == NALTypeSliceIDR || nalType == NALTypeSPS {
					return true
				}
			}
		}
	}
	return false
}

// FindNextStartCode finds the start code index and its length (3 or 4) starting from pos.
func FindNextStartCode(buf []byte, pos int) (index int, length int) {
	n := len(buf)
	for i := pos; i <= n-3; i++ {
		if buf[i] == 0 && buf[i+1] == 0 {
			if i+3 < n && buf[i+2] == 0 && buf[i+3] == 1 {
				return i, 4
			}
			if buf[i+2] == 1 {
				return i, 3
			}
		}
	}
	return -1, 0
}

// AUSplitter chunks streaming byte sequences into discrete H.264 Access Units (frames).
type AUSplitter struct {
	buf []byte
}

func NewAUSplitter() *AUSplitter {
	return &AUSplitter{
		buf: make([]byte, 0, 256*1024),
	}
}

// Push appends incoming stream bytes and extracts all complete frames.
func (s *AUSplitter) Push(data []byte) [][]byte {
	s.buf = append(s.buf, data...)
	var frames [][]byte

	for {
		frame := s.extractOne()
		if frame == nil {
			break
		}
		frames = append(frames, frame)
	}

	return frames
}

func (s *AUSplitter) extractOne() []byte {
	if len(s.buf) < 5 {
		return nil
	}

	firstIdx, firstLen := FindNextStartCode(s.buf, 0)
	if firstIdx == -1 {
		// No start code found in buffer, discard if too large
		if len(s.buf) > 512*1024 {
			s.buf = s.buf[:0]
		}
		return nil
	}

	// Trim garbage preceding the first start code
	if firstIdx > 0 {
		s.buf = s.buf[firstIdx:]
		firstIdx = 0
	}

	if firstLen >= len(s.buf) {
		return nil
	}

	firstNALType := s.buf[firstLen] & 0x1F
	hasSlice := (firstNALType == NALTypeSliceIDR || firstNALType == NALTypeSliceNonIDR)
	searchPos := firstLen

	for {
		idx, length := FindNextStartCode(s.buf, searchPos)
		if idx == -1 {
			// Incomplete AU, wait for more data
			return nil
		}

		nalOffset := idx + length
		if nalOffset >= len(s.buf) {
			return nil
		}

		nalType := s.buf[nalOffset] & 0x1F

		// Check if this NAL indicates start of a new Access Unit
		isNewAU := false
		if nalType == NALTypeAUD {
			// An Access Unit Delimiter marks the boundary of a new Access Unit
			isNewAU = true
		} else if hasSlice {
			// Once we have a slice in the current AU, any subsequent parameter sets,
			// SEI, or new slices indicate the beginning of the next Access Unit.
			switch nalType {
			case NALTypeSPS, NALTypePPS, NALTypeSEI, NALTypeSliceIDR, NALTypeSliceNonIDR:
				isNewAU = true
			}
		} else {
			// We haven't seen a slice yet for the current picture.
			// Non-VCL units (SPS, PPS, SEI) belong to the header of the current picture.
			if nalType == NALTypeSliceIDR || nalType == NALTypeSliceNonIDR {
				hasSlice = true
			}
		}

		if isNewAU {
			frame := make([]byte, idx)
			copy(frame, s.buf[:idx])
			s.buf = s.buf[idx:]
			return frame
		}

		searchPos = idx + length
	}
}

// Reset clears the internal buffer.
func (s *AUSplitter) Reset() {
	s.buf = s.buf[:0]
}
