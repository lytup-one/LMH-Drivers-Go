package rs485

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
	"lmhcan/internal/protocol"
	"lmhcan/internal/serialport"
)

// These registers are synthetic (built from the V1.3 register layout), not a
// capture: they check the decoding rules, not the BMS.
func sampleRegs() map[uint16]uint16 {
	m := map[uint16]uint16{
		0: uint16(0xFFFF - 69), // -0.70 A
		1: 5595, 2: 99, 3: 100,
		4: 27500, 5: 28000, 6: 28000, 7: 50,
		9: 1<<1 | 1<<15, 10: 1 << 6, 11: 1<<8 | 1<<10 | 1<<11,
		12: 0x0004,
		31: 332, 32: 0x8000, 33: 0xFFFB /* -0.5 */, 34: 340,
		35: 351, 36: 0x8000,
		37: 544, 38: 0, 39: 2000,
	}
	for i := uint16(15); i < 31; i++ {
		m[i] = 3300 + i // 3315..3330
	}
	m[30] = 0xFFFF // unused 16th slot
	// ascii strings: version @150, model sn @160, pack sn @170
	put := func(start uint16, s string) {
		b := append([]byte(s), make([]byte, 20-len(s))...)
		for i := 0; i < 10; i++ {
			m[start+uint16(i)] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		}
	}
	put(150, "V1.2")
	put(160, "S15000-A")
	put(170, "GPSR1PC200LS28051029")
	return m
}

func fakeBMS(slave byte, regs map[uint16]uint16) (*serialport.Fake, *int) {
	reads := new(int)
	return &serialport.Fake{Respond: func(req []byte) []byte {
		s, start, n, ok := modbus.ParseRequest(req)
		if !ok || s != slave {
			return nil
		}
		*reads++
		out := make([]uint16, n)
		for i := range out {
			out[i] = regs[start+uint16(i)]
		}
		return modbus.BuildReply(slave, out)
	}}, reads
}

func TestPaceDecode(t *testing.T) {
	f, reads := fakeBMS(1, sampleRegs())
	c := &modbus.Client{P: f, Timeout: 50 * time.Millisecond, Gap: time.Millisecond}
	info, err := Lookup("PACE Modbus")
	if err != nil || info.Name != "pacemodbus" {
		t.Fatalf("lookup: %+v %v", info, err)
	}
	p := newPace(c, 1)
	var b model.Battery
	if err := p.Poll(&b); err != nil {
		t.Fatal(err)
	}
	if *b.CurrentA != -0.7 || *b.VoltageV != 55.95 || *b.SOCPct != 99 || *b.SOHPct != 100 {
		t.Errorf("core: I=%v V=%v SOC=%v SOH=%v", *b.CurrentA, *b.VoltageV, *b.SOCPct, *b.SOHPct)
	}
	if *b.RemainingAh != 275 || *b.FullAh != 280 || *b.InstalledAh != 280 || *b.Cycles != 50 {
		t.Errorf("capacity: %v %v %v %v", *b.RemainingAh, *b.FullAh, *b.InstalledAh, *b.Cycles)
	}
	if len(b.CellsMV) != 15 || b.CellsMV[0] != 3315 || *b.CellMinMV != 3315 || *b.CellMaxMV != 3329 || *b.CellDeltaMV != 14 {
		t.Errorf("cells: %v min %v max %v", b.CellsMV, *b.CellMinMV, *b.CellMaxMV)
	}
	if len(b.CellTempsC) != 3 || *b.CellTempMinC != -0.5 || *b.CellTempMaxC != 34 || *b.MosfetTempC != 35.1 || b.AmbientTempC != nil {
		t.Errorf("temps: %v", b.CellTempsC)
	}
	if *b.ChargeVoltageLimitV != 54.4 || *b.ChargeCurrentLimitA != 0 || *b.DischargeCurrentLimitA != 200 {
		t.Errorf("limits: %v %v %v", *b.ChargeVoltageLimitV, *b.ChargeCurrentLimitA, *b.DischargeCurrentLimitA)
	}
	if got := b.Flags.Warnings; len(got) != 2 || got[0] != "cell_uv" || got[1] != "soc_low" {
		t.Errorf("warnings: %v", got)
	}
	if got := b.Flags.Protections; len(got) != 1 || got[0] != "short_circuit" {
		t.Errorf("protections: %v", got)
	}
	if got := b.Flags.Status; len(got) != 3 || got[0] != "state_charging" {
		t.Errorf("status: %v", got)
	}
	if b.AlarmRaw != "8002" || b.ProtectionRaw != "0040" || b.StatusRaw != "0D00" {
		t.Errorf("raw: %s %s %s", b.AlarmRaw, b.ProtectionRaw, b.StatusRaw)
	}
	if b.Version != "V1.2" || b.ModelSerial != "S15000-A" || b.Serial != "GPSR1PC200LS28051029" {
		t.Errorf("info: %q %q %q", b.Version, b.ModelSerial, b.Serial)
	}
	if *reads != 4 {
		t.Errorf("first poll should make 4 reads (state + 3 strings), got %d", *reads)
	}
	if err := p.Poll(&b); err != nil || *reads != 5 {
		t.Errorf("later polls read only the state: err=%v reads=%d", err, *reads)
	}
	js, _ := json.Marshal(&b)
	if len(js) == 0 {
		t.Error("marshal")
	}
}

