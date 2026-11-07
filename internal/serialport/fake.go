package serialport

import (
	"bytes"
	"os"
	"sync"
	"time"
)

// Fake is an in-memory port for tests. Each Write is passed to Respond; its
// return value (if any) becomes readable. Feed adds bytes directly (for
// sniffing tests). When nothing is readable, Read returns os.ErrDeadlineExceeded
// immediately.
type Fake struct {
	Respond func(req []byte) []byte

	mu     sync.Mutex
	in     bytes.Buffer
	Writes [][]byte
	Closed bool
}

func (f *Fake) Feed(b []byte) {
	f.mu.Lock()
	f.in.Write(b)
	f.mu.Unlock()
}

func (f *Fake) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.in.Len() == 0 {
		return 0, os.ErrDeadlineExceeded
	}
	return f.in.Read(p)
}

func (f *Fake) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Writes = append(f.Writes, append([]byte(nil), p...))
	if f.Respond != nil {
		f.in.Write(f.Respond(p))
	}
	return len(p), nil
}

func (f *Fake) SetReadDeadline(time.Time) error { return nil }
func (f *Fake) ResetInput() error               { f.mu.Lock(); f.in.Reset(); f.mu.Unlock(); return nil }
func (f *Fake) Close() error                    { f.Closed = true; return nil }
