package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

var (
	dviMsg string
	dviErr bool
)

// dviPanel shows the last complete frame decoded from the HSTX command stream.
func dviPanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("DVI", FontSize(16), FontWeight(700))
			Label(snap.Package, FontSize(12))
			if snap.DVI != nil {
				b := snap.DVI.Bounds()
				Label(fmt.Sprintf("%dx%d", b.Dx(), b.Dy()), FontSize(12))
			}
			Filler(1)
			if webFileOutput() {
				if Button(NoIcon, "Save PNG") {
					if snap.DVI == nil {
						dviMsg, dviErr = "no frame captured yet", true
					} else if path, err := saveDVIPNG(snap.DVI); err != nil {
						dviMsg, dviErr = "save failed: "+err.Error(), true
					} else {
						dviMsg, dviErr = "saved "+path, false
					}
					RequestNextFrame()
				}
			}
			Label("decoded from the HSTX command stream", FontSize(11), TextColor(220, 6, 60, 1))
		})
		if dviMsg != "" {
			c := TextColor(140, 45, 72, 1)
			if dviErr {
				c = TextColor(0, 65, 60, 1)
			}
			Label(dviMsg, FontSize(11), FontWeight(600), c)
		}
		Container(Attrs(Grow(1), Expand, Center, Clip), func() {
			if snap.DVI == nil {
				Label("waiting for the first DVI frame (run a Pico DVI demo on GP12-19)",
					FontSize(13), TextColor(220, 6, 62, 1))
				return
			}
			b := snap.DVI.Bounds()
			aspect := float32(b.Dx()) / float32(b.Dy())
			w := float32(viewW) * 0.85
			h := w / aspect
			if maxH := float32(viewH) * 0.72; h > maxH {
				h = maxH
				w = h * aspect
			}
			id := UseImage("dvi", snap.DVI)
			ImageViewAt(id, Vec2{w, h})
		})
	})
}

func saveDVIPNG(img *image.RGBA) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	board := "dvi"
	if selectedBoard != nil {
		board = selectedBoard.ID
	}
	name := fmt.Sprintf("dvi-%s-%s.png", time.Now().Format("20060102-150405"), board)
	path := filepath.Join(wd, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	err = png.Encode(f, img)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return path, nil
}
