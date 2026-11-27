//go:build js

package cyw43439

// newDataPath is a no-op in the browser build: gVisor is desktop-only.
func newDataPath(*Device) (dataPath, error) { return nil, nil }
