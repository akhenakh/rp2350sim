//go:build js

package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"go.hasen.dev/shirei"
)

// webFirmware keeps the last image the page loaded so the toolbar's Load
// button can restart it: in the browser there is no path to read again.
var webFirmware struct {
	name string
	data []byte
}

// webInstallFirmware swaps in an image chosen or dropped by the user.
func webInstallFirmware(name string, data []byte) {
	if err := emu.LoadBytes(name, data, true); err != nil {
		// LoadBytes records the error; the toolbar shows it.
		shirei.RequestNextFrame()
		return
	}
	webFirmware.name, webFirmware.data = name, data
	fwPath = name
	flashMode = false
	shirei.RequestNextFrame()
}

// webFileInput is the hidden <input type=file> the Open… button drives. It is
// created once so the same element handles every pick and tests can fill it.
var webFileInput js.Value

// webInstallFilePicker creates the hidden input and wires its change handler.
func webInstallFilePicker() {
	if !webFileInput.IsUndefined() {
		return
	}
	doc := js.Global().Get("document")
	input := doc.Call("createElement", "input")
	input.Set("type", "file")
	input.Set("accept", ".elf,.uf2,.bin")
	input.Set("style", "display:none")
	doc.Get("body").Call("appendChild", input)
	webFileInput = input

	var change js.Func
	change = js.FuncOf(func(js.Value, []js.Value) any {
		files := input.Get("files")
		if files.Length() == 0 {
			return nil
		}
		file := files.Index(0)
		name := file.Get("name").String()
		var read js.Func
		read = js.FuncOf(func(_ js.Value, args []js.Value) any {
			defer read.Release()
			raw := args[0]
			data := make([]byte, raw.Get("byteLength").Int())
			js.CopyBytesToGo(data, js.Global().Get("Uint8Array").New(raw))
			input.Set("value", "")
			webInstallFirmware(name, data)
			return nil
		})
		file.Call("arrayBuffer").Call("then", read)
		return nil
	})
	input.Call("addEventListener", "change", change)
	webFilePickerChange = change
}

// webFilePickerChange keeps the change callback alive for the page lifetime.
var webFilePickerChange js.Func

// webOpenFirmware opens the browser's file picker. It reports whether the
// browser handled the request; the desktop Shirei file browser runs otherwise.
func webOpenFirmware() bool {
	webInstallFilePicker()
	webFileInput.Call("click")
	return true
}

// webReloadFirmware restarts the last picked image; false lets the desktop path
// re-read the path from disk.
func webReloadFirmware() bool {
	if webFirmware.data == nil {
		return false
	}
	webInstallFirmware(webFirmware.name, webFirmware.data)
	return true
}

// webInstallDrop accepts a firmware dropped anywhere on the page. It uses the
// same install path as the picker.
func webInstallDrop() {
	doc := js.Global().Get("document")
	var drop js.Func
	drop = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		ev.Call("preventDefault")
		dt := ev.Get("dataTransfer")
		if dt.IsUndefined() || dt.IsNull() {
			return nil
		}
		files := dt.Get("files")
		if files.Length() == 0 {
			return nil
		}
		file := files.Index(0)
		name := file.Get("name").String()
		var read js.Func
		read = js.FuncOf(func(_ js.Value, args []js.Value) any {
			defer read.Release()
			raw := args[0]
			data := make([]byte, raw.Get("byteLength").Int())
			js.CopyBytesToGo(data, js.Global().Get("Uint8Array").New(raw))
			webInstallFirmware(name, data)
			return nil
		})
		file.Call("arrayBuffer").Call("then", read)
		return nil
	})
	stop := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			args[0].Call("preventDefault")
		}
		return nil
	})
	doc.Call("addEventListener", "dragover", stop)
	doc.Call("addEventListener", "drop", drop)
}

// webHostTools reports whether host-only tools are available. The browser has
// no listening sockets (GDB) and no process execution (the Build drawer), so
// those toolbar buttons are not offered.
func webHostTools() bool { return false }

