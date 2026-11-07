package rs485

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"lmhcan/internal/model"
	"lmhcan/internal/serialport"
)

// ErrNoReply means the device did not answer a request.
var ErrNoReply = errors.New("no reply")

// Pylontech RS485 command codes (CID2) used here. All are read-only.
const (
	pylonCID1         = 0x46
	pylonAnalog       = 0x42
	pylonManufacturer = 0x51
	pylonLimits       = 0x92
	pylonSerial       = 0x93
)

// pylon polls a BMS that speaks the Pylontech RS485 protocol (VER 0x20).
// The BMS tested answers address 2 only. Frame layouts below were read from
// a capture of the LYTVOLT S15000 in Pylon mode (see testdata in the tests);
// 0x44 (alarm info) and 0x47 (system parameters) got no reply from it and
// are not used.
type pylon struct {
	p       serialport.Port
	adr     byte
	ver     byte
	timeout time.Duration
	gap     time.Duration
	lastEnd time.Time

	infoDone bool
	info     model.Battery // manufacturer / serial, read once
}

func newPylon(p serialport.Port, adr byte) *pylon {
	return &pylon{p: p, adr: adr, ver: 0x20, timeout: time.Second, gap: 100 * time.Millisecond}
}

func (q *pylon) request(cid2 byte, info []byte) (PylonFrame, error) {
	if wait := q.gap - time.Since(q.lastEnd); wait > 0 {
		time.Sleep(wait)
	}
	req := EncodePylon(PylonFrame{Ver: q.ver, Adr: q.adr, CID1: pylonCID1, CID2: cid2, Info: info})
	rsp, err := Exchange(q.p, req, q.timeout, q.gap, PylonComplete)
	q.lastEnd = time.Now()
	if err != nil {
		return PylonFrame{}, err
	}
	if len(rsp) == 0 {
		return PylonFrame{}, ErrNoReply
	}
	if i := bytes.IndexByte(rsp, pylonSOI); i > 0 { // drop line noise before the frame
		rsp = rsp[i:]
	}
	f, err := DecodePylon(rsp)
	if err != nil {
		return f, fmt.Errorf("cmd %02X: %w: %q", cid2, err, rsp)
	}
	if f.CID2 != 0 {
		return f, fmt.Errorf("cmd %02X: BMS returned error code %02X", cid2, f.CID2)
	}
	if f.Adr != q.adr {
		return f, fmt.Errorf("cmd %02X: reply from address %d, expected %d", cid2, f.Adr, q.adr)
	}
	return f, nil
}

func (q *pylon) Poll(b *model.Battery) error {
	f, err := q.request(pylonAnalog, []byte{q.adr})
	if err != nil {
		return err
	}
	nb := *b
	if err := decodePylonAnalog(f.Info, &nb); err != nil {
		return err
	}
	// The limits are best effort: a missed reply keeps the previous limits.
	if f, err := q.request(pylonLimits, []byte{q.adr}); err == nil {
		_ = decodePylonLimits(f.Info, &nb)
	}
	if !q.infoDone {
		ok := true
		if f, err := q.request(pylonManufacturer, nil); err == nil {
			decodePylonManufacturer(f.Info, &q.info)
		} else {
			ok = false
		}
		if f, err := q.request(pylonSerial, []byte{q.adr}); err == nil {
			decodePylonSerial(f.Info, &q.info)
		} else {
			ok = false
		}
		q.infoDone = ok
	}
	nb.Manufacturer, nb.ID, nb.Version, nb.Serial = q.info.Manufacturer, q.info.ID, q.info.Version, q.info.Serial
	*b = nb
	return nil
}

var errShort = errors.New("reply is shorter than its fields")

// reader walks the INFO bytes and remembers the first overrun.
type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || n > len(r.b) {
		r.err = errShort
		return make([]byte, n)
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}
func (r *reader) u8() byte    { return r.take(1)[0] }
func (r *reader) u16() uint16 { return binary.BigEndian.Uint16(r.take(2)) }
func (r *reader) i16() int16  { return int16(r.u16()) }