func TestPaceNoFlagsIsEmptyListNotAbsent(t *testing.T) {
	regs := sampleRegs()
	regs[9], regs[10], regs[11] = 0, 0, 0
	f, _ := fakeBMS(1, regs)
	var b model.Battery
	p := newPace(&modbus.Client{P: f, Timeout: 50 * time.Millisecond, Gap: time.Millisecond}, 1)
	if err := p.Poll(&b); err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(b.Flags)
	if string(js) != `{"warnings":[],"protections":[],"status":[]}` {
		t.Errorf("flags json = %s", js)
	}
}

func TestPollFailureLeavesStateUntouched(t *testing.T) {
	f := &serialport.Fake{} // never answers
	p := newPace(&modbus.Client{P: f, Timeout: 20 * time.Millisecond, Gap: time.Millisecond}, 1)
	b := model.Battery{VoltageV: model.F(50)}
	if err := p.Poll(&b); !errors.Is(err, modbus.ErrNoReply) {
		t.Fatalf("err = %v", err)
	}
	if b.VoltageV == nil || *b.VoltageV != 50 || b.CurrentA != nil {
		t.Error("state changed on failed poll")
	}
}

func TestRegistry(t *testing.T) {
	if len(All()) != 8 {
		t.Errorf("want the 8 RS485 protocols, got %d", len(All()))
	}
	for _, n := range []string{"growatt", "se", "pylon_f"} {
		_, err := Lookup(n)
		if !errors.Is(err, protocol.ErrNotImplemented) {
			t.Errorf("%s should be unsupported, got %v", n, err)
		}
	}
	if i, err := Lookup("luxpower"); err != nil || i.Name != "luxpower_vo3" {
		t.Errorf("alias: %v %v", i.Name, err)
	}
	if _, err := Lookup("nope"); err == nil || IsUnsupported(err) {
		t.Errorf("unknown protocol: %v", err)
	}
}

func TestExchange(t *testing.T) {
	reply := EncodePylon(PylonFrame{Ver: 0x20, Adr: 2, CID1: 0x46, CID2: 0, Info: []byte{9, 9}})
	f := &serialport.Fake{Respond: func(req []byte) []byte {
		if string(req) == "ping" {
			return reply
		}
		return nil
	}}
	got, err := Exchange(f, []byte("ping"), 50*time.Millisecond, 10*time.Millisecond, PylonComplete)
	if err != nil || string(got) != string(reply) {
		t.Errorf("got %q, %v", got, err)
	}
	got, err = Exchange(f, []byte("silent"), 30*time.Millisecond, 10*time.Millisecond, nil)
	if err != nil || got != nil {
		t.Errorf("silence must be (nil, nil), got %q, %v", got, err)
	}
	req := BuildPylonProbe(PylonProbeRequests[3], 0x20, 2)
	if string(req) != "~20024642E00202FD33\r" {
		t.Errorf("analog probe = %q", req)
	}
	for _, r := range PylonProbeRequests {
		if _, err := DecodePylon(BuildPylonProbe(r, 0x20, 2)); err != nil {
			t.Errorf("%s: %v", r.Name, err)
		}
	}
}
