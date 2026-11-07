package rs485

import (
	"testing"

	"lmhcan/internal/model"
	"lmhcan/internal/serialport"
)

// fakeVolt answers Voltronic read requests from a word map.
func fakeVolt(words map[uint16]uint16) *serialport.Fake {
	return &serialport.Fake{Respond: func(req []byte) []byte {
		if len(req) != 8 || req[1] != 3 {
			return nil
		}
		start := uint16(req[2])<<8 | uint16(req[3])
		n := uint16(req[4])<<8 | uint16(req[5])
		regs := make([]uint16, n)
		for i := range regs {
			regs[i] = words[start+uint16(i)]
		}
		return BuildVoltronicReply(req[0], regs)
	}}
}

func sampleVolt(cellMV bool) map[uint16]uint16 {
	m := map[uint16]uint16{0x10: 16, 0x25: 6, 0x30: 0, 0x31: 75, 0x32: 524, 0x33: 87, 0x34: 4, 0x35: 0x4B50, // 0x44B50 = 281424 mAh
		0x36: 1, 0x37: 0x0008, 0x39: 0x0400, 0x3E: 0x0004, 0x3F: 0x4B50,
		0x70: 560, 0x71: 400, 0x72: 1000, 0x73: 2000, 0x74: 0xC0, 0x76: 0x0003, 0x77: 0xD090,
		0x01: 0, 0x02: 0, 0x03: 0x0102, 0x04: 0x0304, 0x05: 0x0001, 0x06: 0x0002}
	for i := uint16(0); i < 16; i++ {
		if cellMV {
			m[0x11+i] = 3270 + i
		} else {
			m[0x11+i] = 33
		}
	}
	for i := uint16(0); i < 6; i++ {
		m[0x26+i] = 2981 + i // 25.0 C ...
	}
	return m
}

func TestVoltronicPoll(t *testing.T) {
	f := fakeVolt(sampleVolt(true))
	var b model.Battery
	if err := newVoltronic(f, 1).Poll(&b); err != nil {
		t.Fatal(err)
	}
	if *b.VoltageV != 52.4 || *b.CurrentA != -7.5 || *b.SOCPct != 87 {
		t.Errorf("core = %v %v %v", *b.VoltageV, *b.CurrentA, *b.SOCPct)
	}
	if len(b.CellsMV) != 16 || b.CellsMV[0] != 3270 || *b.CellDeltaMV != 15 {
		t.Errorf("cells = %v", b.CellsMV)
	}
	if len(b.CellTempsC) != 5 || b.MosfetTempC == nil || *b.MosfetTempC != 25.5 || *b.CellTempMinC != 25 {
		t.Errorf("temps = %v mos %v", b.CellTempsC, b.MosfetTempC)
	}
	if *b.FullAh != 281.424 || *b.InstalledAh != 281.424 || *b.RemainingAh != 250 {
		t.Errorf("capacity = %v %v %v", *b.FullAh, *b.InstalledAh, *b.RemainingAh)
	}
	if *b.ChargeVoltageLimitV != 56 || *b.DischargeCurrentLimitA != 200 || b.StatusRaw != "c0" {
		t.Errorf("limits = %v %v %s", *b.ChargeVoltageLimitV, *b.DischargeCurrentLimitA, b.StatusRaw)
	}
	if len(b.Flags.Warnings) != 1 || b.Flags.Warnings[0] != "charge:charge_oc" || len(b.Flags.Protections) != 1 || b.Flags.Protections[0] != "charge:cell_ov" {
		t.Errorf("flags = %+v", b.Flags)
	}
	if len(b.Flags.Status) != 0 || b.Version == "" {
		t.Errorf("status %v version %q", b.Flags.Status, b.Version)
	}
}

