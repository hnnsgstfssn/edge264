package edge264

import (
	"image"
	"os"
	"path/filepath"
	"testing"
)

func TestDecode(t *testing.T) {
	tests := []struct {
		name       string
		file       string
		wantFrames int
		wantWidth  int
		wantHeight int
		wantSub    image.YCbCrSubsampleRatio
	}{
		{
			name:       "multi-frame stream",
			file:       "finish-frame.264",
			wantFrames: 12,
			wantWidth:  32,
			wantHeight: 16,
			wantSub:    image.YCbCrSubsampleRatio420,
		},
		{
			name:       "single IDR with nal_ref_idc=0",
			file:       "nal-ref-idc-0.264",
			wantFrames: 1,
			wantWidth:  16,
			wantHeight: 16,
			wantSub:    image.YCbCrSubsampleRatio420,
		},
		{
			name:       "non-reference decoding POC",
			file:       "non-ref-dec-poc.264",
			wantFrames: 2,
			wantWidth:  16,
			wantHeight: 16,
			wantSub:    image.YCbCrSubsampleRatio420,
		},
		{
			name:       "POC out of order",
			file:       "poc-out-of-order.264",
			wantFrames: 3,
			wantWidth:  16,
			wantHeight: 16,
			wantSub:    image.YCbCrSubsampleRatio420,
		},
		{
			name:       "positive frame_num IDR",
			file:       "pos-frame-num-idr.264",
			wantFrames: 1,
			wantWidth:  16,
			wantHeight: 16,
			wantSub:    image.YCbCrSubsampleRatio420,
		},
		{
			name:       "supplemental NALs",
			file:       "supp-nals.264",
			wantFrames: 2,
			wantWidth:  16,
			wantHeight: 16,
			wantSub:    image.YCbCrSubsampleRatio420,
		},
		{
			name:       "zero-size crop",
			file:       "zero-cropping.264",
			wantFrames: 2,
			wantWidth:  2,
			wantHeight: 2,
			wantSub:    image.YCbCrSubsampleRatio420,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}

			dec, err := NewDecoder()
			if err != nil {
				t.Fatal(err)
			}
			defer dec.Close()

			frames, err := dec.Decode(data)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			flushed, err := dec.Flush()
			if err != nil {
				t.Fatalf("Flush: %v", err)
			}
			frames = append(frames, flushed...)

			if got := len(frames); got != tt.wantFrames {
				t.Fatalf("got %d frames, want %d", got, tt.wantFrames)
			}

			for i, f := range frames {
				if f.Rect.Dx() != tt.wantWidth || f.Rect.Dy() != tt.wantHeight {
					t.Errorf("frame %d: got %dx%d, want %dx%d",
						i, f.Rect.Dx(), f.Rect.Dy(), tt.wantWidth, tt.wantHeight)
				}
				if f.SubsampleRatio != tt.wantSub {
					t.Errorf("frame %d: got %v, want %v", i, f.SubsampleRatio, tt.wantSub)
				}
			}
		})
	}
}

func TestDecodeNoFrames(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"SPS only", "page-boundaries.264"},
		{"missing parameter sets", "missing-ps.264"},
		{"unsupported NAL types", "unsupp-nals.264"},
		{"max-logs (no slices)", "max-logs.264"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}

			dec, err := NewDecoder()
			if err != nil {
				t.Fatal(err)
			}
			defer dec.Close()

			frames, _ := dec.Decode(data)
			flushed, _ := dec.Flush()
			frames = append(frames, flushed...)

			if len(frames) != 0 {
				t.Fatalf("got %d frames, want 0", len(frames))
			}
		})
	}
}

func TestDecodeNAL(t *testing.T) {
	// Manually split finish-frame.264 into NALs and decode one at a time.
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		t.Fatal(err)
	}

	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	nals := splitNALs(data)
	if len(nals) == 0 {
		t.Fatal("no NALs found in test data")
	}

	var total int
	for _, nal := range nals {
		frames, err := dec.DecodeNAL(nal)
		if err != nil {
			t.Fatalf("DecodeNAL: %v", err)
		}
		total += len(frames)
	}

	flushed, err := dec.Flush()
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	total += len(flushed)

	if total != 12 {
		t.Fatalf("got %d frames, want 12", total)
	}
}

