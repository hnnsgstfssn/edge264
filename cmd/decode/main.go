package main

import (
	"fmt"
	"image/jpeg"
	"os"

	"github.com/hnnsgstfssn/edge264"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <file.264> [output.jpg]\n", os.Args[0])
		os.Exit(1)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}

	dec, err := edge264.NewDecoder()
	if err != nil {
		fmt.Fprintf(os.Stderr, "decoder: %v\n", err)
		os.Exit(1)
	}

	frames, err := dec.Decode(data)
	if err != nil {
		if closeErr := dec.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "close decoder: %v\n", closeErr)
		}
		fmt.Fprintf(os.Stderr, "decode: %v\n", err)
		os.Exit(1)
	}

	flushed, err := dec.Flush()
	if err != nil {
		fmt.Fprintf(os.Stderr, "flush: %v\n", err)
	}
	frames = append(frames, flushed...)

	fmt.Printf("decoded %d frames\n", len(frames))
	for i, f := range frames {
		fmt.Printf("  frame %d: %dx%d %v\n", i, f.Rect.Dx(), f.Rect.Dy(), f.SubsampleRatio)
	}

	// Write first frame as JPEG if output path given.
	if len(os.Args) >= 3 && len(frames) > 0 {
		out, err := os.Create(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "create: %v\n", err)
			if closeErr := dec.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "close decoder: %v\n", closeErr)
			}
			os.Exit(1)
		}
		err = jpeg.Encode(out, frames[0], &jpeg.Options{Quality: 90})
		if err != nil {
			if closeErr := out.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "close output: %v\n", closeErr)
			}
			if closeErr := dec.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "close decoder: %v\n", closeErr)
			}
			fmt.Fprintf(os.Stderr, "encode jpeg: %v\n", err)
			os.Exit(1)
		}
		if closeErr := out.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "close output: %v\n", closeErr)
		}
		fmt.Printf("wrote %s\n", os.Args[2])
	}

	if closeErr := dec.Close(); closeErr != nil {
		fmt.Fprintf(os.Stderr, "close decoder: %v\n", closeErr)
	}
}
