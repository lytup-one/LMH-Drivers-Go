package modbus

import (
	"errors"
	"testing"
	"time"

	"lmhcan/internal/serialport"
)

func slaveFake(slave byte, regs map[uint16]uint16) *serialport.Fake {
	return &serialport.Fake{Respond: func(req []byte) []byte {
		s, start, n, ok := ParseRequest(req)
		if !ok || s != slave {
			return nil // CRC error / wrong address: a real slave stays silent
		}
		out := make([]uint16, n)
		for i := range out {
			v, ok := regs[start+uint16(i)]
			if !ok {
				return BuildException(slave, 2)
			}
			out[i] = v
		}
		return BuildReply(slave, out)
	}}
}

func client(f *serialport.Fake) *Client {
	return &Client{P: f, Timeout: 50 * time.Millisecond, Gap: time.Millisecond}
}

func TestCRCKnownVector(t *testing.T) {
	// Standard example: 01 03 00 00 00 0A -> CRC bytes C5 CD (little endian 0xCDC5).
	if got := CRC16([]byte{1, 3, 0, 0, 0, 10}); got != 0xCDC5 {
		t.Errorf("CRC16 = %#04x, want 0xcdc5", got)
	}
	if CRC16(BuildReply(1, []uint16{1, 2, 3})) != 0 {
		t.Error("CRC over a frame including its CRC must be 0")
	}
}

func TestReadHolding(t *testing.T) {
	f := slaveFake(1, map[uint16]uint16{0: 0x0102, 1: 0xFFFE, 2: 7})
	got, err := client(f).ReadHolding(1, 0, 3)
	if err != nil || len(got) != 3 || got[0] != 0x0102 || got[1] != 0xFFFE || got[2] != 7 {
		t.Fatalf("got %v, %v", got, err)
	}
	if want := []byte{1, 3, 0, 0, 0, 3, 0x05, 0xCB}; string(f.Writes[0]) != string(want) {
		t.Errorf("request = % x, want % x", f.Writes[0], want)
	}
}

func TestErrors(t *testing.T) {
	f := slaveFake(1, map[uint16]uint16{0: 1})
	c := client(f)

	if _, err := c.ReadHolding(2, 0, 1); !errors.Is(err, ErrNoReply) {
		t.Errorf("wrong address: %v, want ErrNoReply", err)
	}
	_, err := c.ReadHolding(1, 100, 1)
	var ex *Exception
	if !errors.As(err, &ex) || ex.Code != 2 {
		t.Errorf("exception: %v", err)
	}

	bad := &serialport.Fake{Respond: func([]byte) []byte {
		r := BuildReply(1, []uint16{9})
		r[3] ^= 0xFF // corrupt a data byte
		return r
	}}
	if _, err := client(bad).ReadHolding(1, 0, 1); !errors.Is(err, ErrBadFrame) {
		t.Errorf("corrupt frame: %v, want ErrBadFrame", err)
	}
	short := &serialport.Fake{Respond: func([]byte) []byte { return BuildReply(1, []uint16{9, 9})[:5] }}
	if _, err := client(short).ReadHolding(1, 0, 2); !errors.Is(err, ErrBadFrame) {
		t.Errorf("truncated frame: %v, want ErrBadFrame", err)
	}
	if _, err := c.ReadHolding(1, 0, 0); err == nil {
		t.Error("count 0 must be rejected")
	}
}
