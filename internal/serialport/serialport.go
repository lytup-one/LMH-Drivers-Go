// Package serialport opens a serial / RS485 port with the standard library
// only (termios through ioctl). On the LMH the on-board RS485 ports are USB CDC
// devices (/dev/ttyACM*): the driver handles the RS485 direction in hardware,
// so no RS485 ioctl is used or needed.
package serialport

import (
	"errors"
	"fmt"
	"time"
)

// Port is a byte stream with a read deadline.
type Port interface {
	// Read returns os.ErrDeadlineExceeded (errors.Is) when the deadline passes
	// with no data.
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	SetReadDeadline(t time.Time) error
	// ResetInput discards bytes received but not yet read.
	ResetInput() error
	Close() error
}

// Config is the line format. Data bits are always 8.
type Config struct {
	Baud     int
	Parity   byte // 'N', 'E' or 'O'
	StopBits int  // 1 or 2
}

// Default is 9600 8N1.
func Default() Config { return Config{Baud: 9600, Parity: 'N', StopBits: 1} }

// ErrUnsupported is returned on platforms without serial support.
var ErrUnsupported = errors.New("serial ports are only supported on Linux")

// Validate checks the line format.
func (c Config) Validate() error {
	if _, ok := baudBits[c.Baud]; !ok {
		return fmt.Errorf("unsupported baud rate %d", c.Baud)
	}
	switch c.Parity {
	case 'N', 'E', 'O':
	default:
		return fmt.Errorf("parity %q must be N, E or O", string(c.Parity))
	}
	if c.StopBits != 1 && c.StopBits != 2 {
		return fmt.Errorf("stop bits %d must be 1 or 2", c.StopBits)
	}
	return nil
}

// Bauds lists the supported baud rates in ascending order.
func Bauds() []int { return []int{1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200} }

// baudBits holds the Linux termios speed constants (B1200 ... B115200).
var baudBits = map[int]uint32{
	1200: 0x9, 2400: 0xb, 4800: 0xc, 9600: 0xd,
	19200: 0xe, 38400: 0xf, 57600: 0x1001, 115200: 0x1002,
}
