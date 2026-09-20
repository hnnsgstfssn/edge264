package edge264

import (
	"encoding/binary"
	"fmt"
	"image"

	"github.com/hnnsgstfssn/edge264/internal/wasm"
)

const frameSize = 64

// Edge264Frame wasm32 struct layout offsets.
const (
	offSamples0 = 0
	offSamples1 = 4
	offSamples2 = 8
	offWidthY   = 30
	offWidthC   = 32
	offHeightY  = 34
	offHeightC  = 36
	offStrideY  = 38
	offStrideC  = 40
)

// WASM errno values (musl libc).
const (
	enomsg  int32 = 42
	enobufs int32 = 105
	enodata int32 = 61
)

// readFrame reads an Edge264Frame from WASM memory at framePtr and copies
// the Y/Cb/Cr planes to Go heap as an *image.YCbCr.
func readFrame(inst *wasm.Instance, framePtr uint32) (*image.YCbCr, error) {
	raw, ok := inst.Read(framePtr, frameSize)
	if !ok {
		return nil, fmt.Errorf("edge264: read frame struct failed")
	}
	// Copy struct bytes so we can safely read after subsequent WASM calls.
	buf := make([]byte, frameSize)
	copy(buf, raw)

	le := binary.LittleEndian

	samplesY := le.Uint32(buf[offSamples0:])
	samplesCb := le.Uint32(buf[offSamples1:])
	samplesCr := le.Uint32(buf[offSamples2:])

	// width_Y/height_Y are already the visible (post-crop) dimensions.
	// Sample pointers already account for crop offsets.
	widthY := int(int16(le.Uint16(buf[offWidthY:])))
	widthC := int(int16(le.Uint16(buf[offWidthC:])))
	heightY := int(int16(le.Uint16(buf[offHeightY:])))
	heightC := int(int16(le.Uint16(buf[offHeightC:])))
	strideY := int(int16(le.Uint16(buf[offStrideY:])))
	strideC := int(int16(le.Uint16(buf[offStrideC:])))

	if widthY <= 0 || heightY <= 0 || strideY <= 0 || strideC <= 0 {
		return nil, fmt.Errorf("edge264: invalid frame: %dx%d stride %d/%d",
			widthY, heightY, strideY, strideC)
	}

	// Determine chroma subsampling from the luma/chroma dimension ratio.
	var subsample image.YCbCrSubsampleRatio
	switch {
	case widthC < widthY && heightC < heightY:
		subsample = image.YCbCrSubsampleRatio420
	case widthC < widthY:
		subsample = image.YCbCrSubsampleRatio422
	default:
		subsample = image.YCbCrSubsampleRatio444
	}

	// Copy Y plane to Go heap.
	// Cb and Cr are interleaved within each stride_C row: first half is Cb,
	// second half is Cr. Consecutive rows of each component are stride_C apart,
	// so CStride = strideC for Go's image.YCbCr.
	yBytes := uint32((heightY-1)*strideY + widthY)
	yRaw, ok := inst.Read(samplesY, yBytes)
	if !ok {
		return nil, fmt.Errorf("edge264: read Y plane failed at 0x%x (%d bytes)", samplesY, yBytes)
	}
	yData := make([]byte, yBytes)
	copy(yData, yRaw)

	cBytes := uint32((heightC-1)*strideC + widthC)
	cbRaw, ok := inst.Read(samplesCb, cBytes)
	if !ok {
		return nil, fmt.Errorf("edge264: read Cb plane failed at 0x%x (%d bytes)", samplesCb, cBytes)
	}
	cbData := make([]byte, cBytes)
	copy(cbData, cbRaw)

	crRaw, ok := inst.Read(samplesCr, cBytes)
	if !ok {
		return nil, fmt.Errorf("edge264: read Cr plane failed at 0x%x (%d bytes)", samplesCr, cBytes)
	}
	crData := make([]byte, cBytes)
	copy(crData, crRaw)

	return &image.YCbCr{
		Y:              yData,
		Cb:             cbData,
		Cr:             crData,
		YStride:        strideY,
		CStride:        strideC,
		SubsampleRatio: subsample,
		Rect:           image.Rect(0, 0, widthY, heightY),
	}, nil
}
