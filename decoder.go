// Package edge264 provides a pure Go H.264 decoder using WebAssembly.
package edge264

import (
	"context"
	"fmt"
	"image"
	"sync"

	"github.com/hnnsgstfssn/edge264/internal/wasm"
)

// Decoder decodes H.264 Annex B streams into YCbCr frames.
// A Decoder is safe for concurrent use; calls are serialized internally.
type Decoder struct {
	inst     *wasm.Instance
	mu       sync.Mutex
	decPtr   uint32 // Edge264Decoder* in WASM memory
	framePtr uint32 // Edge264Frame buffer (64 bytes) in WASM memory
	ptrPtr   uint32 // Edge264Decoder** buffer (4 bytes) for edge264_free
	inputPtr uint32 // reusable input buffer
	inputCap uint32 // capacity of input buffer
	closed   bool
}

// NewDecoder creates a new H.264 decoder backed by a dedicated WASM instance.
func NewDecoder() (*Decoder, error) {
	ctx := context.Background()

	inst, err := wasm.NewInstance(ctx)
	if err != nil {
		return nil, fmt.Errorf("edge264: %w", err)
	}

	// edge264_alloc(0, 0, 0, 0, 0, 0, 0) — single-threaded, no callbacks.
	results, err := inst.FnAlloc.Call(ctx, 0, 0, 0, 0, 0, 0, 0)
	if err != nil {
		_ = inst.Close(ctx) // nolint:errcheck
		return nil, fmt.Errorf("edge264: alloc: %w", err)
	}
	decPtr := uint32(results[0])
	if decPtr == 0 {
		_ = inst.Close(ctx) // nolint:errcheck
		return nil, fmt.Errorf("edge264: alloc returned NULL")
	}

	// Allocate Edge264Frame buffer (64 bytes) and ptr-to-ptr buffer (4 bytes).
	framePtr, err := inst.Malloc(ctx, frameSize)
	if err != nil {
		_ = inst.Close(ctx) // nolint:errcheck
		return nil, fmt.Errorf("edge264: %w", err)
	}
	ptrPtr, err := inst.Malloc(ctx, 4)
	if err != nil {
		_ = inst.Free(ctx, framePtr) // nolint:errcheck
		_ = inst.Close(ctx)          // nolint:errcheck
		return nil, fmt.Errorf("edge264: %w", err)
	}

	return &Decoder{
		inst:     inst,
		decPtr:   decPtr,
		framePtr: framePtr,
		ptrPtr:   ptrPtr,
	}, nil
}

// Decode decodes an H.264 Annex B byte stream and returns decoded frames.
// The input must contain start codes (00 00 01 or 00 00 00 01).
func (d *Decoder) Decode(data []byte) ([]*image.YCbCr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, fmt.Errorf("edge264: decoder closed")
	}
	if len(data) < 4 {
		return nil, nil
	}

	ctx := context.Background()

	// Copy input to WASM memory.
	bufPtr, err := d.ensureInput(ctx, uint32(len(data)))
	if err != nil {
		return nil, err
	}
	d.inst.Write(bufPtr, data)
	endPtr := bufPtr + uint32(len(data))

	// Skip the initial [0]001 start code.
	nalStart := bufPtr + 3
	if data[2] == 0 {
		nalStart = bufPtr + 4
	}

	var frames []*image.YCbCr
	for nalStart < endPtr {
		// Find next start code.
		results, err := d.inst.FnFindStartCode.Call(ctx,
			uint64(nalStart), uint64(endPtr), 0)
		if err != nil {
			return frames, fmt.Errorf("edge264: find_start_code: %w", err)
		}
		nalEnd := uint32(results[0])

		// Decode this NAL.
		results, err = d.inst.FnDecodeNAL.Call(ctx,
			uint64(d.decPtr), uint64(nalStart), uint64(nalEnd), 0, 0)
		if err != nil {
			return frames, fmt.Errorf("edge264: decode_NAL: %w", err)
		}
		ret := int32(results[0])

		// Drain available frames.
		drained, err := d.drainFrames(ctx)
		if err != nil {
			return frames, err
		}
		frames = append(frames, drained...)

		if ret == enobufs {
			continue // retry same NAL after draining
		}
		// Advance past the start code to the next NAL.
		if nalEnd >= endPtr {
			break
		}
		nalStart = nalEnd + 3
	}

	return frames, nil
}

