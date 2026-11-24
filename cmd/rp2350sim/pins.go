package main

// chainPinCallbacks preserves board/device callbacks when the headless runner
// adds its own pin tracing or SPI-bridge observer.
func chainPinCallbacks(prev, next func(int, bool)) func(int, bool) {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(pin int, level bool) {
		prev(pin, level)
		next(pin, level)
	}
}
