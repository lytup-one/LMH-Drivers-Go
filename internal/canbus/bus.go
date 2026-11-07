// Package canbus is the transport layer: it moves raw CAN frames in and out of
// the kernel (SocketCAN) and knows nothing about any device protocol.
package canbus

import (
	"context"
	"time"
)

// Frame is one classic CAN frame (or a kernel error frame).
type Frame struct {
	ID       uint32 // 11-bit or 29-bit identifier, without flag bits
	Extended bool   // true for 29-bit identifiers
	Data     []byte // 0..8 bytes
	Time     time.Time
	Error    bool // kernel error frame (bus error, bus-off, ...); Data holds the error detail
}

// Bus is a source of CAN frames. Implementations: the SocketCAN bus on the LMH
// and a Fake bus for tests.
type Bus interface {
	// Recv blocks until a frame arrives or ctx is done (then it returns ctx.Err()).
	Recv(ctx context.Context) (Frame, error)
	// Send transmits a frame. The monitoring engine never calls it; it exists for
	// polled protocols that are added later.
	Send(f Frame) error
	Close() error
}
