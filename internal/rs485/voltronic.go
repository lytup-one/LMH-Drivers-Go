package rs485

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
	"lmhcan/internal/serialport"
)

// Voltronic Power "Inverter and BMS 485 communication Protocol" (ACE BMS, 2020).
//
// The framing looks like Modbus RTU but is not: the request is
//
//	slave(1) 0x03 start(2) length(2, in 16-bit words) crc(2, low byte first)
//
// and the reply carries a 2-byte word count where Modbus has a 1-byte byte
// count:
//
//	slave(1) 0x03 length(2, words) data(2 x length) crc(2, low byte first)
//
// so modbus.Client cannot parse it. An error reply is slave, 0x83, code, crc.
// Addresses are 16-bit words; a "4 byte" parameter takes two addresses,
// big-endian. Slave 1 is the common address every BMS answers.
//
// Register map (from the document; never checked against a real BMS yet):
//
//	0x10 cell count M    0x11..0x24 cell voltages (document says 0.1 V, see cellScale)
//	0x25 sensor count N  0x26..0x2F temperatures 0.1 K (sensor 6 is the MOSFET)
//	0x30 charge current 0.1 A   0x31 discharge current 0.1 A   0x32 voltage 0.1 V
//	0x33 SOC %           0x34..0x35 total capacity mAh
//	0x36 packs online    0x37 charge alarm   0x38 discharge alarm
//	0x39 charge protect  0x3A charge protect 2   0x3B discharge protect   0x3C discharge protect 2
//	0x3D BMS state       0x3E..0x3F design capacity mAh
//	0x70 charge voltage limit 0.1 V   0x71 discharge voltage limit 0.1 V
//	0x72 charge current limit 0.1 A   0x73 discharge current limit 0.1 A
//	0x74 charge/discharge status   0x75 run time to empty min   0x76..0x77 remaining mAh
const (
	voltVersionAt, voltVersionN = 0x01, 7
	voltStatusAt, voltStatusN   = 0x10, 0x26 // 0x10..0x35
	voltMfrAt, voltMfrN         = 0x36, 10   // 0x36..0x3F
	voltLimitAt, voltLimitN     = 0x70, 8    // 0x70..0x77
)

var (
	voltChargeAlarm = []string{"charge_ot", "cell_ov", "charge_ut", "charge_oc"}
	voltDischAlarm  = []string{"discharge_ot", "discharge_ut", "mosfet_ot", "cell_uv"}
	voltChargeProt  = []string{"in_system", "boost_not_ready", "afe_comm", "afe_charge_oc", "afe_discharge_oc", "afe_discharge_sc",
		"mosfet_ot", "safety_uv", "charge_ot", "charge_ut", "cell_ov", "charge_oc", "cell_ov_2nd", "can_id_incomplete", "", "charge_oc_2nd"}
	voltChargeProt2 = []string{"shutdown_by_cmd", "low_voltage_shutdown", "charge_short_circuit", "can_id_error"}
	voltDischProt   = []string{"in_system", "boost_not_ready", "afe_charge_oc", "afe_discharge_oc", "afe_discharge_sc", "",
		"can_id_error", "", "", "", "mosfet_ot", "cell_uv", "discharge_oc", "discharge_ut", "discharge_ot", "low_voltage_shutdown"}
	voltDischProt2 = []string{"afe_comm", "discharge_short_circuit", "discharge_oc_2nd", "", "cell_ov_2nd", "discharge_ot_2nd",
		"shutdown_by_cmd", "", "", "", "", "", "", "can_id_incomplete"}
)

type voltronic struct {
	p       serialport.Port
	slave   byte
	timeout time.Duration
	gap     time.Duration
	lastEnd time.Time

	infoDone bool
	version  string
}

func newVoltronic(p serialport.Port, slave byte) *voltronic {
	return &voltronic{p: p, slave: slave, timeout: time.Second, gap: 120 * time.Millisecond}
}

// BuildVoltronicRead builds a read request (function 0x03).
func BuildVoltronicRead(slave byte, start, words uint16) []byte {
	req := []byte{slave, 0x03, byte(start >> 8), byte(start), byte(words >> 8), byte(words)}
	crc := modbus.CRC16(req)
	return append(req, byte(crc), byte(crc>>8))
}

// BuildVoltronicReply builds a valid read reply (for tests and fake BMSs).
func BuildVoltronicReply(slave byte, regs []uint16) []byte {
	out := []byte{slave, 0x03, byte(len(regs) >> 8), byte(len(regs))}
	for _, r := range regs {
		out = append(out, byte(r>>8), byte(r))
	}
	crc := modbus.CRC16(out)
	return append(out, byte(crc), byte(crc>>8))
}

// VoltronicComplete reports whether b is a whole, CRC-correct reply or error reply.
func VoltronicComplete(b []byte) bool {
	if len(b) == 5 && b[1]&0x80 != 0 {
		return modbus.CRC16(b) == 0
	}
	if len(b) < 6 {
		return false
	}
	n := 2 * int(binary.BigEndian.Uint16(b[2:4]))
	return len(b) == 6+n && modbus.CRC16(b) == 0
}