// decodePylonAnalog applies a 0x42 reply. Layout seen on the BMS:
//
//	flags(1) pack(1) M(1) M x cell mV(2) N(1) N x temp(2, 0.1 K)
//	current(2, signed, 0.1 A, + charging) voltage(2, mV) remaining(2, 10 mAh)
//	P(1) then P x 2-byte fields: total capacity (10 mAh), cycles, ...
//
// There is no SOC field: it is derived as remaining / total capacity.
func decodePylonAnalog(info []byte, b *model.Battery) error {
	r := &reader{b: info}
	r.u8() // flags
	r.u8() // pack number
	m := int(r.u8())
	cells := make([]int, 0, m)
	for i := 0; i < m; i++ {
		cells = append(cells, int(r.u16()))
	}
	n := int(r.u8())
	temps := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		temps = append(temps, round((float64(r.u16())-2731)/10, 1))
	}
	cur := r.i16()
	volt := r.u16()
	remaining := r.u16()
	p := int(r.u8())
	var fields []uint16
	for i := 0; i < p; i++ {
		fields = append(fields, r.u16())
	}
	if r.err != nil {
		return fmt.Errorf("analog values: %w (%d bytes)", r.err, len(info))
	}
	if len(fields) < 2 || fields[0] == 0 {
		return fmt.Errorf("analog values: no total capacity / cycle fields (P=%d)", p)
	}

	b.CellsMV, b.CellMinMV, b.CellMaxMV, b.CellDeltaMV = cells, nil, nil, nil
	if len(cells) > 0 {
		lo, hi := cells[0], cells[0]
		for _, c := range cells {
			if c < lo {
				lo = c
			}
			if c > hi {
				hi = c
			}
		}
		b.CellMinMV, b.CellMaxMV, b.CellDeltaMV = model.F(float64(lo)), model.F(float64(hi)), model.F(float64(hi-lo))
	}
	b.TempsC = temps
	b.CurrentA = model.F(round(float64(cur)/10, 1))
	b.VoltageV = model.F(round(float64(volt)/1000, 3))
	b.RemainingAh = model.F(round(float64(remaining)/100, 2))
	b.FullAh = model.F(round(float64(fields[0])/100, 2))
	b.Cycles = model.I(int(fields[1]))
	b.SOCPct = model.F(round(100*float64(remaining)/float64(fields[0]), 1))
	return nil
}

// decodePylonLimits applies a 0x92 reply:
//
//	pack(1) charge V limit(2, mV) discharge V limit(2, mV)
//	charge I limit(2, 0.1 A) discharge I limit(2, 0.1 A) flags(1)
//
// The flags byte is reported raw.
func decodePylonLimits(info []byte, b *model.Battery) error {
	r := &reader{b: info}
	r.u8()
	cv, dv, ci, di, fl := r.u16(), r.u16(), r.i16(), r.i16(), r.u8()
	if r.err != nil {
		return fmt.Errorf("charge/discharge info: %w", r.err)
	}
	b.ChargeVoltageLimitV = model.F(round(float64(cv)/1000, 3))
	b.DischargeVoltageLimitV = model.F(round(float64(dv)/1000, 3))
	b.ChargeCurrentLimitA = model.F(round(float64(ci)/10, 1))
	b.DischargeCurrentLimitA = model.F(round(float64(di)/10, 1))
	b.RequestFlagsRaw = fmt.Sprintf("%02X", fl)
	return nil
}

// decodePylonManufacturer applies a 0x51 reply: device name(10) version(2) manufacturer(20).
func decodePylonManufacturer(info []byte, b *model.Battery) {
	if len(info) < 32 {
		return
	}
	b.ID = strings.TrimRight(string(info[:10]), " \x00")
	b.Version = fmt.Sprintf("%d.%d", info[10], info[11])
	b.Manufacturer = strings.TrimRight(string(info[12:32]), " \x00")
}

// decodePylonSerial applies a 0x93 reply: pack(1) then up to 16 ASCII characters
// (the BMS's longer pack serial is cut at 16).
func decodePylonSerial(info []byte, b *model.Battery) {
	if len(info) > 1 {
		b.Serial = strings.TrimRight(string(info[1:]), " \x00")
	}
}
