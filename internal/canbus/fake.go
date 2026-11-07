package canbus

import (
	"context"
	"sync"
)

// Fake is an in-memory Bus for tests: push frames into C, read what was sent.
type Fake struct {
	C    chan Frame
	mu   sync.Mutex
	sent []Frame
}

func NewFake() *Fake { return &Fake{C: make(chan Frame, 256)} }

func (f *Fake) Recv(ctx context.Context) (Frame, error) {
	select {
	case fr := <-f.C:
		return fr, nil
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}

func (f *Fake) Send(fr Frame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fr)
	return nil
}

// Sent returns the frames transmitted so far.
func (f *Fake) Sent() []Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Frame(nil), f.sent...)
}

func (f *Fake) Close() error { return nil }