func TestFlushAndContinue(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		t.Fatal(err)
	}

	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	// First decode pass.
	frames1, err := dec.Decode(data)
	if err != nil {
		t.Fatalf("Decode 1: %v", err)
	}
	flushed1, err := dec.Flush()
	if err != nil {
		t.Fatalf("Flush 1: %v", err)
	}
	n1 := len(frames1) + len(flushed1)

	// Reset and decode the same stream again.
	dec.Reset()

	frames2, err := dec.Decode(data)
	if err != nil {
		t.Fatalf("Decode 2: %v", err)
	}
	flushed2, err := dec.Flush()
	if err != nil {
		t.Fatalf("Flush 2: %v", err)
	}
	n2 := len(frames2) + len(flushed2)

	if n1 != n2 {
		t.Fatalf("first pass: %d frames, second pass: %d frames", n1, n2)
	}
}

func TestReset(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	// Reset on a fresh decoder should not panic.
	dec.Reset()

	// Decode after reset should work.
	data, err := os.ReadFile(filepath.Join("testdata", "nal-ref-idc-0.264"))
	if err != nil {
		t.Fatal(err)
	}
	frames, err := dec.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	flushed, err := dec.Flush()
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := len(frames) + len(flushed); got != 1 {
		t.Fatalf("got %d frames, want 1", got)
	}
}

func TestCloseIdempotent(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}

	if err := dec.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := dec.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestClosedDecoderErrors(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	dec.Close()

	if _, err := dec.Decode([]byte{0, 0, 0, 1, 0x67}); err == nil {
		t.Fatal("Decode on closed decoder should error")
	}
	if _, err := dec.DecodeNAL([]byte{0x67}); err == nil {
		t.Fatal("DecodeNAL on closed decoder should error")
	}
	if _, err := dec.Flush(); err == nil {
		t.Fatal("Flush on closed decoder should error")
	}
}

func TestMultipleDecoders(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		t.Fatal(err)
	}

	// Two decoders running concurrently with isolated state.
	dec1, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec1.Close()

	dec2, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec2.Close()

	frames1, _ := dec1.Decode(data)
	flushed1, _ := dec1.Flush()
	n1 := len(frames1) + len(flushed1)

	frames2, _ := dec2.Decode(data)
	flushed2, _ := dec2.Flush()
	n2 := len(frames2) + len(flushed2)

	if n1 != 12 || n2 != 12 {
		t.Fatalf("dec1: %d frames, dec2: %d frames, want 12 each", n1, n2)
	}
}

func TestDecodeSmallInput(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	// Input too small (< 4 bytes) should return nil, nil.
	frames, err := dec.Decode([]byte{0, 0, 1})
	if err != nil {
		t.Fatalf("Decode small: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("got %d frames, want 0", len(frames))
	}

	// Empty NAL should return nil, nil.
	frames, err = dec.DecodeNAL(nil)
	if err != nil {
		t.Fatalf("DecodeNAL nil: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("got %d frames, want 0", len(frames))
	}
}

// splitNALs splits an Annex B stream into individual NAL units (without start codes).
func splitNALs(data []byte) [][]byte {
	var nals [][]byte
	i := 0
	// Skip initial start code.
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		i = 4
	} else if len(data) >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1 {
		i = 3
	}

	for i < len(data) {
		// Find next start code.
		end := len(data)
		for j := i; j < len(data)-2; j++ {
			if data[j] == 0 && data[j+1] == 0 && (data[j+2] == 1 || (data[j+2] == 0 && j+3 < len(data) && data[j+3] == 1)) {
				end = j
				break
			}
		}
		if end > i {
			nals = append(nals, data[i:end])
		}
		// Skip past start code.
		if end >= len(data) {
			break
		}
		if data[end+2] == 0 {
			i = end + 4
		} else {
			i = end + 3
		}
	}
	return nals
}
