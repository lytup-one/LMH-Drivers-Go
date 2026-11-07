// Package modbus is a minimal Modbus RTU master: read holding registers
// (function 0x03). It is the transport for RS485 devices that speak Modbus
// (the PACE BMS today, inverters and generators later).
package modbus

import (
	"errors"
	"fmt"
	"os"
	"time"

	"lmhcan/internal/serialport"
)

// Errors a read can return. Use errors.Is / errors.As.
var (
	ErrNoReply  = errors.New("no reply")
	ErrBadFrame = errors.New("bad frame")
)

// Exception is a Modbus exception response (illegal address, device busy, ...).
type Exception struct{ Code byte }

func (e *Exception) Error() string { return fmt.Sprintf("modbus exception %d", e.Code) }

// Client talks to devices on one serial port. It is not safe for concurrent use.
type Client struct {
	P       serialport.Port
	Timeout time.Duration // reply timeout, default 600 ms
	Gap     time.Duration // minimum idle time between frames, default 120 ms

	lastEnd time.Time
}

// CRC16 is the Modbus CRC (poly 0xA001, init 0xFFFF). The CRC of a frame that
// includes its own CRC bytes is 0.
func CRC16(b []byte) uint16 {
	c := uint16(0xFFFF)
	for _, x := range b {
		c ^= uint16(x)
		for i := 0; i < 8; i++ {
			if c&1 != 0 {
				c = c>>1 ^ 0xA001
			} else {
				c >>= 1
			}
		}
	}
	return c
}

// ReadHolding reads count holding registers (1..125) starting at start from
// the device with address slave.
func (c *Client) ReadHolding(slave byte, start, count uint16) ([]uint16, error) {
	if count < 1 || count > 125 {
		return nil, fmt.Errorf("register count %d out of range 1..125", count)
	}
	timeout, gap := c.Timeout, c.Gap
	if timeout == 0 {
		timeout = 600 * time.Millisecond
	}
	if gap == 0 {
		gap = 120 * time.Millisecond
	}
	if wait := gap - time.Since(c.lastEnd); wait > 0 {
		time.Sleep(wait)
	}

	req := []byte{slave, 0x03, byte(start >> 8), byte(start), byte(count >> 8), byte(count)}
	crc := CRC16(req)
	req = append(req, byte(crc), byte(crc>>8))
	if err := c.P.ResetInput(); err != nil {
		return nil, err
	}
	if _, err := c.P.Write(req); err != nil {
		return nil, err
	}

	want := 5 + 2*int(count)
	rsp := make([]byte, 0, want)
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 256)
	for len(rsp) < want {
		// An exception reply is 5 bytes: slave, func|0x80, code, crc.
		if len(rsp) >= 5 && rsp[1]&0x80 != 0 {
			break
		}
		if err := c.P.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		n, err := c.P.Read(buf)
		rsp = append(rsp, buf[:n]...)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			return nil, err
		}
		if n == 0 && time.Now().After(deadline) {
			break
		}
	}
	c.lastEnd = time.Now()

	if len(rsp) == 0 {
		return nil, ErrNoReply
	}
	if len(rsp) == 5 && rsp[1]&0x80 != 0 && CRC16(rsp) == 0 && rsp[0] == slave {
		return nil, &Exception{Code: rsp[2]}
	}
	if len(rsp) != want || CRC16(rsp) != 0 || rsp[0] != slave || rsp[1] != 0x03 || int(rsp[2]) != 2*int(count) {
		head := rsp
		if len(head) > 16 {
			head = head[:16]
		}
		return nil, fmt.Errorf("%w (%d of %d bytes): % x", ErrBadFrame, len(rsp), want, head)
	}
	out := make([]uint16, count)
	for i := range out {
		out[i] = uint16(rsp[3+2*i])<<8 | uint16(rsp[4+2*i])
	}
	return out, nil
}

// BuildReply builds a valid function-3 reply (for tests and fake slaves).
func BuildReply(slave byte, regs []uint16) []byte {
	b := []byte{slave, 0x03, byte(2 * len(regs))}
	for _, r := range regs {
		b = append(b, byte(r>>8), byte(r))
	}
	crc := CRC16(b)
	return append(b, byte(crc), byte(crc>>8))
}

// BuildException builds a valid exception reply.
func BuildException(slave, code byte) []byte {
	b := []byte{slave, 0x83, code}
	crc := CRC16(b)
	return append(b, byte(crc), byte(crc>>8))
}

// ParseRequest decodes a function-3 request frame (for fake slaves).
func ParseRequest(req []byte) (slave byte, start, count uint16, ok bool) {
	if len(req) != 8 || CRC16(req) != 0 || req[1] != 0x03 {
		return 0, 0, 0, false
	}
	return req[0], uint16(req[2])<<8 | uint16(req[3]), uint16(req[4])<<8 | uint16(req[5]), true
}
