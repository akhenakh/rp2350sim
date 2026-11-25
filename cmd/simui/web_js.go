//go:build js

package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"syscall/js"
	"time"
)

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
