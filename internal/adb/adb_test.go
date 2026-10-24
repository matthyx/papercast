package adb

import "testing"

func TestParseWMSize(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantW  int
		wantH  int
		wantOK bool
	}{
		{
			name:   "Portrait physical panel normalized to landscape",
			input:  "Physical size: 1200x1600\n",
			wantW:  1600,
			wantH:  1200,
			wantOK: true,
		},
		{
			name:   "Landscape physical panel",
			input:  "Physical size: 2560x1600\n",
			wantW:  2560,
			wantH:  1600,
			wantOK: true,
		},
		{
			name:   "Override size takes precedence",
			input:  "Physical size: 2560x1600\nOverride size: 1920x1200\n",
			wantW:  1920,
			wantH:  1200,
			wantOK: true,
		},
		{
			name:   "Empty or invalid output",
			input:  "no devices/emulators found\n",
			wantW:  0,
			wantH:  0,
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, h, ok := ParseWMSize(tc.input)
			if ok != tc.wantOK || w != tc.wantW || h != tc.wantH {
				t.Fatalf("ParseWMSize(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tc.input, w, h, ok, tc.wantW, tc.wantH, tc.wantOK)
			}
		})
	}
}
