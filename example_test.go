package edge264_test

import (
	"fmt"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/hnnsgstfssn/edge264"
)

// Decode an H.264 Annex B file and print frame dimensions.
func Example_decodeFile() {
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		log.Fatal(err)
	}

	dec, err := edge264.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}
	defer dec.Close()

	// Decode returns frames that are available immediately.
	frames, err := dec.Decode(data)
	if err != nil {
		log.Fatal(err)
	}

	// Flush returns frames buffered for reordering.
	flushed, err := dec.Flush()
	if err != nil {
		log.Fatal(err)
	}
	frames = append(frames, flushed...)

	fmt.Printf("decoded %d frames\n", len(frames))
	for i, f := range frames {
		fmt.Printf("  frame %d: %dx%d %v\n", i, f.Rect.Dx(), f.Rect.Dy(), f.SubsampleRatio)
	}
	// Output:
	// decoded 12 frames
	//   frame 0: 32x16 YCbCrSubsampleRatio420
	//   frame 1: 32x16 YCbCrSubsampleRatio420
	//   frame 2: 32x16 YCbCrSubsampleRatio420
	//   frame 3: 32x16 YCbCrSubsampleRatio420
	//   frame 4: 32x16 YCbCrSubsampleRatio420
	//   frame 5: 32x16 YCbCrSubsampleRatio420
	//   frame 6: 32x16 YCbCrSubsampleRatio420
	//   frame 7: 32x16 YCbCrSubsampleRatio420
	//   frame 8: 32x16 YCbCrSubsampleRatio420
	//   frame 9: 32x16 YCbCrSubsampleRatio420
	//   frame 10: 32x16 YCbCrSubsampleRatio420
	//   frame 11: 32x16 YCbCrSubsampleRatio420
}

// Decode individual NAL units for RTP or custom transport scenarios.
func Example_decodeNAL() {
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		log.Fatal(err)
	}

	dec, err := edge264.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}
	defer dec.Close()

	// Split the Annex B stream into individual NAL units.
	nals := splitNALs(data)

	var total int
	for _, nal := range nals {
		// DecodeNAL accepts a raw NAL unit without start code prefix.
		frames, err := dec.DecodeNAL(nal)
		if err != nil {
			log.Fatal(err)
		}
		total += len(frames)
	}

	// Flush remaining frames after all NALs are processed.
	flushed, err := dec.Flush()
	if err != nil {
		log.Fatal(err)
	}
	total += len(flushed)

	fmt.Printf("decoded %d frames from %d NALs\n", total, len(nals))
	// Output:
	// decoded 12 frames from 19 NALs
}

// Simulate seeking in a stream by decoding, flushing, resetting, then
// decoding again from a new position.
func Example_seekWithReset() {
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		log.Fatal(err)
	}

	dec, err := edge264.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}
	defer dec.Close()

	// First segment: decode and collect frames.
	frames1, _ := dec.Decode(data)
	flushed1, _ := dec.Flush()
	n1 := len(frames1) + len(flushed1)

	// Seek: reset the decoder (discards all internal state).
	dec.Reset()

	// Second segment: decode the same (or different) data.
	frames2, _ := dec.Decode(data)
	flushed2, _ := dec.Flush()
	n2 := len(frames2) + len(flushed2)

	fmt.Printf("segment 1: %d frames\n", n1)
	fmt.Printf("segment 2: %d frames\n", n2)
	// Output:
	// segment 1: 12 frames
	// segment 2: 12 frames
}

// Decode two streams concurrently with independent decoders.
func Example_concurrentDecoders() {
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		log.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([]int, 2)

	for i := range 2 {
		wg.Go(func() {

			dec, err := edge264.NewDecoder()
			if err != nil {
				log.Fatal(err)
			}
			defer dec.Close()

			frames, _ := dec.Decode(data)
			flushed, _ := dec.Flush()
			results[i] = len(frames) + len(flushed)
		})
	}
	wg.Wait()

	fmt.Printf("decoder 0: %d frames\n", results[0])
	fmt.Printf("decoder 1: %d frames\n", results[1])
}

