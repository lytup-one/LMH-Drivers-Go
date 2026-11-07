package rs485

import (
	"bytes"
	"testing"

	"lmhcan/internal/model"
	"lmhcan/internal/serialport"
)

func fakeLux(regs []uint16) *serialport.Fake {
	return &serialport.Fake{Respond: func(req []byte) []byte {
		// only the little-endian request of the document is answered
		if !bytes.Equal(req, BuildLuxpowerRead(1, 0, 16)) {
			return nil
		}
		return BuildLuxpowerReply(1, regs)
	}}
}

func TestLuxpowerRequestMatchesDocument(t *testing.T) {
	req := BuildLuxpowerRead(1, 0, 16)
	if !bytes.Equal(req[:6], []byte{0x01, 0x03, 0x00, 0x00, 0x10, 0x00}) {
		t.Errorf("request = % x", req)
	}
}

func TestLuxpowerPoll(t *testing.T) {
	regs := []uint16{544, 0, 2000, 98 | 100<<8, 280, 537, 0xFFFA /* -0.6 A */, 0x0002, 0x0000, 0x0000, 3340, 3330, 321, 318, 1, 50}
	var b model.Battery
	if err := newLuxpower(fakeLux(regs), 1).Poll(&b); err != nil {
		t.Fatal(err)
	}
	if *b.VoltageV != 53.7 || *b.CurrentA != -0.6 || *b.SOCPct != 98 || *b.SOHPct != 100 || *b.FullAh != 280 {
		t.Errorf("core = %v %v %v %v %v", *b.VoltageV, *b.CurrentA, *b.SOCPct, *b.SOHPct, *b.FullAh)
	}
	if *b.ChargeVoltageLimitV != 54.4 || *b.ChargeCurrentLimitA != 0 || *b.DischargeCurrentLimitA != 200 {
		t.Error("limits")
	}
	if *b.CellMaxMV != 3340 || *b.CellDeltaMV != 10 || *b.CellTempMaxC != 32.1 || *b.CellTempMinC != 31.8 || *b.Modules != 1 || *b.Cycles != 50 {
		t.Error("cells / temps / counters")
	}
	if len(b.Flags.Status) != 1 || b.Flags.Status[0] != "charge_disabled" || b.Flags.Warnings != nil {
		t.Errorf("flags = %+v", b.Flags)
	}
	if b.StatusRaw != "0002" || b.ProtectionRaw != "fault=0000" {
		t.Errorf("raw = %s %s", b.StatusRaw, b.ProtectionRaw)
	}
}

func TestLuxpowerErrors(t *testing.T) {
	var b model.Battery
	if err := newLuxpower(&serialport.Fake{}, 1).Poll(&b); err != ErrNoReply {
		t.Errorf("silent: %v", err)
	}
	zero := make([]uint16, 16)
	if err := newLuxpower(fakeLux(zero), 1).Poll(&b); err == nil {
		t.Error("zero voltage must be an error")
	}
	if !LuxpowerComplete(BuildLuxpowerReply(1, zero)) || LuxpowerComplete(BuildLuxpowerReply(1, zero)[:30]) {
		t.Error("complete detection")
	}
}

// Real reply of the LYTVOLT S15000 in Luxpower_VO3 mode (captured with `probe -style luxpower`).
func TestLuxpowerRealCapture(t *testing.T) {
	raw := []byte{0x01, 0x03, 0x20, 0x20, 0x02, 0x00, 0x00, 0xd0, 0x07, 0x62, 0x64, 0x17, 0x01, 0x18, 0x02, 0xfa, 0xff,
		0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1e, 0x0d, 0x17, 0x0d, 0x45, 0x01, 0x40, 0x01, 0x01, 0x00, 0x33, 0x00, 0xf8, 0xd6}
	if !LuxpowerComplete(raw) {
		t.Fatal("captured frame must pass the CRC check")
	}
	f := &serialport.Fake{Respond: func(req []byte) []byte { return raw }}
	var b model.Battery
	if err := newLuxpower(f, 1).Poll(&b); err != nil {
		t.Fatal(err)
	}
	if *b.VoltageV != 53.6 || *b.CurrentA != -0.6 || *b.SOCPct != 98 || *b.SOHPct != 100 || *b.FullAh != 279 || *b.Cycles != 51 {
		t.Errorf("core = %v %v %v %v %v %v", *b.VoltageV, *b.CurrentA, *b.SOCPct, *b.SOHPct, *b.FullAh, *b.Cycles)
	}
	if *b.CellMinMV != 3351 || *b.CellMaxMV != 3358 || *b.CellDeltaMV != 7 || *b.CellTempMinC != 32 || *b.CellTempMaxC != 32.5 {
		t.Error("cells / temps")
	}
	if *b.ChargeVoltageLimitV != 54.4 || *b.DischargeCurrentLimitA != 200 || b.StatusRaw != "0002" {
		t.Error("limits / status")
	}
}