// DecodeNAL decodes a single NAL unit (without start code prefix) and returns
// any frames that became available.
func (d *Decoder) DecodeNAL(nal []byte) ([]*image.YCbCr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, fmt.Errorf("edge264: decoder closed")
	}
	if len(nal) == 0 {
		return nil, nil
	}

	ctx := context.Background()

	// Prepend a 4-byte start code prefix. The C bitstream reader's get_bytes
	// loads 16 bytes from CPB-2 on refill, using the 2 bytes before the NAL
	// for emulation-prevention-byte detection. Without a valid prefix those
	// bytes are uninitialized heap data that can corrupt parsing.
	total := uint32(len(nal)) + 4
	bufPtr, err := d.ensureInput(ctx, total)
	if err != nil {
		return nil, err
	}
	d.inst.Write(bufPtr, []byte{0, 0, 0, 1})
	d.inst.Write(bufPtr+4, nal)
	nalStart := bufPtr + 4
	endPtr := nalStart + uint32(len(nal))

	results, err := d.inst.FnDecodeNAL.Call(ctx,
		uint64(d.decPtr), uint64(nalStart), uint64(endPtr), 0, 0)
	if err != nil {
		return nil, fmt.Errorf("edge264: decode_NAL: %w", err)
	}
	ret := int32(results[0])

	frames, err := d.drainFrames(ctx)
	if err != nil {
		return frames, err
	}

	if ret == enobufs {
		// Retry after draining.
		_, err = d.inst.FnDecodeNAL.Call(ctx,
			uint64(d.decPtr), uint64(nalStart), uint64(endPtr), 0, 0)
		if err != nil {
			return frames, fmt.Errorf("edge264: decode_NAL retry: %w", err)
		}
		more, err := d.drainFrames(ctx)
		frames = append(frames, more...)
		if err != nil {
			return frames, err
		}
	}

	return frames, nil
}

// Flush signals end-of-stream and returns all remaining buffered frames.
// The decoder remains usable for further Decode calls.
func (d *Decoder) Flush() ([]*image.YCbCr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, fmt.Errorf("edge264: decoder closed")
	}

	ctx := context.Background()

	// Sentinel: buf >= end triggers bump_all_frames in the C library.
	_, _ = d.inst.FnDecodeNAL.Call(ctx, uint64(d.decPtr), 1, 0, 0, 0) // nolint:errcheck

	return d.drainFrames(ctx)
}

// Reset resets the decoder for seeking. All buffered frames are discarded.
func (d *Decoder) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	ctx := context.Background()
	_, _ = d.inst.FnFlush.Call(ctx, uint64(d.decPtr)) // nolint:errcheck
}

// Close releases all resources. The Decoder must not be used after Close.
func (d *Decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true

	ctx := context.Background()

	// edge264_free takes Edge264Decoder**, so write ptr to the ptrPtr buffer.
	d.inst.WriteUint32(d.ptrPtr, d.decPtr)
	_, _ = d.inst.FnFreeDecoder.Call(ctx, uint64(d.ptrPtr)) // nolint:errcheck

	_ = d.inst.Free(ctx, d.framePtr) // nolint:errcheck
	_ = d.inst.Free(ctx, d.ptrPtr)   // nolint:errcheck
	if d.inputPtr != 0 {
		_ = d.inst.Free(ctx, d.inputPtr) // nolint:errcheck
	}

	return d.inst.Close(ctx)
}

// ensureInput ensures the WASM input buffer has at least size bytes.
func (d *Decoder) ensureInput(ctx context.Context, size uint32) (uint32, error) {
	if d.inputCap >= size {
		return d.inputPtr, nil
	}
	if d.inputPtr != 0 {
		_ = d.inst.Free(ctx, d.inputPtr) // nolint:errcheck
	}
	ptr, err := d.inst.Malloc(ctx, size)
	if err != nil {
		d.inputPtr = 0
		d.inputCap = 0
		return 0, fmt.Errorf("edge264: input alloc(%d): %w", size, err)
	}
	d.inputPtr = ptr
	d.inputCap = size
	return ptr, nil
}

// drainFrames reads all available frames from the decoder.
func (d *Decoder) drainFrames(ctx context.Context) ([]*image.YCbCr, error) {
	var frames []*image.YCbCr
	for {
		results, err := d.inst.FnGetFrame.Call(ctx,
			uint64(d.decPtr), uint64(d.framePtr), 0)
		if err != nil {
			return frames, fmt.Errorf("edge264: get_frame: %w", err)
		}
		if int32(results[0]) != 0 {
			break // ENOMSG or other = no more frames
		}

		img, err := readFrame(d.inst, d.framePtr)
		if err != nil {
			return frames, err
		}
		frames = append(frames, img)
	}
	return frames, nil
}
