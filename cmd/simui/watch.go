package main

import (
	"os"
	"sync"
	"time"
)

// firmwareWatcher polls a firmware or flash file and reloads the machine when
// it changes on disk. Polling is used instead of an OS notification library so
// the UI behaves the same on Linux, macOS and Windows and survives editors that
// replace files atomically. A change must stay stable for two intervals before
// the callback runs, so a linker writing the file in place is not caught
// half-finished.
type firmwareWatcher struct {
	// reloadMu serializes a path/mode change with an in-flight reload. Together
	// with generation it prevents a stale callback from republishing state after
	// the toolbar has selected another file.
	reloadMu   sync.Mutex
	mu         sync.Mutex
	path       string
	interval   time.Duration
	onChange   func(path string) error
	done       chan struct{}
	started    bool
	generation uint64

	fingerprint fileFingerprint
	have        bool
	pending     bool
	pendingFP   fileFingerprint
	stable      int

	reloads    int
	lastReload time.Time
	err        string
}

type fileFingerprint struct {
	size  int64
	mtime time.Time
	mode  os.FileMode
}

func newFirmwareWatcher() *firmwareWatcher {
	return &firmwareWatcher{
		interval: 200 * time.Millisecond,
		done:     make(chan struct{}),
	}
}

// watchFile points the shared watcher at path. mode is "firmware" for an
// ELF/UF2 image or "flash" for a raw flash backing file.
func watchFile(path, mode string) {
	if !watchEnabled || path == "" {
		return
	}
	if firmwareWatch == nil {
		firmwareWatch = newFirmwareWatcher()
	}
	firmwareWatch.Start()
	reload := func(p string) error { return emu.Reload(p) }
	if mode == "flash" {
		reload = func(p string) error { return emu.ReloadFlash(p) }
	}
	firmwareWatch.Watch(path, reload)
}

// Start begins polling. Watch must be called to set the target.
func (w *firmwareWatcher) Start() {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()
	go w.run()
}

// Watch points the watcher at path and resets the change baseline. reload is
// called with path after a stable change; its error is shown in the toolbar.
// The initial fingerprint is captured synchronously so a write immediately
// after Watch is observed as a change rather than silently becoming baseline.
func (w *firmwareWatcher) Watch(path string, reload func(string) error) {
	w.reloadMu.Lock()
	defer w.reloadMu.Unlock()
	fp, have := fingerprintFile(path)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.generation++
	w.path = path
	w.onChange = reload
	w.fingerprint = fp
	w.have = have
	w.pending = false
	w.stable = 0
	w.err = ""
}

func fingerprintFile(path string) (fileFingerprint, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return fileFingerprint{}, false
	}
	return fileFingerprint{size: info.Size(), mtime: info.ModTime(), mode: info.Mode()}, true
}

// Stop ends the polling goroutine.
func (w *firmwareWatcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		close(w.done)
		w.started = false
	}
}

// WatchStatus is the toolbar view of the watcher.
type WatchStatus struct {
	Path     string
	Reloads  int
	Last     time.Time
	Err      string
	Watching bool
	Interval time.Duration
}

func (w *firmwareWatcher) Status() WatchStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return WatchStatus{
		Path: w.path, Reloads: w.reloads, Last: w.lastReload,
		Err: w.err, Watching: w.started && w.path != "", Interval: w.interval,
	}
}

func (w *firmwareWatcher) run() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

func (w *firmwareWatcher) poll() {
	w.reloadMu.Lock()
	defer w.reloadMu.Unlock()

	w.mu.Lock()
	path := w.path
	generation := w.generation
	w.mu.Unlock()
	if path == "" {
		return
	}
	fp, have := fingerprintFile(path)
	if !have {
		return
	}

	w.mu.Lock()
	if generation != w.generation || path != w.path {
		w.mu.Unlock()
		return
	}
	if !w.have {
		w.fingerprint = fp
		w.have = true
		w.mu.Unlock()
		return
	}
	if fp == w.fingerprint {
		w.pending = false
		w.stable = 0
		w.mu.Unlock()
		return
	}
	if !w.pending || fp != w.pendingFP {
		w.pending = true
		w.pendingFP = fp
		w.stable = 0
		w.mu.Unlock()
		return
	}
	w.stable++
	if w.stable < 2 {
		w.mu.Unlock()
		return
	}
	reload := w.onChange
	w.fingerprint = fp
	w.pending = false
	w.stable = 0
	w.mu.Unlock()

	if reload == nil {
		return
	}
	if err := reload(path); err != nil {
		w.mu.Lock()
		if generation == w.generation {
			w.err = err.Error()
		}
		w.mu.Unlock()
		return
	}
	w.mu.Lock()
	if generation == w.generation {
		w.err = ""
		w.reloads++
		w.lastReload = time.Now()
	}
	w.mu.Unlock()
}
