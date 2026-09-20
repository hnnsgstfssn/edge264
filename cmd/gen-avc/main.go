// Command gen-avc generates H.264 Annex B bitstreams from YAML test
// descriptions. It is a faithful translation of tests/gen_avc.py.
//
// Usage: gen-avc input.yaml output.264
package main

import (
	"fmt"
	"io"
	"log"
	"math"
	"math/bits"
	"os"
	"regexp"
	"slices"
	"strconv"

	"github.com/goccy/go-yaml"
)

// ---------------------------------------------------------------------------
// YAML helpers — the data is map[string]any all the way down.
// ---------------------------------------------------------------------------

func has(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func getInt(m map[string]any, key string) int {
	return toInt(m[key])
}

func getFloat(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	}
	log.Fatalf("getFloat: key %q not a float in %v", key, m)
	return 0
}

func getMap(m map[string]any, key string) map[string]any {
	v, ok := m[key].(map[string]any)
	if !ok {
		log.Fatalf("getMap: key %q not a map in %v", key, m)
	}
	return v
}

func getSlice(m map[string]any, key string) []any {
	if m[key] == nil {
		return nil
	}
	v, ok := m[key].([]any)
	if !ok {
		log.Fatalf("getSlice: key %q not a slice in %v", key, m)
	}
	return v
}

func getIntSlice(m map[string]any, key string) []int {
	raw := getSlice(m, key)
	out := make([]int, len(raw))
	for i, v := range raw {
		out[i] = toInt(v)
	}
	return out
}

func asMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		log.Fatalf("asMap: %T is not map[string]any", v)
	}
	return m
}

// ---------------------------------------------------------------------------
// bitWriter — accumulates bits into a byte buffer.
//
// The Python code uses a big-integer with a leading sentinel bit. Here we use
// an explicit partial-byte accumulator instead.
// ---------------------------------------------------------------------------

type bitWriter struct {
	buf   []byte // complete bytes accumulated
	cur   byte   // partial byte being built (MSB-first)
	nbits int    // bits used in cur (0-7)
}

// writeBits writes n bits from val (MSB-first, n <= 64).
func (w *bitWriter) writeBits(n int, val uint64) {
	for n > 0 {
		avail := min(n, 8-w.nbits)
		// Extract the top 'avail' bits from the remaining n bits of val.
		shift := n - avail
		w.cur |= byte((val>>shift)&((1<<avail)-1)) << (8 - w.nbits - avail)
		w.nbits += avail
		n -= avail
		if w.nbits == 8 {
			w.buf = append(w.buf, w.cur)
			w.cur = 0
			w.nbits = 0
		}
	}
}

// writeVLC parses a binary string like "00110" and writes its bits.
func (w *bitWriter) writeVLC(s string) {
	v, err := strconv.ParseUint(s, 2, 64)
	if err != nil {
		log.Fatalf("writeVLC: bad string %q: %v", s, err)
	}
	w.writeBits(len(s), v)
}

// writeUE writes an unsigned Exp-Golomb code.
func (w *bitWriter) writeUE(v int) {
	x := uint64(v + 1)
	bl := bits.Len64(x)
	w.writeBits(bl*2-1, x)
}

// writeSE writes a signed Exp-Golomb code.
func (w *bitWriter) writeSE(v int) {
	var u int
	if v > 0 {
		u = v * 2
	} else {
		u = -v*2 + 1
	}
	w.writeUE(u - 1)
}

// alignByte pads to the next byte boundary (for I_PCM).
func (w *bitWriter) alignByte() {
	if w.nbits > 0 {
		w.buf = append(w.buf, w.cur)
		w.cur = 0
		w.nbits = 0
	}
}

// bitLen returns total accumulated bits.
func (w *bitWriter) bitLen() int {
	return len(w.buf)*8 + w.nbits
}

// flush writes complete bytes (with EPB escaping) to out, keeps partial byte.
func (w *bitWriter) flush(out io.Writer) {
	if len(w.buf) > 0 {
		if _, err := out.Write(escape(w.buf)); err != nil {
			log.Fatal(err)
		}
		w.buf = w.buf[:0]
	}
}

// finalize pads to byte boundary and returns all bytes with EPB escaping.
func (w *bitWriter) finalize() []byte {
	if w.nbits > 0 {
		w.buf = append(w.buf, w.cur)
		w.cur = 0
		w.nbits = 0
	}
	out := escape(w.buf)
	w.buf = w.buf[:0]
	return out
}

// ---------------------------------------------------------------------------
// EPB (Emulation Prevention Byte) escaping.
// Replaces 00 00 {00,01,02,03} → 00 00 03 {00,01,02,03}.
// ---------------------------------------------------------------------------