// VoltronicProbeRequest is one read-only request sent by `lmh-can probe -style voltronic`.
type VoltronicProbeRequest struct {
	Name  string
	Start uint16
	Words uint16
}

var VoltronicProbeRequests = []VoltronicProbeRequest{
	{"version information", voltVersionAt, voltVersionN},
	{"general status", voltStatusAt, voltStatusN},
	{"alarms / protection / design capacity", voltMfrAt, voltMfrN},
	{"warning states", 0x40, 0x2A},
	{"charge / discharge limits", voltLimitAt, voltLimitN},
}

// readWords reads words 16-bit registers from start.
func (v *voltronic) readWords(start, words uint16) ([]uint16, error) {
	if wait := v.gap - time.Since(v.lastEnd); wait > 0 {
		time.Sleep(wait)
	}
	rsp, err := Exchange(v.p, BuildVoltronicRead(v.slave, start, words), v.timeout, v.gap, VoltronicComplete)
	v.lastEnd = time.Now()
	if err != nil {
		return nil, err
	}
	if len(rsp) == 0 {
		return nil, ErrNoReply
	}
	if len(rsp) == 5 && rsp[1]&0x80 != 0 && modbus.CRC16(rsp) == 0 {
		return nil, fmt.Errorf("read 0x%04X: BMS error code %d (1 = slave id, 2 = command, 3 = CRC)", start, rsp[2])
	}
	if !VoltronicComplete(rsp) || rsp[0] != v.slave || rsp[1] != 0x03 || len(rsp) != 6+2*int(words) {
		head := rsp
		if len(head) > 16 {
			head = head[:16]
		}
		return nil, fmt.Errorf("read 0x%04X: bad reply (%d bytes, expected %d): % x", start, len(rsp), 6+2*int(words), head)
	}
	out := make([]uint16, words)
	for i := range out {
		out[i] = binary.BigEndian.Uint16(rsp[4+2*i:])
	}
	return out, nil
}

func (v *voltronic) Poll(b *model.Battery) error {
	st, err := v.readWords(voltStatusAt, voltStatusN)
	if err != nil {
		return err
	}
	nb := *b
	if err := decodeVoltStatus(st, &nb); err != nil {
		return err
	}
	// The rest is best effort: a missed reply keeps the previous values.
	if m, err := v.readWords(voltMfrAt, voltMfrN); err == nil {
		decodeVoltAlarms(m, &nb)
	}
	if l, err := v.readWords(voltLimitAt, voltLimitN); err == nil {
		decodeVoltLimits(l, &nb)
	}
	if !v.infoDone {
		if r, err := v.readWords(voltVersionAt, voltVersionN); err == nil {
			v.version = fmt.Sprintf("protocol %d.%d fw %s hw %s", r[1]>>8, r[1]&0xFF, dotted(r[2], r[3]), dotted(r[4], r[5]))
			v.infoDone = true
		}
	}
	nb.Version = v.version
	*b = nb
	return nil
}

// cellScale returns the factor that turns a raw cell register into mV. The
// document says 0.1 V, which cannot be right for a LiFePO4 cell (3.3 V would
// read 33); mV is the likely truth. Instead of trusting either, pick the scale
// whose sum best matches the module voltage, and give up when neither is
// within 25 %.
func cellScale(cells []uint16, moduleV float64) (float64, bool) {
	var sum float64
	for _, c := range cells {
		sum += float64(c)
	}
	if sum == 0 || moduleV <= 0 {
		return 0, false
	}
	best, bestErr := 0.0, 1e9
	for _, mv := range []float64{1, 100} { // mV per count
		e := sum * mv / 1000 / moduleV
		if e < 1 {
			e = 1 / e
		}
		if e < bestErr {
			best, bestErr = mv, e
		}
	}
	return best, bestErr < 1.25
}