// Extract the first decoded frame and write it as a JPEG.
func Example_frameToJPEG() {
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		log.Fatal(err)
	}

	dec, err := edge264.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}
	defer dec.Close()

	frames, _ := dec.Decode(data)
	flushed, _ := dec.Flush()
	frames = append(frames, flushed...)

	if len(frames) == 0 {
		log.Fatal("no frames decoded")
	}

	// Each frame is an *image.YCbCr which implements image.Image.
	// It can be passed directly to any Go image encoder.
	f, err := os.CreateTemp("", "frame-*.jpg")
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if err := jpeg.Encode(f, frames[0], &jpeg.Options{Quality: 90}); err != nil {
		log.Fatal(err)
	}

	info, _ := f.Stat()
	fmt.Printf("wrote %dx%d frame (%d bytes JPEG)\n",
		frames[0].Rect.Dx(), frames[0].Rect.Dy(), info.Size())
}

// Access raw YCbCr plane data for custom processing (e.g., pixel analysis,
// format conversion, or feeding into a video encoder).
func Example_rawPlaneAccess() {
	data, err := os.ReadFile(filepath.Join("testdata", "nal-ref-idc-0.264"))
	if err != nil {
		log.Fatal(err)
	}

	dec, err := edge264.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}
	defer dec.Close()

	frames, _ := dec.Decode(data)
	flushed, _ := dec.Flush()
	frames = append(frames, flushed...)

	f := frames[0]
	fmt.Printf("dimensions: %dx%d\n", f.Rect.Dx(), f.Rect.Dy())
	fmt.Printf("subsampling: %v\n", f.SubsampleRatio)
	fmt.Printf("Y plane:  stride=%d len=%d\n", f.YStride, len(f.Y))
	fmt.Printf("Cb plane: stride=%d len=%d\n", f.CStride, len(f.Cb))
	fmt.Printf("Cr plane: stride=%d len=%d\n", f.CStride, len(f.Cr))

	// Read the luma value at pixel (0,0).
	y := f.Y[0]
	// For 4:2:0, chroma pixels map to 2x2 luma blocks.
	cb := f.Cb[0]
	cr := f.Cr[0]
	fmt.Printf("pixel (0,0): Y=%d Cb=%d Cr=%d\n", y, cb, cr)
	// Output:
	// dimensions: 16x16
	// subsampling: YCbCrSubsampleRatio420
	// Y plane:  stride=16 len=256
	// Cb plane: stride=16 len=120
	// Cr plane: stride=16 len=120
	// pixel (0,0): Y=128 Cb=128 Cr=128
}

// Extract a thumbnail by decoding only until the first frame is available,
// then stopping early. Useful for generating previews without decoding the
// entire stream.
func Example_thumbnail() {
	data, err := os.ReadFile(filepath.Join("testdata", "finish-frame.264"))
	if err != nil {
		log.Fatal(err)
	}

	dec, err := edge264.NewDecoder()
	if err != nil {
		log.Fatal(err)
	}
	defer dec.Close()

	// Feed NALs one at a time, stopping at the first decoded frame.
	nals := splitNALs(data)
	for _, nal := range nals {
		frames, err := dec.DecodeNAL(nal)
		if err != nil {
			log.Fatal(err)
		}
		if len(frames) > 0 {
			fmt.Printf("thumbnail: %dx%d\n", frames[0].Rect.Dx(), frames[0].Rect.Dy())
			return
		}
	}

	// If no frame was emitted during decoding, flush to get buffered frames.
	flushed, _ := dec.Flush()
	if len(flushed) > 0 {
		fmt.Printf("thumbnail: %dx%d\n", flushed[0].Rect.Dx(), flushed[0].Rect.Dy())
	}
	// Output:
	// thumbnail: 32x16
}

// splitNALs splits an Annex B byte stream into individual NAL units,
// stripping start code prefixes (00 00 01 or 00 00 00 01).
func splitNALs(data []byte) [][]byte {
	var nals [][]byte
	i := 0
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		i = 4
	} else if len(data) >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1 {
		i = 3
	}
	for i < len(data) {
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