func escape(b []byte) []byte {
	if len(b) < 3 {
		return b
	}
	var out []byte
	i := 0
	for i < len(b) {
		if i+2 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] <= 3 {
			out = append(out, 0, 0, 3, b[i+2])
			i += 3
		} else {
			out = append(out, b[i])
			i++
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// VLC tables — verbatim from Python.
// ---------------------------------------------------------------------------

var ct02 = [][]string{{"1"}, {"000101", "01"}, {"00000111", "000100", "001"}, {"000000111", "00000110", "0000101", "00011"}, {"0000000111", "000000110", "00000101", "000011"}, {"00000000111", "0000000110", "000000101", "0000100"}, {"0000000001111", "00000000110", "0000000101", "00000100"}, {"0000000001011", "0000000001110", "00000000101", "000000100"}, {"0000000001000", "0000000001010", "0000000001101", "0000000100"}, {"00000000001111", "00000000001110", "0000000001001", "00000000100"}, {"00000000001011", "00000000001010", "00000000001101", "0000000001100"}, {"000000000001111", "000000000001110", "00000000001001", "00000000001100"}, {"000000000001011", "000000000001010", "000000000001101", "00000000001000"}, {"0000000000001111", "000000000000001", "000000000001001", "000000000001100"}, {"0000000000001011", "0000000000001110", "0000000000001101", "000000000001000"}, {"0000000000000111", "0000000000001010", "0000000000001001", "0000000000001100"}, {"0000000000000100", "0000000000000110", "0000000000000101", "0000000000001000"}}
var ct24 = [][]string{{"11"}, {"001011", "10"}, {"000111", "00111", "011"}, {"0000111", "001010", "001001", "0101"}, {"00000111", "000110", "000101", "0100"}, {"00000100", "0000110", "0000101", "00110"}, {"000000111", "00000110", "00000101", "001000"}, {"00000001111", "000000110", "000000101", "000100"}, {"00000001011", "00000001110", "00000001101", "0000100"}, {"000000001111", "00000001010", "00000001001", "000000100"}, {"000000001011", "000000001110", "000000001101", "00000001100"}, {"000000001000", "000000001010", "000000001001", "00000001000"}, {"0000000001111", "0000000001110", "0000000001101", "000000001100"}, {"0000000001011", "0000000001010", "0000000001001", "0000000001100"}, {"0000000000111", "00000000001011", "0000000000110", "0000000001000"}, {"00000000001001", "00000000001000", "00000000001010", "0000000000001"}, {"00000000000111", "00000000000110", "00000000000101", "00000000000100"}}
var ct48 = [][]string{{"1111"}, {"001111", "1110"}, {"001011", "01111", "1101"}, {"001000", "01100", "01110", "1100"}, {"0001111", "01010", "01011", "1011"}, {"0001011", "01000", "01001", "1010"}, {"0001001", "001110", "001101", "1001"}, {"0001000", "001010", "001001", "1000"}, {"00001111", "0001110", "0001101", "01101"}, {"00001011", "00001110", "0001010", "001100"}, {"000001111", "00001010", "00001101", "0001100"}, {"000001011", "000001110", "00001001", "00001100"}, {"000001000", "000001010", "000001101", "00001000"}, {"0000001101", "000000111", "000001001", "000001100"}, {"0000001001", "0000001100", "0000001011", "0000001010"}, {"0000000101", "0000001000", "0000000111", "0000000110"}, {"0000000001", "0000000100", "0000000011", "0000000010"}}
var ct8 = [][]string{{"000011"}, {"000000", "000001"}, {"000100", "000101", "000110"}, {"001000", "001001", "001010", "001011"}, {"001100", "001101", "001110", "001111"}, {"010000", "010001", "010010", "010011"}, {"010100", "010101", "010110", "010111"}, {"011000", "011001", "011010", "011011"}, {"011100", "011101", "011110", "011111"}, {"100000", "100001", "100010", "100011"}, {"100100", "100101", "100110", "100111"}, {"101000", "101001", "101010", "101011"}, {"101100", "101101", "101110", "101111"}, {"110000", "110001", "110010", "110011"}, {"110100", "110101", "110110", "110111"}, {"111000", "111001", "111010", "111011"}, {"111100", "111101", "111110", "111111"}}
var ctn1 = [][]string{{"01"}, {"000111", "1"}, {"000100", "000110", "001"}, {"000011", "0000011", "0000010", "000101"}, {"000010", "00000011", "00000010", "0000000"}}
var ctn2 = [][]string{{"1"}, {"0001111", "01"}, {"0001110", "0001101", "001"}, {"000000111", "0001100", "0001011", "00001"}, {"000000110", "000000101", "0001010", "000001"}, {"0000000111", "0000000110", "000000100", "0001001"}, {"00000000111", "00000000110", "0000000101", "0001000"}, {"000000000111", "000000000110", "00000000101", "0000000100"}, {"0000000000111", "000000000101", "000000000100", "00000000100"}}
var tz4x4 = [][]string{{"1", "011", "010", "0011", "0010", "00011", "00010", "000011", "000010", "0000011", "0000010", "00000011", "00000010", "000000011", "000000010", "000000001"}, {"111", "110", "101", "100", "011", "0101", "0100", "0011", "0010", "00011", "00010", "000011", "000010", "000001", "000000"}, {"0101", "111", "110", "101", "0100", "0011", "100", "011", "0010", "00011", "00010", "000001", "00001", "000000"}, {"00011", "111", "0101", "0100", "110", "101", "100", "0011", "011", "0010", "00010", "00001", "00000"}, {"0101", "0100", "0011", "111", "110", "101", "100", "011", "0010", "00001", "0001", "00000"}, {"000001", "00001", "111", "110", "101", "100", "011", "010", "0001", "001", "000000"}, {"000001", "00001", "101", "100", "011", "11", "010", "0001", "001", "000000"}, {"000001", "0001", "00001", "011", "11", "10", "010", "001", "000000"}, {"000001", "000000", "0001", "11", "10", "001", "01", "00001"}, {"00001", "00000", "001", "11", "10", "01", "0001"}, {"0000", "0001", "001", "010", "1", "011"}, {"0000", "0001", "01", "1", "001"}, {"000", "001", "1", "01"}, {"00", "01", "1"}, {"0", "1"}}
var tz2x4 = [][]string{{"1", "010", "011", "0010", "0011", "0001", "00001", "00000"}, {"000", "01", "001", "100", "101", "110", "111"}, {"000", "001", "01", "10", "110", "111"}, {"110", "00", "01", "10", "111"}, {"00", "01", "10", "11"}, {"00", "01", "1"}, {"0", "1"}}
var tz2x2 = [][]string{{"1", "01", "001", "000"}, {"1", "01", "00"}, {"1", "0"}}
var rb = [][]string{{"1", "0"}, {"1", "01", "00"}, {"11", "10", "01", "00"}, {"11", "10", "01", "001", "000"}, {"11", "10", "011", "010", "001", "000"}, {"11", "000", "001", "011", "010", "101", "100"}, {"111", "110", "101", "100", "011", "010", "001", "0001", "00001", "000001", "0000001", "00000001", "000000001", "0000000001", "00000000001"}}

var meIntra = [48]int{3, 29, 30, 17, 31, 18, 37, 8, 32, 38, 19, 9, 20, 10, 11, 2, 16, 33, 34, 21, 35, 22, 39, 4, 36, 40, 23, 5, 24, 6, 7, 1, 41, 42, 43, 25, 44, 26, 46, 12, 45, 47, 27, 13, 28, 14, 15, 0}
var meInter = [48]int{0, 2, 3, 7, 4, 8, 17, 13, 5, 18, 9, 14, 10, 15, 16, 11, 1, 32, 33, 36, 34, 37, 44, 40, 35, 45, 38, 41, 39, 42, 43, 19, 6, 24, 25, 20, 26, 21, 46, 28, 27, 47, 22, 29, 23, 30, 31, 12}

// ---------------------------------------------------------------------------
// genResidualBlockCAVLC
// ---------------------------------------------------------------------------

func genResidualBlockCAVLC(w *bitWriter, nC int, coeffs []int) {
	// Find non-zero coefficient indices and values.
	var icoeffs []int
	var nzcoeffs []int
	for i, c := range coeffs {
		if c != 0 {
			icoeffs = append(icoeffs, i)
			nzcoeffs = append(nzcoeffs, c)
		}
	}
	totalCoeff := len(nzcoeffs)

	// Count trailing ones (max 3).
	trailingOnes := 0
	for _, c := range slices.Backward(nzcoeffs) {
		if abs(c) > 1 {
			break
		}
		trailingOnes++
	}
	if trailingOnes > 3 {
		trailingOnes = 3
	}

	// Select coeff_token table.
	var table [][]string
	switch {
	case nC == -2:
		table = ctn2
	case nC == -1:
		table = ctn1
	case nC < 2:
		table = ct02
	case nC < 4:
		table = ct24
	case nC < 8:
		table = ct48
	default:
		table = ct8
	}
	w.writeVLC(table[totalCoeff][trailingOnes])

	if totalCoeff == 0 {
		return
	}

	suffixLength := 0
	if totalCoeff > 10 && trailingOnes < 3 {
		suffixLength = 1
	}

	for i, c := range slices.Backward(nzcoeffs) {
		idx := len(nzcoeffs) - 1 - i // reversed index
		if idx < trailingOnes {
			if c < 0 {
				w.writeBits(1, 1)
			} else {
				w.writeBits(1, 0)
			}
		} else {
			neg := 0
			if c < 0 {
				neg = 1
			}
			levelCode := abs(c)*2 - 2 + neg
			if idx == trailingOnes && trailingOnes < 3 {
				levelCode -= 2
			}

			levelPrefix := levelCode >> suffixLength
			levelSuffixSize := suffixLength

			if suffixLength == 0 && levelCode >= 14 && levelCode < 30 {
				levelPrefix = 14
				levelCode -= 14
				levelSuffixSize = 4
			}
			maxSuffix := max(suffixLength, 1)
			if levelCode >= 15<<maxSuffix {
				levelCode += 4096 - (15 << maxSuffix)
				levelPrefix = bits.Len(uint(levelCode)) + 2
				levelSuffixSize = levelPrefix - 3
			}

			w.writeBits(levelPrefix+1, 1)
			if levelSuffixSize > 0 {
				w.writeBits(levelSuffixSize, uint64(levelCode)&((1<<levelSuffixSize)-1))
			}

			if suffixLength == 0 {
				suffixLength = 1
			}
			threshold := 3 << (suffixLength - 1)
			if abs(c) > threshold {
				suffixLength++
			}
			if suffixLength > 6 {
				suffixLength = 6
			}
		}
	}

	// total_zeros
	zerosLeft := 0
	if totalCoeff < len(coeffs) {
		zerosLeft = icoeffs[len(icoeffs)-1] - len(icoeffs) + 1
		var tzTable [][]string
		switch {
		case len(coeffs) >= 15:
			tzTable = tz4x4
		case len(coeffs) == 8:
			tzTable = tz2x4
		default:
			tzTable = tz2x2
		}
		w.writeVLC(tzTable[totalCoeff-1][zerosLeft])
	}

	// run_before
	for i := totalCoeff - 1; i > 0; i-- {
		if zerosLeft > 0 {
			runBefore := icoeffs[i] - icoeffs[i-1] - 1
			idx := min(zerosLeft-1, 6)
			w.writeVLC(rb[idx][runBefore])
			zerosLeft -= runBefore
		}
	}
}

// ---------------------------------------------------------------------------
// genSliceDataCAVLC
// ---------------------------------------------------------------------------

func genSliceDataCAVLC(w *bitWriter, out io.Writer, slice map[string]any, sliceType int) {
	skipRun := 0
	mbs := getSlice(slice, "macroblocks_cavlc")
	for _, rawMB := range mbs {
		mb := asMap(rawMB)

		// Flush complete bytes to file between macroblocks.
		w.flush(out)

		if has(mb, "mb_skip_run") {
			w.writeUE(getInt(mb, "mb_skip_run"))
			skipRun = getInt(mb, "mb_skip_run")
		}
		skipRun--
		if skipRun >= 0 {
			continue
		}

		if has(mb, "mb_field_decoding_flag") {
			w.writeBits(1, uint64(getInt(mb, "mb_field_decoding_flag")))
		}

		// macroblock_layer()
		mbType := getInt(mb, "mb_type")
		w.writeUE(mbType)

		ipcmTypes := [3]int{30, 48, 25}
		if mbType == ipcmTypes[sliceType] {
			w.alignByte()
			pcm := asMap(mb["pcm_samples"])
			bitsY := getInt(pcm, "bits_Y")
			bitsC := getInt(pcm, "bits_C")
			for _, s := range getSlice(pcm, "Y") {
				w.writeBits(bitsY, uint64(toInt(s)))
			}
			cb := getSlice(pcm, "Cb")
			cr := getSlice(pcm, "Cr")
			for _, s := range cb {
				w.writeBits(bitsC, uint64(toInt(s)))
			}
			for _, s := range cr {
				w.writeBits(bitsC, uint64(toInt(s)))
			}
		}

		inxnTypes := [3]int{5, 23, 0}
		if mbType == inxnTypes[sliceType] && has(mb, "transform_size_8x8_flag") {
			w.writeBits(1, uint64(getInt(mb, "transform_size_8x8_flag")))
		}

		// rem_intra4x4_pred_modes or rem_intra8x8_pred_modes
		var predModes []any
		if has(mb, "rem_intra4x4_pred_modes") {
			predModes = getSlice(mb, "rem_intra4x4_pred_modes")
		} else if has(mb, "rem_intra8x8_pred_modes") {
			predModes = getSlice(mb, "rem_intra8x8_pred_modes")
		}
		for _, rawMode := range predModes {
			mode := toInt(rawMode)
			if mode < 0 {
				w.writeBits(1, 1)
			} else {
				w.writeBits(1, 0)
				w.writeBits(3, uint64(mode))
			}
		}

		if has(mb, "intra_chroma_pred_mode") {
			w.writeUE(getInt(mb, "intra_chroma_pred_mode"))
		}

		// sub_mb_types for P/B sub-8x8
		subTypes := [3][]int{{3, 4}, {22}, {}}
		if intIn(mbType, subTypes[sliceType]) {
			for _, raw := range getSlice(mb, "sub_mb_types") {
				w.writeUE(toInt(raw))
			}
		}

		// non-Direct Inter
		interTypes := [3][]int{rangeSlice(0, 5), rangeSlice(1, 23), {}}
		if intIn(mbType, interTypes[sliceType]) {
			refIdx := getMap(mb, "ref_idx")
			numRefIdx := getMap(slice, "num_ref_idx_active")
			for k, v := range refIdx {
				idx := toInt(v)
				// Determine l0 or l1 from key: keys < 4 are l0, >= 4 are l1
				ki, _ := strconv.Atoi(k) // nolint:errcheck
				lKey := fmt.Sprintf("l%d", ki/4)
				if getInt(numRefIdx, lKey) == 2 {
					w.writeBits(1, uint64(idx^1))
				} else {
					w.writeUE(idx)
				}
			}
			mvds := getSlice(mb, "mvds")
			for _, rawPair := range mvds {
				pair := rawPair.([]any) // nolint:errcheck
				w.writeSE(toInt(pair[0]))
				w.writeSE(toInt(pair[1]))
			}
		}

		// coded_block_pattern (not Intra_16x16 nor PCM)
		cbpTypes := [3][]int{rangeSlice(0, 6), rangeSlice(0, 24), {0}}
		if intIn(mbType, cbpTypes[sliceType]) {
			cbp := getInt(mb, "coded_block_pattern")
			if mbType < inxnTypes[sliceType] {
				w.writeUE(meInter[cbp])
			} else {
				w.writeUE(meIntra[cbp])
			}
		}

		// transform_size_8x8_flag (not I_NxN)
		if mbType != inxnTypes[sliceType] && has(mb, "transform_size_8x8_flag") {
			w.writeBits(1, uint64(getInt(mb, "transform_size_8x8_flag")))
		}

		if has(mb, "mb_qp_delta") {
			w.writeSE(getInt(mb, "mb_qp_delta"))
			for _, rawBlock := range getSlice(mb, "coeffLevels") {
				block := asMap(rawBlock)
				var coeffs []int
				if has(block, "c") {
					coeffs = toIntSlice(getSlice(block, "c"))
				}
				genResidualBlockCAVLC(w, getInt(block, "nC"), coeffs)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// genSliceLayer
// ---------------------------------------------------------------------------

func genSliceLayer(w *bitWriter, out io.Writer, slice map[string]any) {
	w.writeUE(getInt(slice, "first_mb_in_slice"))
	w.writeUE(getInt(slice, "slice_type"))
	sliceType := getInt(slice, "slice_type") % 5
	w.writeUE(getInt(slice, "pic_parameter_set_id"))

	if has(slice, "colour_plane_id") {
		w.writeBits(2, uint64(getInt(slice, "colour_plane_id")))
	}

	frameNum := getMap(slice, "frame_num")
	fnBits := getInt(frameNum, "bits")
	fnAbs := getInt(frameNum, "absolute")
	w.writeBits(fnBits, uint64(fnAbs)&((1<<fnBits)-1))

	if has(slice, "field_pic_flag") {
		fpf := getInt(slice, "field_pic_flag")
		w.writeBits(1, uint64(fpf))
		if fpf != 0 {
			w.writeBits(1, uint64(getInt(slice, "bottom_field_flag")))
		}
	}

	nalUnitType := getInt(slice, "nal_unit_type")
	nonIdrFlag := 1
	if has(slice, "non_idr_flag") {
		nonIdrFlag = getInt(slice, "non_idr_flag")
	}
	idrPicFlag := nalUnitType == 5 || nonIdrFlag == 0

	if idrPicFlag {
		w.writeUE(getInt(slice, "idr_pic_id"))
	}

	poc := getMap(slice, "pic_order_cnt")
	pocType := getInt(poc, "type")
	if pocType == 0 {
		pocBits := getInt(poc, "bits")
		pocAbs := getInt(poc, "absolute")
		w.writeBits(pocBits, uint64(pocAbs)&((1<<pocBits)-1))
		if has(poc, "bottom") {
			w.writeSE(getInt(poc, "bottom") - getInt(poc, "absolute"))
		}
	}
	if pocType == 1 && has(poc, "delta0") {
		w.writeSE(getInt(poc, "delta0"))
		if has(poc, "delta1") {
			w.writeSE(getInt(poc, "delta1"))
		}
	}

	if sliceType == 1 {
		w.writeBits(1, uint64(getInt(slice, "direct_spatial_mv_pred_flag")))
	}

	if sliceType <= 1 {
		numRefIdx := getMap(slice, "num_ref_idx_active")
		overrideFlag := getInt(numRefIdx, "override_flag")
		w.writeBits(1, uint64(overrideFlag))
		if overrideFlag != 0 {
			w.writeUE(getInt(numRefIdx, "l0") - 1)
			if sliceType == 1 {
				w.writeUE(getInt(numRefIdx, "l1") - 1)
			}
		}

		fieldToIdc := map[string]int{"sref": 1, "lref": 2, "view": 5}
		for i := 0; i <= sliceType; i++ {
			key := fmt.Sprintf("ref_pic_list_modification_l%d", i)
			if has(slice, key) {
				w.writeBits(1, 1)
				for _, rawEntry := range getSlice(slice, key) {
					entry := rawEntry.([]any)  // nolint:errcheck
					field := entry[0].(string) // nolint:errcheck
					diff := toInt(entry[1])
					idc := fieldToIdc[field]
					if diff < 0 {
						idc--
					}
					w.writeUE(idc)
					if field == "lref" {
						w.writeUE(diff)
					} else {
						w.writeUE(abs(diff) - 1)
					}
				}
				w.writeUE(3)
			} else {
				w.writeBits(1, 0)
			}
		}

		if has(slice, "explicit_weights_l0") {
			re := regexp.MustCompile(`-?\d+`)

			wl0 := getSlice(slice, "explicit_weights_l0")
			first := asMap(wl0[0])
			yNums := re.FindAllString(first["Y"].(string), -1)   // nolint:errcheck
			cbNums := re.FindAllString(first["Cb"].(string), -1) // nolint:errcheck
			yDenom, _ := strconv.Atoi(yNums[1])                  // nolint:errcheck
			cbDenom, _ := strconv.Atoi(cbNums[1])                // nolint:errcheck
			w.writeUE(yDenom)
			w.writeUE(cbDenom)

			for i := 0; i <= sliceType; i++ {
				key := fmt.Sprintf("explicit_weights_l%d", i)
				for _, rawRef := range getSlice(slice, key) {
					ref := asMap(rawRef)
					for _, plane := range []string{"Y", "Cb", "Cr"} {
						nums := re.FindAllString(ref[plane].(string), -1) // nolint:errcheck
						weight, _ := strconv.Atoi(nums[0])                // nolint:errcheck
						denom, _ := strconv.Atoi(nums[1])                 // nolint:errcheck
						offset, _ := strconv.Atoi(nums[2])                // nolint:errcheck
						defaultWeight := 1 << denom
						if weight != defaultWeight || offset != 0 {
							w.writeBits(1, 1)
							w.writeSE(weight)
							w.writeSE(offset)
						} else {
							w.writeBits(1, 0)
						}
					}
				}
			}
		}
	}

	nalRefIdc := getInt(slice, "nal_ref_idc")
	if nalRefIdc != 0 {
		if idrPicFlag {
			w.writeBits(1, uint64(getInt(slice, "no_output_of_prior_pics_flag")))
			w.writeBits(1, uint64(getInt(slice, "long_term_reference_flag")))
		} else {
			if has(slice, "memory_management_control_operations") {
				w.writeBits(1, 1)
				for _, rawMMCO := range getSlice(slice, "memory_management_control_operations") {
					mmco := asMap(rawMMCO)
					w.writeUE(getInt(mmco, "mmco"))
					if has(mmco, "sref") {
						w.writeUE(-getInt(mmco, "sref"))
					}
					if has(mmco, "lref") {
						w.writeUE(getInt(mmco, "lref"))
					}
				}
				w.writeBits(1, 1) // memory_management_control_operation == 0
			} else {
				w.writeBits(1, 0)
			}
		}
	}

	if has(slice, "cabac_init_idc") {
		w.writeUE(getInt(slice, "cabac_init_idc"))
	}
	w.writeSE(getInt(slice, "slice_qp_delta"))

	if has(slice, "disable_deblocking_filter_idc") {
		ddf := getInt(slice, "disable_deblocking_filter_idc")
		w.writeUE(ddf)
		if ddf != 1 {
			w.writeSE(getInt(slice, "slice_alpha_c0_offset") >> 1)
			w.writeSE(getInt(slice, "slice_beta_offset") >> 1)
		}
	}

	if has(slice, "macroblocks_cavlc") {
		genSliceDataCAVLC(w, out, slice, sliceType)
	} else {
		log.Fatal("CABAC slice data generation is not implemented")
	}
}

// ---------------------------------------------------------------------------
// genSEI
// ---------------------------------------------------------------------------

func genSEI(w *bitWriter, nal map[string]any) {
	seiMessages := getSlice(nal, "sei_messages")
	for _, rawSEI := range seiMessages {
		sei := asMap(rawSEI)
		payloadType := getInt(sei, "payloadType")

		// payloadType prefix: (payloadType / 255) bytes of 0xFF, then remainder.
		ffCount := payloadType / 255
		w.writeBits(ffCount*8, (1<<(ffCount*8))-1)
		w.writeBits(8, uint64(payloadType%255))

		// Build payload in a sub-writer.
		var pw bitWriter
		if payloadType == 0 {
			pw.writeBits(1, 1) // seq_parameter_set_id
			delayBits := getInt(sei, "delay_bits")
			for _, rawCPB := range getSlice(sei, "nal_hrd_cpbs") {
				cpb := asMap(rawCPB)
				pw.writeBits(delayBits, uint64(getInt(cpb, "initial_cpb_removal_delay")))
				pw.writeBits(delayBits, uint64(getInt(cpb, "initial_cpb_removal_delay_offset")))
			}
			for _, rawCPB := range getSlice(sei, "vcl_hrd_cpbs") {
				cpb := asMap(rawCPB)
				pw.writeBits(delayBits, uint64(getInt(cpb, "initial_cpb_removal_delay")))
				pw.writeBits(delayBits, uint64(getInt(cpb, "initial_cpb_removal_delay_offset")))
			}
		}

		payloadBits := pw.bitLen()
		payloadSize := (payloadBits + 7) / 8

		// payloadSize prefix.
		ffCountS := payloadSize / 255
		w.writeBits(ffCountS*8, (1<<(ffCountS*8))-1)
		w.writeBits(8, uint64(payloadSize%255))

		// Write payload bits.
		if pw.nbits > 0 || len(pw.buf) > 0 {
			for _, b := range pw.buf {
				w.writeBits(8, uint64(b))
			}
			if pw.nbits > 0 {
				w.writeBits(pw.nbits, uint64(pw.cur>>(8-pw.nbits)))
			}
		}

		// Payload alignment: rbsp_trailing_bits if not byte-aligned.
		if payloadBits%8 > 0 {
			pad := 8 - payloadBits%8
			// bit_equal_to_one followed by (pad-1) bit_equal_to_zero
			w.writeBits(pad, 1<<(pad-1))
		}
	}
}

// ---------------------------------------------------------------------------
// genHRDParameters
// ---------------------------------------------------------------------------

func genHRDParameters(w *bitWriter, hrd map[string]any) {
	cpbs := getSlice(hrd, "cpbs")
	w.writeUE(len(cpbs) - 1)

	// Compute bit_rate_scale and cpb_size_scale.
	var brOr, szOr uint64
	for _, raw := range cpbs {
		cpb := asMap(raw)
		brOr |= uint64(getInt(cpb, "bit_rate"))
		szOr |= uint64(getInt(cpb, "size"))
	}
	bitRateScale := min(bits.TrailingZeros64(brOr)-6, 15)
	cpbSizeScale := min(bits.TrailingZeros64(szOr)-4, 15)

	w.writeBits(4, uint64(bitRateScale))
	w.writeBits(4, uint64(cpbSizeScale))

	for _, raw := range cpbs {
		cpb := asMap(raw)
		w.writeUE(int(uint64(getInt(cpb, "bit_rate"))>>6>>bitRateScale) - 1)
		w.writeUE(int(uint64(getInt(cpb, "size"))>>4>>cpbSizeScale) - 1)
		w.writeBits(1, uint64(getInt(cpb, "cbr_flag")))
	}

	w.writeBits(5, uint64(getInt(hrd, "initial_cpb_removal_delay_length")-1))
	w.writeBits(5, uint64(getInt(hrd, "cpb_removal_delay_length")-1))
	w.writeBits(5, uint64(getInt(hrd, "dpb_output_delay_length")-1))
	w.writeBits(5, uint64(getInt(hrd, "time_offset_length")))
}

// ---------------------------------------------------------------------------
// genVUIParameters
// ---------------------------------------------------------------------------

func genVUIParameters(w *bitWriter, sps, vui map[string]any) {
	if has(vui, "aspect_ratio") {
		w.writeBits(1, 1)
		ar := getMap(vui, "aspect_ratio")
		idc := getInt(ar, "idc")
		w.writeBits(8, uint64(idc))
		if idc == 255 {
			w.writeBits(16, uint64(getInt(ar, "width")))
			w.writeBits(16, uint64(getInt(ar, "height")))
		}
	} else {
		w.writeBits(1, 0)
	}

	osf := getInt(vui, "overscan_appropriate_flag")
	v := min(osf+1, 1)
	w.writeBits(1, uint64(v))
	if osf >= 0 {
		w.writeBits(1, uint64(osf))
	}

	if has(vui, "video_format") {
		w.writeBits(1, 1)
		w.writeBits(3, uint64(getInt(vui, "video_format")))
		w.writeBits(1, uint64(getInt(vui, "video_full_range_flag")))
		if has(vui, "colour_primaries") {
			w.writeBits(1, 1)
			w.writeBits(8, uint64(getInt(vui, "colour_primaries")))
			w.writeBits(8, uint64(getInt(vui, "transfer_characteristics")))
			w.writeBits(8, uint64(getInt(vui, "matrix_coefficients")))
		} else {
			w.writeBits(1, 0)
		}
	} else {
		w.writeBits(1, 0)
	}

	if has(vui, "chroma_sample_loc") {
		w.writeBits(1, 1)
		csl := getMap(vui, "chroma_sample_loc")
		w.writeUE(getInt(csl, "top"))
		w.writeUE(getInt(csl, "bottom"))
	} else {
		w.writeBits(1, 0)
	}

	if has(vui, "num_units_in_tick") {
		w.writeBits(1, 1)
		w.writeBits(32, uint64(getInt(vui, "num_units_in_tick")))
		w.writeBits(32, uint64(getInt(vui, "time_scale")))
		w.writeBits(1, uint64(getInt(vui, "fixed_frame_rate_flag")))
	} else {
		w.writeBits(1, 0)
	}

	if has(vui, "nal_hrd_parameters") {
		w.writeBits(1, 1)
		genHRDParameters(w, getMap(vui, "nal_hrd_parameters"))
	} else {
		w.writeBits(1, 0)
	}

	if has(vui, "vcl_hrd_parameters") {
		w.writeBits(1, 1)
		genHRDParameters(w, getMap(vui, "vcl_hrd_parameters"))
	} else {
		w.writeBits(1, 0)
	}

	if has(vui, "nal_hrd_parameters") || has(vui, "vcl_hrd_parameters") {
		w.writeBits(1, uint64(getInt(vui, "low_delay_hrd_flag")))
	}

	w.writeBits(1, uint64(getInt(vui, "pic_struct_present_flag")))

	if has(vui, "motion_vectors_over_pic_boundaries_flag") {
		w.writeBits(1, 1)
		w.writeBits(1, uint64(getInt(vui, "motion_vectors_over_pic_boundaries_flag")))

		picSizeInMbs := getMap(sps, "pic_size_in_mbs")
		psMbs := getInt(picSizeInMbs, "width") * getInt(picSizeInMbs, "height")
		bitDepth := getMap(sps, "bit_depth")
		chromaFmtIdc := getInt(sps, "chroma_format_idc")
		rawMbBits := 256*getInt(bitDepth, "luma") + ((64<<chromaFmtIdc)&^64)*getInt(bitDepth, "chroma")

		maxBytesPerPicDenom := 0
		if has(vui, "max_bytes_per_pic") {
			maxBytesPerPicDenom = (psMbs * rawMbBits) / getInt(vui, "max_bytes_per_pic") / 8
		}
		w.writeUE(maxBytesPerPicDenom)

		maxBitsPerMbDenom := 0
		if has(vui, "max_bits_per_mb") {
			maxBitsPerMbDenom = (128 + rawMbBits) / getInt(vui, "max_bits_per_mb")
		}
		w.writeUE(maxBitsPerMbDenom)

		w.writeUE(getInt(vui, "log2_max_mv_length_horizontal"))
		w.writeUE(getInt(vui, "log2_max_mv_length_vertical"))
		w.writeUE(getInt(vui, "max_num_reorder_frames"))
		w.writeUE(getInt(vui, "max_dec_frame_buffering"))
	} else {
		w.writeBits(1, 0)
	}
}

// ---------------------------------------------------------------------------
// genSPS
// ---------------------------------------------------------------------------

func genSPS(w *bitWriter, sps map[string]any) {
	profileIdc := getInt(sps, "profile_idc")
	w.writeBits(8, uint64(profileIdc))

	// constraint_set_flags — sum(f << (7-i) for i,f in enumerate(...))
	flags := getSlice(sps, "constraint_set_flags")
	var csf uint64
	for i, raw := range flags {
		csf |= uint64(toInt(raw)) << (7 - i)
	}
	w.writeBits(8, csf)

	levelIdc := int(math.Round(getFloat(sps, "level_idc") * 10))
	w.writeBits(8, uint64(levelIdc))

	w.writeBits(1, 1) // seq_parameter_set_id = 0 (UE = 1 bit "1")

	highProfiles := map[int]bool{100: true, 110: true, 122: true, 244: true, 44: true, 83: true, 86: true, 118: true, 128: true, 138: true, 139: true, 134: true, 135: true}
	if highProfiles[profileIdc] {
		chromaFmtIdc := getInt(sps, "chroma_format_idc")
		w.writeUE(chromaFmtIdc)
		if chromaFmtIdc == 3 {
			w.writeBits(1, uint64(getInt(sps, "separate_colour_plane_flag")))
		}
		bd := getMap(sps, "bit_depth")
		w.writeUE(getInt(bd, "luma") - 8)
		w.writeUE(getInt(bd, "chroma") - 8)
		w.writeBits(1, uint64(getInt(sps, "qpprime_y_zero_transform_bypass_flag")))

		if has(sps, "seq_scaling_matrix") {
			w.writeBits(1, 1)
			for _, rawList := range getSlice(sps, "seq_scaling_matrix") {
				sl := toIntSlice(rawList.([]any)) // nolint:errcheck
				if len(sl) > 0 {
					w.writeBits(1, 1) // seq_scaling_list_present_flag
				} else {
					w.writeBits(1, 0)
				}
				lastScale := 8
				for _, nextScale := range sl {
					delta := ((nextScale-lastScale+128)%256+256)%256 - 128
					w.writeSE(delta)
					lastScale = nextScale
				}
			}
		} else {
			w.writeBits(1, 0)
		}
	}

	w.writeUE(getInt(sps, "log2_max_frame_num") - 4)
	pocType := getInt(sps, "pic_order_cnt_type")
	w.writeUE(pocType)

	if pocType == 0 {
		w.writeUE(getInt(sps, "log2_max_pic_order_cnt_lsb") - 4)
	} else if pocType == 1 {
		w.writeBits(1, uint64(getInt(sps, "delta_pic_order_always_zero_flag")))
		w.writeSE(getInt(sps, "offset_for_non_ref_pic"))
		w.writeSE(getInt(sps, "offset_for_top_to_bottom_field"))
		offsets := getSlice(sps, "offsets_for_ref_frames")
		w.writeUE(len(offsets))
		for _, raw := range offsets {
			w.writeSE(toInt(raw))
		}
	}

	w.writeUE(getInt(sps, "max_num_ref_frames"))
	w.writeBits(1, uint64(getInt(sps, "gaps_in_frame_num_value_allowed_flag")))

	picSize := getMap(sps, "pic_size_in_mbs")
	frameMbsOnly := getInt(sps, "frame_mbs_only_flag")
	w.writeUE(getInt(picSize, "width") - 1)
	w.writeUE((getInt(picSize, "height") >> (1 - frameMbsOnly)) - 1)
	w.writeBits(1, uint64(frameMbsOnly))

	if frameMbsOnly == 0 {
		w.writeBits(1, uint64(getInt(sps, "mb_adaptive_frame_field_flag")))
	}

	w.writeBits(1, uint64(getInt(sps, "direct_8x8_inference_flag")))

	if has(sps, "frame_crop_offsets") {
		w.writeBits(1, 1)
		fco := getMap(sps, "frame_crop_offsets")
		chromaFmtIdc := 1 // default
		if has(sps, "chroma_format_idc") {
			chromaFmtIdc = getInt(sps, "chroma_format_idc")
		}
		shiftX := 0
		if chromaFmtIdc == 1 || chromaFmtIdc == 2 {
			shiftX = 1
		}
		shiftY := 0
		if chromaFmtIdc == 1 {
			shiftY = 1
		}
		shiftY += 1 - frameMbsOnly
		w.writeUE(getInt(fco, "left") >> shiftX)
		w.writeUE(getInt(fco, "right") >> shiftX)
		w.writeUE(getInt(fco, "top") >> shiftY)
		w.writeUE(getInt(fco, "bottom") >> shiftY)
	} else {
		w.writeBits(1, 0)
	}

	if has(sps, "vui_parameters") {
		w.writeBits(1, 1)
		genVUIParameters(w, sps, getMap(sps, "vui_parameters"))
	} else {
		w.writeBits(1, 0)
	}
}

// ---------------------------------------------------------------------------
// genPPS
// ---------------------------------------------------------------------------

func genPPS(w *bitWriter, pps map[string]any) {
	w.writeUE(getInt(pps, "pic_parameter_set_id"))
	w.writeBits(1, 1) // seq_parameter_set_id = 0
	w.writeBits(1, uint64(getInt(pps, "entropy_coding_mode_flag")))
	w.writeBits(1, uint64(getInt(pps, "bottom_field_pic_order_in_frame_present_flag")))
	w.writeBits(1, 1) // num_slice_groups = 0 (UE "1")

	nrida := getMap(pps, "num_ref_idx_default_active")
	w.writeUE(getInt(nrida, "l0") - 1)
	w.writeUE(getInt(nrida, "l1") - 1)
	w.writeBits(1, uint64(getInt(pps, "weighted_pred_flag")))
	w.writeBits(2, uint64(getInt(pps, "weighted_bipred_idc")))
	w.writeSE(getInt(pps, "pic_init_qp") - 26)
	w.writeBits(1, 1) // pic_init_qs = 0 (SE "1")
	w.writeSE(getInt(pps, "chroma_qp_index_offset"))
	w.writeBits(1, uint64(getInt(pps, "deblocking_filter_control_present_flag")))
	w.writeBits(1, uint64(getInt(pps, "constrained_intra_pred_flag")))
	w.writeBits(1, 0) // redundant_pic_cnt_present_flag

	if has(pps, "transform_8x8_mode_flag") {
		w.writeBits(1, uint64(getInt(pps, "transform_8x8_mode_flag")))
		if has(pps, "pic_scaling_matrix") {
			w.writeBits(1, 1)
			for _, rawList := range getSlice(pps, "pic_scaling_matrix") {
				sl := toIntSlice(rawList.([]any)) // nolint:errcheck
				if len(sl) > 0 {
					w.writeBits(1, 1)
				} else {
					w.writeBits(1, 0)
				}
				lastScale := 8
				for _, nextScale := range sl {
					delta := ((nextScale-lastScale+128)%256+256)%256 - 128
					w.writeSE(delta)
					lastScale = nextScale
				}
			}
		} else {
			w.writeBits(1, 0)
		}
		w.writeSE(getInt(pps, "second_chroma_qp_index_offset"))
	}
}

// ---------------------------------------------------------------------------
// genAUD
// ---------------------------------------------------------------------------

func genAUD(w *bitWriter, aud map[string]any) {
	w.writeBits(3, uint64(getInt(aud, "primary_pic_type")))
}

// ---------------------------------------------------------------------------
// genPrefixNAL
// ---------------------------------------------------------------------------

func genPrefixNAL(w *bitWriter, out io.Writer, nal map[string]any) {
	w.writeBits(1, 0) // svc_extension_flag
	w.writeBits(1, uint64(getInt(nal, "non_idr_flag")))
	w.writeBits(6, uint64(getInt(nal, "priority_id")))
	w.writeBits(10, uint64(getInt(nal, "view_id")))
	w.writeBits(3, uint64(getInt(nal, "temporal_id")))
	w.writeBits(1, uint64(getInt(nal, "anchor_pic_flag")))
	w.writeBits(1, uint64(getInt(nal, "inter_view_flag")))
	w.writeBits(1, 1) // reserved_one_bit

	if getInt(nal, "nal_unit_type") != 14 {
		genSliceLayer(w, out, nal)
	}
}

// ---------------------------------------------------------------------------
// genMVCVUIParameters
// ---------------------------------------------------------------------------

func genMVCVUIParameters(w *bitWriter, vui map[string]any) {
	ops := getSlice(vui, "vui_mvc_operation_points")
	w.writeUE(len(ops) - 1)
	for _, rawOp := range ops {
		op := asMap(rawOp)
		w.writeBits(3, uint64(getInt(op, "temporal_id")))
		views := getSlice(op, "target_views")
		w.writeUE(len(views) - 1)
		for _, rawView := range views {
			w.writeUE(toInt(rawView))
		}
		if has(vui, "num_units_in_tick") {
			w.writeBits(1, 1)
			w.writeBits(32, uint64(getInt(vui, "num_units_in_tick")))
			w.writeBits(32, uint64(getInt(vui, "time_scale")))
			w.writeBits(1, uint64(getInt(vui, "fixed_frame_rate_flag")))
		} else {
			w.writeBits(1, 0)
		}
		if has(vui, "nal_hrd_parameters") {
			w.writeBits(1, 1)
			genHRDParameters(w, getMap(vui, "nal_hrd_parameters"))
		} else {
			w.writeBits(1, 0)
		}
		if has(vui, "vcl_hrd_parameters") {
			w.writeBits(1, 1)
			genHRDParameters(w, getMap(vui, "vcl_hrd_parameters"))
		} else {
			w.writeBits(1, 0)
		}
		if has(vui, "nal_hrd_parameters") || has(vui, "vcl_hrd_parameters") {
			w.writeBits(1, uint64(getInt(vui, "low_delay_hrd_flag")))
		}
		w.writeBits(1, uint64(getInt(vui, "pic_struct_present_flag")))
	}
}

// ---------------------------------------------------------------------------
// genSubsetSPS
// ---------------------------------------------------------------------------

func genSubsetSPS(w *bitWriter, ssps map[string]any) {
	genSPS(w, ssps)

	profileIdc := getInt(ssps, "profile_idc")
	if profileIdc == 118 || profileIdc == 128 || profileIdc == 134 {
		w.writeBits(1, 1) // bit_equal_to_one
		w.writeUE(1)      // num_views_minus1
		viewIDs := getIntSlice(ssps, "view_ids")
		w.writeUE(viewIDs[0])
		w.writeUE(viewIDs[1])

		anchorRefs := getMap(ssps, "num_anchor_refs")
		w.writeUE(getInt(anchorRefs, "l0"))
		if getInt(anchorRefs, "l0") != 0 {
			w.writeUE(viewIDs[0])
		}
		w.writeUE(getInt(anchorRefs, "l1"))
		if getInt(anchorRefs, "l1") != 0 {
			w.writeUE(viewIDs[0])
		}

		nonAnchorRefs := getMap(ssps, "num_non_anchor_refs")
		w.writeUE(getInt(nonAnchorRefs, "l0"))
		if getInt(nonAnchorRefs, "l0") != 0 {
			w.writeUE(viewIDs[0])
		}
		w.writeUE(getInt(nonAnchorRefs, "l1"))
		if getInt(nonAnchorRefs, "l1") != 0 {
			w.writeUE(viewIDs[0])
		}

		lvs := getSlice(ssps, "level_values_signalled")
		w.writeUE(len(lvs) - 1)
		for _, rawLevel := range lvs {
			level := asMap(rawLevel)
			levelIdc := int(math.Round(getFloat(level, "idc") * 10))
			w.writeBits(8, uint64(levelIdc))
			ops := getSlice(level, "operation_points")
			w.writeUE(len(ops) - 1)
			for _, rawOp := range ops {
				op := asMap(rawOp)
				w.writeBits(3, uint64(getInt(op, "temporal_id")))
				views := getSlice(op, "target_views")
				w.writeUE(len(views) - 1)
				for _, rawView := range views {
					w.writeUE(toInt(rawView))
				}
				w.writeUE(getInt(op, "num_views") - 1)
			}
		}

		if has(ssps, "mvc_vui_parameters") {
			w.writeBits(1, 1)
			genMVCVUIParameters(w, getMap(ssps, "mvc_vui_parameters"))
		} else {
			w.writeBits(1, 0)
		}
	}

	w.writeBits(1, 0) // additional_extension2_flag
}

// ---------------------------------------------------------------------------
// NAL type → generator dispatch table.
// ---------------------------------------------------------------------------

type genFunc func(w *bitWriter, out io.Writer, nal map[string]any)

func wrapNoIO(fn func(w *bitWriter, nal map[string]any)) genFunc {
	return func(w *bitWriter, out io.Writer, nal map[string]any) {
		fn(w, nal)
	}
}

var genBits = map[int]genFunc{
	1:  func(w *bitWriter, out io.Writer, nal map[string]any) { genSliceLayer(w, out, nal) },
	5:  func(w *bitWriter, out io.Writer, nal map[string]any) { genSliceLayer(w, out, nal) },
	6:  wrapNoIO(genSEI),
	7:  wrapNoIO(func(w *bitWriter, nal map[string]any) { genSPS(w, nal) }),
	8:  wrapNoIO(func(w *bitWriter, nal map[string]any) { genPPS(w, nal) }),
	9:  wrapNoIO(func(w *bitWriter, nal map[string]any) { genAUD(w, nal) }),
	14: func(w *bitWriter, out io.Writer, nal map[string]any) { genPrefixNAL(w, out, nal) },
	15: wrapNoIO(func(w *bitWriter, nal map[string]any) { genSubsetSPS(w, nal) }),
	20: func(w *bitWriter, out io.Writer, nal map[string]any) { genPrefixNAL(w, out, nal) },
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s input.yaml output.264\n", os.Args[0])
		os.Exit(1)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	var nals []map[string]any
	err = yaml.Unmarshal(data, &nals)
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Create(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}

	for _, nal := range nals {
		if !has(nal, "nal_ref_idc") {
			continue
		}

		if _, writeErr := f.Write([]byte{0, 0, 0, 1}); writeErr != nil {
			log.Fatal(writeErr)
		}

		var w bitWriter
		w.writeBits(1, 0) // forbidden_zero_bit
		w.writeBits(2, uint64(getInt(nal, "nal_ref_idc")))
		nalUnitType := getInt(nal, "nal_unit_type")
		w.writeBits(5, uint64(nalUnitType))

		if gen, ok := genBits[nalUnitType]; ok {
			gen(&w, f, nal)
		}

		// RBSP trailing bits for applicable NAL types.
		if (1<<nalUnitType)&0b1110011011001111111110 != 0 {
			w.writeBits(1, 1)
		}

		// Pad to byte boundary and write.
		totalBits := w.bitLen()
		if totalBits%8 != 0 {
			w.writeBits(8-totalBits%8, 0)
		}
		if _, err := f.Write(w.finalize()); err != nil {
			log.Fatal(err)
		}
	}

	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Small helpers.
// ---------------------------------------------------------------------------

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case uint64:
		return int(x)
	case float64:
		return int(x)
	}
	log.Fatalf("toInt: unexpected type %T for value %v", v, v)
	return 0
}

func toIntSlice(s []any) []int {
	out := make([]int, len(s))
	for i, v := range s {
		out[i] = toInt(v)
	}
	return out
}

func intIn(v int, s []int) bool {
	return slices.Contains(s, v)
}

func rangeSlice(lo, hi int) []int {
	s := make([]int, hi-lo)
	for i := range s {
		s[i] = lo + i
	}
	return s
}