// decodeVoltStatus applies words 0x10..0x35.
func decodeVoltStatus(r []uint16, b *model.Battery) error {
	if len(r) < voltStatusN {
		return errShort
	}
	w := func(addr int) uint16 { return r[addr-voltStatusAt] }
	volt := float64(w(0x32)) / 10
	if volt == 0 {
		return errors.New("module voltage is 0: BMS not ready or wrong map")
	}
	b.VoltageV = model.F(round(volt, 1))
	b.CurrentA = model.F(round(float64(int(w(0x30))-int(w(0x31)))/10, 1))
	b.SOCPct = model.F(float64(w(0x33)))
	b.FullAh = model.F(round(float64(uint32(w(0x34))<<16|uint32(w(0x35)))/1000, 3))

	m := int(w(0x10))
	if m > 20 {
		m = 20
	}
	b.CellsMV, b.CellMinMV, b.CellMaxMV, b.CellDeltaMV = nil, nil, nil, nil
	if raw := r[0x11-voltStatusAt : 0x11-voltStatusAt+m]; m > 0 {
		if sc, ok := cellScale(raw, volt); ok {
			lo, hi := 1<<30, 0
			for _, c := range raw {
				mv := int(float64(c) * sc)
				b.CellsMV = append(b.CellsMV, mv)
				if mv < lo {
					lo = mv
				}
				if mv > hi {
					hi = mv
				}
			}
			// At 0.1 V per count the cells are too coarse for min / max / spread to mean anything.
			if sc == 1 {
				b.CellMinMV, b.CellMaxMV, b.CellDeltaMV = model.F(float64(lo)), model.F(float64(hi)), model.F(float64(hi-lo))
			}
		}
	}

	n := int(w(0x25))
	if n > 10 {
		n = 10
	}
	b.TempsC, b.CellTempsC, b.MosfetTempC = nil, nil, nil
	for i := 0; i < n; i++ {
		raw := w(0x26 + i)
		if raw == 0 || raw == 0xFFFF { // "does not exist" reads 0 (document) or 0xFFFF (this BMS)
			continue
		}
		t := round((float64(raw)-2731)/10, 1)
		b.TempsC = append(b.TempsC, t)
		switch {
		case n == 6 && i == 5:
			b.MosfetTempC = model.F(t)
		case n == 6:
			b.CellTempsC = append(b.CellTempsC, t)
		}
	}
	if len(b.CellTempsC) > 0 {
		lo, hi := b.CellTempsC[0], b.CellTempsC[0]
		for _, t := range b.CellTempsC {
			if t < lo {
				lo = t
			}
			if t > hi {
				hi = t
			}
		}
		b.CellTempMinC, b.CellTempMaxC = model.F(lo), model.F(hi)
	}
	return nil
}

// decodeVoltAlarms applies words 0x36..0x3F.
func decodeVoltAlarms(r []uint16, b *model.Battery) {
	if len(r) < voltMfrN {
		return
	}
	b.Modules = model.I(int(r[0]))
	b.InstalledAh = model.F(round(float64(uint32(r[8])<<16|uint32(r[9]))/1000, 3))
	b.AlarmRaw = fmt.Sprintf("chg=%04x dis=%04x", r[1], r[2])
	b.ProtectionRaw = fmt.Sprintf("chg=%04x chg2=%04x dis=%04x dis2=%04x", r[3], r[4], r[5], r[6])
	f := &model.Flags{Warnings: []string{}, Protections: []string{}, Status: []string{}}
	f.Warnings = append(append(f.Warnings, prefixed("charge:", flagNames(r[1], voltChargeAlarm))...),
		prefixed("discharge:", flagNames(r[2], voltDischAlarm))...)
	for _, p := range []struct {
		pre   string
		v     uint16
		names []string
	}{{"charge:", r[3], voltChargeProt}, {"charge:", r[4], voltChargeProt2}, {"discharge:", r[5], voltDischProt}, {"discharge:", r[6], voltDischProt2}} {
		f.Protections = append(f.Protections, prefixed(p.pre, flagNames(p.v, p.names))...)
	}
	if b.Flags != nil {
		f.Status = b.Flags.Status
	}
	b.Flags = f
}

// decodeVoltLimits applies words 0x70..0x77.
func decodeVoltLimits(r []uint16, b *model.Battery) {
	if len(r) < voltLimitN {
		return
	}
	b.ChargeVoltageLimitV = model.F(round(float64(r[0])/10, 1))
	b.DischargeVoltageLimitV = model.F(round(float64(r[1])/10, 1))
	b.ChargeCurrentLimitA = model.F(round(float64(r[2])/10, 1))
	b.DischargeCurrentLimitA = model.F(round(float64(r[3])/10, 1))
	b.StatusRaw = fmt.Sprintf("%02x", r[4]&0xFF)
	b.RemainingAh = model.F(round(float64(uint32(r[6])<<16|uint32(r[7]))/1000, 3))
	st := []string{}
	if r[4]&0x80 == 0 {
		st = append(st, "charge_disabled")
	}
	if r[4]&0x40 == 0 {
		st = append(st, "discharge_disabled")
	}
	if r[4]&0x20 != 0 {
		st = append(st, "charge_now_soc_le_9")
	}
	if r[4]&0x10 != 0 {
		st = append(st, "charge_soon_soc_le_14")
	}
	if b.Flags == nil {
		b.Flags = &model.Flags{Warnings: []string{}, Protections: []string{}}
	}
	b.Flags.Status = st
}

func prefixed(p string, in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = p + s
	}
	return out
}

// dotted renders two words as four dot-separated bytes (01 00 00 00 -> 1.0.0.0).
func dotted(a, b uint16) string {
	return fmt.Sprintf("%d.%d.%d.%d", a>>8, a&0xFF, b>>8, b&0xFF)
}