func TestVoltronicCellScaleFallbackAndSilence(t *testing.T) {
	var b model.Battery
	// 0.1 V per count (as the document says): 16 x 33 = 52.8 V matches the module voltage.
	if err := newVoltronic(fakeVolt(sampleVolt(false)), 1).Poll(&b); err != nil {
		t.Fatal(err)
	}
	if len(b.CellsMV) != 16 || b.CellsMV[0] != 3300 {
		t.Errorf("0.1 V cells = %v", b.CellsMV)
	}
	// Cells that match neither scale are left out, the rest is still reported.
	m := sampleVolt(true)
	for i := uint16(0); i < 16; i++ {
		m[0x11+i] = 500
	}
	var c model.Battery
	if err := newVoltronic(fakeVolt(m), 1).Poll(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.CellsMV) != 0 || c.VoltageV == nil {
		t.Errorf("cells matching no scale must be left out, got %v", c.CellsMV)
	}
	silent := &serialport.Fake{}
	if err := newVoltronic(silent, 1).Poll(&b); err != ErrNoReply {
		t.Errorf("silent BMS: %v", err)
	}
}

func TestVoltronicComplete(t *testing.T) {
	r := BuildVoltronicReply(1, []uint16{1, 2, 3})
	if !VoltronicComplete(r) || VoltronicComplete(r[:len(r)-1]) {
		t.Error("complete detection")
	}
}

// Replies of the LYTVOLT S15000 in Voltronic mode (captured with `probe -style voltronic`).
func TestVoltronicRealCapture(t *testing.T) {
	status := []uint16{0x10}
	for i := 0; i < 16; i++ {
		status = append(status, 0x21)
	}
	status = append(status, 0xffff, 0xffff, 0xffff, 0xffff, 4, 0x0bec, 0x0bea, 0x0bef, 0x0beb)
	for i := 0; i < 6; i++ {
		status = append(status, 0xffff)
	}
	status = append(status, 0, 6, 0x0219, 0x62, 4, 0x4566)
	words := map[uint16]uint16{0x01: 0, 0x02: 0x0100, 0x03: 0x0100, 0x04: 0, 0x05: 0x0100, 0x06: 0, 0x07: 0xffff,
		0x36: 1, 0x3E: 4, 0x3F: 0x45c0,
		0x70: 0x0220, 0x71: 0x01d6, 0x72: 0, 0x73: 0x07d0, 0x74: 0x40, 0x75: 0, 0x76: 4, 0x77: 0x345e}
	for i, w := range status {
		words[0x10+uint16(i)] = w
	}
	var b model.Battery
	if err := newVoltronic(fakeVolt(words), 1).Poll(&b); err != nil {
		t.Fatal(err)
	}
	if *b.VoltageV != 53.7 || *b.CurrentA != -0.6 || *b.SOCPct != 98 || *b.FullAh != 279.91 || *b.InstalledAh != 280 {
		t.Errorf("core = %v %v %v %v %v", *b.VoltageV, *b.CurrentA, *b.SOCPct, *b.FullAh, *b.InstalledAh)
	}
	if len(b.CellsMV) != 16 || b.CellsMV[0] != 3300 || b.CellDeltaMV != nil {
		t.Errorf("cells = %v delta %v", b.CellsMV, b.CellDeltaMV)
	}
	if len(b.TempsC) != 4 || b.TempsC[0] != 32.1 || b.MosfetTempC != nil {
		t.Errorf("temps = %v", b.TempsC)
	}
	if *b.ChargeVoltageLimitV != 54.4 || *b.DischargeVoltageLimitV != 47 || *b.ChargeCurrentLimitA != 0 || *b.DischargeCurrentLimitA != 200 {
		t.Errorf("limits wrong")
	}
	if len(b.Flags.Status) != 1 || b.Flags.Status[0] != "charge_disabled" {
		t.Errorf("status = %v", b.Flags.Status)
	}
	if b.Version != "protocol 1.0 fw 1.0.0.0 hw 1.0.0.0" {
		t.Errorf("version = %q", b.Version)
	}
}
