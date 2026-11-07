//go:build !linux

package serialport

// Open is not available on this platform.
func Open(path string, c Config) (Port, error) { return nil, ErrUnsupported }

// List returns nothing on this platform.
func List() []string { return nil }
