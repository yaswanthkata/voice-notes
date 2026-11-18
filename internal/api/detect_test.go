package api

import "testing"

func TestDetectAudio(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		sendAs string
		ok     bool
	}{
		{"wav", minimalWAV(), "audio/wav", true},
		// EBML header with DocType "webm"
		{"webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 0x01, 0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}, "audio/webm", true},
		{"m4a", append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'M', '4', 'A', ' '}, make([]byte, 20)...), "audio/mp4", true},
		{"mp3 frame", append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 40)...), "audio/mp3", true},
		{"png", []byte("\x89PNG\r\n\x1a\n0000"), "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, detected, ok := detectAudio(tc.data)
			if ok != tc.ok || got != tc.sendAs {
				t.Fatalf("detectAudio = (%q, ok=%v) detected=%q; want (%q, ok=%v)", got, ok, detected, tc.sendAs, tc.ok)
			}
		})
	}
}
