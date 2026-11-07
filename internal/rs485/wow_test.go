package rs485

import (
	"testing"
	"time"

	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
	"lmhcan/internal/protocol"
	"lmhcan/internal/serialport"
)

// Registers 0..39 read from the LYTVOLT S15000 in WOW mode (slave 1, 9600 8N1)
// with `lmh-can probe -modbus` on 2026-11-04. Addresses 40..59 read 0xFFFF.
var wowDump = []uint16{
	65467, 5379, 98, 100, 27560, 27991, 28000, 50, 65535, 0, 32768, 3584, 0, 65535, 65535,
	3363, 3363, 3360, 3358, 3366, 3363, 3363, 3365, 3365, 3361, 3361, 3361, 3361, 3363, 3361, 3365,
	323, 321, 326, 321, 342, 366, 544, 0, 2000,
}

func wowPoller() (*wow, *serialport.Fake) {
	f := &serialport.Fake{Respond: func(req []byte) []byte {
		s, start, n, ok := modbus.ParseRequest(req)
		if !ok || s != 1 {
			return nil
		}
		out := make([]uint16, n)
		for i := range out {
			if a := int(start) + i; a < len(wowDump) {
				out[i] = wowDump[a]
			} else {
				out[i] = 0xFFFF
			}
		}
		return modbus.BuildReply(1, out)
	}}
	return newWow(&modbus.Client{P: f, Timeout: 50 * time.Millisecond, Gap: time.Millisecond}, 1), f
}

func TestWowDecodesTheCapturedRegisters(t *testing.T) {
	q, f := wowPoller()
	var b model.Battery
	if err := q.Poll(&b); err != nil {
		t.Fatal(err)
	}
	if *b.CurrentA != -0.69 || *b.VoltageV != 53.79 || *b.SOCPct != 98 || *b.SOHPct != 100 {
		t.Errorf("core: I=%v V=%v SOC=%v SOH=%v", *b.CurrentA, *b.VoltageV, *b.SOCPct, *b.SOHPct)
	}
	if *b.RemainingAh != 275.6 || *b.FullAh != 279.91 || *b.InstalledAh != 280 || *b.Cycles != 50 {
		t.Errorf("capacity: %v %v %v %v", *b.RemainingAh, *b.FullAh, *b.InstalledAh, *b.Cycles)
	}
	if len(b.CellsMV) != 16 || b.CellsMV[0] != 3363 || b.CellsMV[15] != 3365 || *b.CellMinMV != 3358 || *b.CellMaxMV != 3366 || *b.CellDeltaMV != 8 {
		t.Errorf("cells: %v min=%v max=%v", b.CellsMV, *b.CellMinMV, *b.CellMaxMV)
	}
	sum := 0
	for _, c := range b.CellsMV {
		sum += c
	}
	if d := float64(sum)/1000 - *b.VoltageV; d < -0.2 || d > 0.2 {
		t.Errorf("cells add up to %.2f V, pack reads %.2f V", float64(sum)/1000, *b.VoltageV)
	}
	if len(b.CellTempsC) != 4 || b.CellTempsC[0] != 32.3 || *b.MosfetTempC != 34.2 || *b.AmbientTempC != 36.6 {
		t.Errorf("temps: %v mosfet=%v ambient=%v", b.CellTempsC, *b.MosfetTempC, *b.AmbientTempC)
	}
	if *b.ChargeVoltageLimitV != 54.4 || *b.ChargeCurrentLimitA != 0 || *b.DischargeCurrentLimitA != 200 {
		t.Errorf("limits: %v %v %v", *b.ChargeVoltageLimitV, *b.ChargeCurrentLimitA, *b.DischargeCurrentLimitA)
	}
	if b.AlarmRaw != "0000" || b.ProtectionRaw != "8000" || b.StatusRaw != "0E00" {
		t.Errorf("raw flags: %s %s %s", b.AlarmRaw, b.ProtectionRaw, b.StatusRaw)
	}
	if b.Flags != nil {
		t.Error("flag names are not confirmed for WOW mode and must not be reported")
	}
	if len(f.Writes) != 1 {
		t.Errorf("one read of registers 0..39 expected, got %d requests", len(f.Writes))
	}
}

func TestWowIsRegisteredAsCaptured(t *testing.T) {
	i, err := Lookup("WOW")
	if err != nil || i.Level != protocol.Captured || i.New == nil || i.DefaultSlave != 1 {
		t.Errorf("wow: %+v %v", i, err)
	}
}