// webBoot reads the demo configuration from the page URL:
//
//	index.html?board=touch-lcd2&fw=firmware/touchlcd.elf&tab=lcd
//
// The defaults keep the generated page useful with no query string. It returns
// false on a desktop build so the command line keeps working unchanged.
func webBoot() (boardID, firmware, tab string, ok bool) {
	loc := js.Global().Get("location")
	if loc.IsUndefined() || loc.IsNull() {
		return "", "", "", false
	}
	q, err := url.ParseQuery(strings.TrimPrefix(loc.Get("search").String(), "?"))
	if err != nil {
		return "", "", "", false
	}
	boardID = q.Get("board")
	if boardID == "" {
		boardID = "geek"
	}
	firmware = q.Get("fw")
	if firmware == "" {
		firmware = "firmware/lcd.elf"
	}
	webInstallDrop()
	webInstallFilePicker()
	return boardID, firmware, q.Get("tab"), true
}

// webPrepareBoard strips the host-only parts of a board definition: the browser
// has no filesystem directory backing and no listening sockets.
func webPrepareBoard(b *BoardDef) {
	if b == nil {
		return
	}
	if b.SD != nil {
		b.SD.Dir = ""
	}
	b.Serial = nil
	b.I2CTCP = nil
	b.SPITCP = nil
	b.Ethernet = nil
	b.Wifi = nil
}

// webTurboDefault starts the browser demo unpaced: it cannot keep up with the
// 150 MHz system clock, so the red "slow" indicator would be permanent.
func webTurboDefault() bool { return true }

// webFileOutput is false in the browser: there is no writable filesystem, so
// Save WAV/PNG/trace/VCD would only report a failure. A Blob download would be
// the next step if those buttons are wanted there.
func webFileOutput() bool { return false }

// webYield returns to the browser event loop. Go's js runtime only hands
// control back to the host when every goroutine blocks, so the emulator loop
// must sleep instead of spinning or the page never paints.
func webYield() {
	time.Sleep(time.Millisecond)
}

// webChunk is the emulator step size. The browser pays a fixed cost per chunk
// (mutex, snapshot, event-loop yield), so it steps fewer, larger chunks than
// the desktop build.
func webChunk() uint64 { return 600_000 }

// webFetch loads a URL as bytes. The calling goroutine blocks while the browser
// fetch resolves; Go's js runtime returns to the page event loop whenever every
// goroutine is blocked, so this is safe before the UI starts.
func webFetch(url string) ([]byte, error) {
	fetch := js.Global().Get("fetch")
	if fetch.IsUndefined() {
		return nil, fmt.Errorf("fetch is not available")
	}
	var (
		once sync.Once
		data []byte
		err  error
	)
	done := make(chan struct{})
	finish := func() { once.Do(func() { close(done) }) }

	var body, buf, fail js.Func
	defer func() {
		body.Release()
		buf.Release()
		fail.Release()
	}()

	fail = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			err = fmt.Errorf("%s: %v", url, args[0])
		} else {
			err = fmt.Errorf("%s: fetch failed", url)
		}
		finish()
		return nil
	})
	buf = js.FuncOf(func(_ js.Value, args []js.Value) any {
		raw := args[0]
		data = make([]byte, raw.Get("byteLength").Int())
		js.CopyBytesToGo(data, js.Global().Get("Uint8Array").New(raw))
		finish()
		return nil
	})
	body = js.FuncOf(func(_ js.Value, args []js.Value) any {
		resp := args[0]
		if !resp.Get("ok").Bool() {
			err = fmt.Errorf("%s: HTTP %d", url, resp.Get("status").Int())
			finish()
			return nil
		}
		resp.Call("arrayBuffer").Call("then", buf).Call("catch", fail)
		return nil
	})

	fetch.Invoke(url).Call("then", body).Call("catch", fail)
	<-done
	return data, err
}
