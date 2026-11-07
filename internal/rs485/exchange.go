package rs485

import (
	"errors"
	"os"
	"time"

	"lmhcan/internal/serialport"
)

// Exchange sends req and returns everything the device sends back. It waits up
// to timeout for the first byte, then stops after gap of silence (or as soon as
// stop reports the reply complete, if stop is not nil). A device that stays
// silent gives (nil, nil): silence is a result, not an error.
func Exchange(p serialport.Port, req []byte, timeout, gap time.Duration, stop func([]byte) bool) ([]byte, error) {
	if err := p.ResetInput(); err != nil {
		return nil, err
	}
	if _, err := p.Write(req); err != nil {
		return nil, err
	}
	var rsp []byte
	buf := make([]byte, 512)
	deadline := time.Now().Add(timeout)
	for {
		if err := p.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		n, err := p.Read(buf)
		if n > 0 {
			rsp = append(rsp, buf[:n]...)
			deadline = time.Now().Add(gap)
			if stop != nil && stop(rsp) {
				return rsp, nil
			}
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				if len(rsp) > 0 || time.Now().After(deadline) {
					return rsp, nil
				}
				continue
			}
			return rsp, err
		}
	}
}

// PylonComplete reports whether b already holds a whole Pylon frame.
func PylonComplete(b []byte) bool { return len(b) > 0 && b[len(b)-1] == pylonEOI }

// PylonProbeRequest is one read-only request sent by `lmh-can probe`.
type PylonProbeRequest struct {
	Name string
	CID2 byte
	Pack bool // INFO carries the pack address
}

// PylonProbeRequests are the standard "get" commands of the Pylontech RS485
// protocol. None of them changes anything on the BMS.
var PylonProbeRequests = []PylonProbeRequest{
	{"protocol version", 0x4F, false},
	{"manufacturer info", 0x51, false},
	{"system parameters", 0x47, false},
	{"analog values", 0x42, true},
	{"alarm info", 0x44, true},
	{"charge/discharge management", 0x92, true},
	{"serial number", 0x93, true},
}

// BuildPylonProbe builds the request for the given version byte and address.
func BuildPylonProbe(r PylonProbeRequest, ver, adr byte) []byte {
	f := PylonFrame{Ver: ver, Adr: adr, CID1: 0x46, CID2: r.CID2}
	if r.Pack {
		f.Info = []byte{adr}
	}
	return EncodePylon(f)
}
