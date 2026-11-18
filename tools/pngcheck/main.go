// pngcheck asserts basic properties of a rendered PNG: dimensions and a
// minimum number of distinct colours. Used by tools/smoke.sh so a blank or
// wrongly-sized snapshot fails the build without storing fragile goldens.
package main

import (
	"flag"
	"fmt"
	"image"
	_ "image/png"
	"os"
)

func main() {
	wantW := flag.Int("w", 0, "expected width (0 to skip)")
	wantH := flag.Int("h", 0, "expected height (0 to skip)")
	minColors := flag.Int("colors", 2, "minimum number of distinct colours")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: pngcheck [-w N] [-h N] [-colors N] file.png")
		os.Exit(2)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pngcheck:", err)
		os.Exit(1)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pngcheck:", err)
		os.Exit(1)
	}
	b := img.Bounds()
	if *wantW != 0 && b.Dx() != *wantW {
		fmt.Fprintf(os.Stderr, "pngcheck: width %d, want %d\n", b.Dx(), *wantW)
		os.Exit(1)
	}
	if *wantH != 0 && b.Dy() != *wantH {
		fmt.Fprintf(os.Stderr, "pngcheck: height %d, want %d\n", b.Dy(), *wantH)
		os.Exit(1)
	}
	seen := map[uint32]struct{}{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, a := img.At(x, y).RGBA()
			seen[r>>8<<24|g>>8<<16|bb>>8<<8|a>>8] = struct{}{}
		}
	}
	if len(seen) < *minColors {
		fmt.Fprintf(os.Stderr, "pngcheck: %d colours, want at least %d\n", len(seen), *minColors)
		os.Exit(1)
	}
	fmt.Printf("pngcheck: %dx%d, %d colours\n", b.Dx(), b.Dy(), len(seen))
}
