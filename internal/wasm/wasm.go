// Package wasm provides WebAssembly runtime utilities for H.264 decoding.
package wasm

import (
	"context"
	_ "embed"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed edge264.wasm
var wasmBinary []byte

var (
	initOnce sync.Once
	initErr  error
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	counter  atomic.Uint64
)

func ensureCompiled(ctx context.Context) error {
	initOnce.Do(func() {
		rt = wazero.NewRuntime(ctx)
		wasi_snapshot_preview1.MustInstantiate(ctx, rt)
		compiled, initErr = rt.CompileModule(ctx, wasmBinary)
	})
	return initErr
}

// Instance represents a single wazero module instance with cached function references.
type Instance struct {
	mod api.Module
	mem api.Memory

	FnMalloc        api.Function
	FnFree          api.Function
	FnFindStartCode api.Function
	FnAlloc         api.Function
	FnFlush         api.Function
	FnFreeDecoder   api.Function
	FnDecodeNAL     api.Function
	FnGetFrame      api.Function
	FnReturnFrame   api.Function
}

// NewInstance creates a new wazero module instance.
func NewInstance(ctx context.Context) (*Instance, error) {
	if err := ensureCompiled(ctx); err != nil {
		return nil, fmt.Errorf("wasm compile: %w", err)
	}

	id := counter.Add(1)
	config := wazero.NewModuleConfig().
		WithName(fmt.Sprintf("edge264-%d", id)).
		WithStartFunctions("_initialize")

	mod, err := rt.InstantiateModule(ctx, compiled, config)
	if err != nil {
		return nil, fmt.Errorf("wasm instantiate: %w", err)
	}

	inst := &Instance{
		mod:             mod,
		mem:             mod.Memory(),
		FnMalloc:        mod.ExportedFunction("malloc"),
		FnFree:          mod.ExportedFunction("free"),
		FnFindStartCode: mod.ExportedFunction("edge264_find_start_code"),
		FnAlloc:         mod.ExportedFunction("edge264_alloc"),
		FnFlush:         mod.ExportedFunction("edge264_flush"),
		FnFreeDecoder:   mod.ExportedFunction("edge264_free"),
		FnDecodeNAL:     mod.ExportedFunction("edge264_decode_NAL"),
		FnGetFrame:      mod.ExportedFunction("edge264_get_frame"),
		FnReturnFrame:   mod.ExportedFunction("edge264_return_frame"),
	}

	if inst.FnMalloc == nil || inst.FnFree == nil ||
		inst.FnFindStartCode == nil || inst.FnAlloc == nil ||
		inst.FnFlush == nil || inst.FnFreeDecoder == nil ||
		inst.FnDecodeNAL == nil || inst.FnGetFrame == nil ||
		inst.FnReturnFrame == nil {
		mod.Close(ctx) // nolint:errcheck
		return nil, fmt.Errorf("wasm module missing required exports")
	}

	return inst, nil
}

// Malloc allocates size bytes in WASM linear memory.
func (i *Instance) Malloc(ctx context.Context, size uint32) (uint32, error) {
	results, err := i.FnMalloc.Call(ctx, uint64(size))
	if err != nil {
		return 0, err
	}
	ptr := uint32(results[0])
	if ptr == 0 {
		return 0, fmt.Errorf("wasm malloc(%d) returned NULL", size)
	}
	return ptr, nil
}

// Free releases WASM memory at ptr.
func (i *Instance) Free(ctx context.Context, ptr uint32) error {
	_, err := i.FnFree.Call(ctx, uint64(ptr))
	return err
}

// Write copies data into WASM linear memory at offset.
func (i *Instance) Write(offset uint32, data []byte) bool {
	return i.mem.Write(offset, data)
}

// Read returns a view into WASM linear memory. The caller must copy
// the data before the next WASM call if it needs to persist.
func (i *Instance) Read(offset, size uint32) ([]byte, bool) {
	return i.mem.Read(offset, size)
}

// ReadUint32 reads a little-endian uint32 from WASM memory.
func (i *Instance) ReadUint32(offset uint32) (uint32, bool) {
	return i.mem.ReadUint32Le(offset)
}

// WriteUint32 writes a little-endian uint32 to WASM memory.
func (i *Instance) WriteUint32(offset, val uint32) bool {
	return i.mem.WriteUint32Le(offset, val)
}

// Close releases the wazero module instance.
func (i *Instance) Close(ctx context.Context) error {
	return i.mod.Close(ctx)
}
