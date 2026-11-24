package main

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestFirmwareWatcherReloadsOnStableChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fw.elf")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newFirmwareWatcher()
	w.interval = 20 * time.Millisecond
	got := make(chan string, 4)
	w.Watch(path, func(p string) error {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		got <- string(data)
		return nil
	})
	w.Start()
	defer w.Stop()

	// Let the watcher take its baseline. No callback should fire.
	time.Sleep(100 * time.Millisecond)
	select {
	case v := <-got:
		t.Fatalf("unexpected reload before any change: %q", v)
	default:
	}

	if err := os.WriteFile(path, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v != "two" {
			t.Fatalf("reloaded %q, want two", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not reload after a stable change")
	}
	if st := w.Status(); st.Reloads != 1 || st.Err != "" {
		t.Fatalf("status = %+v", st)
	}

	// A second change reloads again.
	time.Sleep(60 * time.Millisecond)
	if err := os.WriteFile(path, []byte("three"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v != "three" {
			t.Fatalf("second reload = %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not reload a second change")
	}
}

func TestFirmwareWatcherCapturesBaselineAtWatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fw.elf")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newFirmwareWatcher()
	calls := 0
	w.Watch(path, func(string) error { calls++; return nil })
	if err := os.WriteFile(path, []byte("two-long"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.poll() // observe the new fingerprint
	w.poll() // first stable poll
	w.poll() // second stable poll
	if calls != 1 {
		t.Fatalf("reload calls = %d, want 1", calls)
	}
}

func TestFirmwareWatcherSerializesPathChanges(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.elf")
	second := filepath.Join(dir, "second.elf")
	if err := os.WriteFile(first, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newFirmwareWatcher()
	entered := make(chan struct{})
	release := make(chan struct{})
	w.Watch(first, func(string) error {
		close(entered)
		<-release
		return nil
	})
	if err := os.WriteFile(first, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	pollDone := make(chan struct{})
	go func() {
		w.poll()
		w.poll()
		w.poll()
		close(pollDone)
	}()
	<-entered

	secondCalls := 0
	watchDone := make(chan struct{})
	go func() {
		w.Watch(second, func(string) error { secondCalls++; return nil })
		close(watchDone)
	}()
	close(release)
	<-pollDone
	<-watchDone
	if secondCalls != 0 {
		t.Fatalf("path change replayed the new baseline %d times", secondCalls)
	}
	if err := os.WriteFile(second, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.poll()
	w.poll()
	w.poll()
	if secondCalls != 1 {
		t.Fatalf("new path reload calls = %d, want 1", secondCalls)
	}
}

func TestFirmwareWatcherKeepsWatchingAfterError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fw.elf")
	if err := os.WriteFile(path, []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newFirmwareWatcher()
	w.interval = 20 * time.Millisecond
	var fail atomic.Bool
	fail.Store(true)
	done := make(chan struct{}, 4)
	w.Watch(path, func(string) error {
		if fail.Load() {
			return os.ErrInvalid
		}
		done <- struct{}{}
		return nil
	})
	w.Start()
	defer w.Stop()
	time.Sleep(80 * time.Millisecond)

	if err := os.WriteFile(path, []byte("another"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := w.Status(); st.Err != "" {
			fail.Store(false)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := w.Status(); st.Err == "" {
		t.Fatal("watcher did not report the reload error")
	}

	// The next change must retry and clear the error.
	if err := os.WriteFile(path, []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not retry after an error")
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if st := w.Status(); st.Err == "" && st.Reloads == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("status after recovery = %+v", w.Status())
}
