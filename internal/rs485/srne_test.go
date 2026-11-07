package rs485

import (
	"strings"
	"testing"

	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
	"lmhcan/internal/serialport"
)

// fakeSRNE answers function-3 reads from a register map; addresses in reject
// get an "illegal data address" exception, everything else not in the map reads 0.
func fakeSRNE(regs map[uint16]uint16, reject map[uint16]bool) (*serialport.Fake, *[]uint16) {
	var asked []uint16
	f := &serialport.Fake{Respond: func(req []byte) []byte {
		slave, start, count, ok := modbus.ParseRequest(req)
		if !ok || slave != 1 {
			return nil
		}
		asked = append(asked, start)
		if count > 32 {
			return modbus.BuildException(slave, 0x0A)
		}
		if reject[start] {
			return modbus.BuildException(slave, 2)
		}
		out := make([]uint16, count)
		for i := range out {
			out[i] = regs[start+uint16(i)]
		}
		return modbus.BuildReply(slave, out)
	}}
	return f, &asked
}

func srneSample() map[uint16]uint16 {
	m := map[uint16]uint16{
		0x100: 90, 0x101: 512, 0x102: 670, 0x103: 41<<8 | 25,
		0x107: 3500, 0x108: 120, 0x109: 4200, 0x10B: 2, 0x10E: 5000,
		0x204: 14, 0x205: 99, 0x210: 4, 0x212: 3900, 0x213: 2301, 0x214: 55, 0x215: 5001,
		0x216: 2300, 0x217: 62, 0x218: 5000, 0x219: 70, 0x21A: 0xFFF6 /* -0.10 */, 0x21B: 1500, 0x21C: 1600, 0x21E: 100, 0x21F: 30,
		0x220: 452, 0x221: 480, 0x222: 0, 0x223: 0, 0x224: 123,
		0xF02D: 40, 0xF02E: 12, 0xF02F: 156, 0xF030: 98, 0xF031: 200,
		0xF034: 0x0001, 0xF035: 0x0001, // 65537 Ah, low word first
		0xF038: 100, 0xF03A: 50, 0xF03C: 5, 0xF03D: 7, 0xF03E: 600, 0xF03F: 30,
		0xF046: 20, 0xF048: 30, 0xF04A: 1000, 0xF04B: 100,
		0x14: 105, 0x15: 106, 0x16: 200, 0x17: 201, 0x1A: 1, 0x1B: 7,
	}
	for i, c := range "SN12345" {
		m[0x35+uint16(i)] = uint16(c)
	}
	return m
}

func TestSRNEPoll(t *testing.T) {
	f, asked := fakeSRNE(srneSample(), nil)
	var inv model.Inverter
	if err := newSRNE(&modbus.Client{P: f}, 1).Poll(&inv); err != nil {
		t.Fatal(err)
	}
	if *inv.BatterySOCPct != 90 || *inv.BatteryVoltageV != 51.2 || *inv.BatteryCurrentA != 67 || *inv.BatteryTempC != 25 || inv.TempsC["controller"] != 41 {
		t.Errorf("battery side: %+v", inv)
	}
	if len(inv.PV) != 1 || inv.PV[0].VoltageV != 350 || inv.PV[0].CurrentA != 12 || inv.PV[0].PowerW != 4200 {
		t.Errorf("pv = %+v (second input must stay out until it reports)", inv.PV)
	}
	if inv.ChargeState != "constant voltage" || *inv.ChargePowerW != 5000 || *inv.PVChargeCurrentA != 12.3 {
		t.Errorf("charge = %s %v %v", inv.ChargeState, *inv.ChargePowerW, *inv.PVChargeCurrentA)
	}
	if inv.State != "mains powered operation" || *inv.BusVoltageV != 390 || *inv.GridVoltageV != 230.1 || *inv.GridFrequencyHz != 50.01 {
		t.Errorf("state / grid = %s %v %v %v", inv.State, *inv.BusVoltageV, *inv.GridVoltageV, *inv.GridFrequencyHz)
	}
	if *inv.OutputVoltageV != 230 || *inv.LoadPowerW != 1500 || *inv.LoadApparentVA != 1600 || *inv.LoadPF != -0.1 || *inv.LoadPct != 30 {
		t.Error("output / load")
	}
	if len(inv.FaultCodes) != 2 || inv.FaultCodes[0] != 14 || inv.Faults[0] != "inverter overload" || inv.Faults[1] != "fault 99" {
		t.Errorf("faults = %v %v", inv.FaultCodes, inv.Faults)
	}
	if inv.TempsC["heatsink_dcdc"] != 45.2 || inv.TempsC["heatsink_dcac"] != 48 || inv.TempsC["controller"] != 41 || len(inv.TempsC) != 3 {
		t.Errorf("temps = %v (a sensor reading 0 must not be reported)", inv.TempsC)
	}
	if *inv.Today.PVEnergyKWh != 15.6 || *inv.Today.BatteryChargeAh != 40 || *inv.Today.InverterMinutes != 600 || *inv.RunningDays != 200 {
		t.Errorf("today = %+v", inv.Today)
	}
	if *inv.Totals.BatteryChargeAh != 65537 || *inv.Totals.PVEnergyKWh != 10 || *inv.Totals.MainsLoadEnergyKWh != 3 || *inv.Totals.InverterHours != 1000 {
		t.Errorf("totals = %+v", inv.Totals)
	}
	if inv.Version != "software 1.05 / 1.06, hardware 2.00 / 2.01" || inv.SerialNo != "SN12345" || *inv.ModelCode != 7 || *inv.RS485Addr != 1 {
		t.Errorf("identity = %q %q", inv.Version, inv.SerialNo)
	}
	inv.Device, inv.Protocol = "inv1", "srne"
	inv.StateCode = model.I(4)
	inv.Finish()
	if *inv.BatteryPowerW != 3430.4 || *inv.PVPowerW != 4200 || inv.Class != "inverter" {
		t.Errorf("derived = %v %v", *inv.BatteryPowerW, *inv.PVPowerW)
	}
	for _, a := range *asked {
		if a != 0x100 && a != 0x200 && a != 0x220 && a != 0xF02D && a != 0xF046 && a != 0x14 && a != 0x1A && a != 0x35 {
			t.Errorf("unexpected read at 0x%X", a)
		}
	}
}

func TestSRNERejectedBlocksAreNotAskedAgain(t *testing.T) {
	f, asked := fakeSRNE(srneSample(), map[uint16]bool{0xF02D: true, 0xF046: true})
	p := newSRNE(&modbus.Client{P: f}, 1)
	var inv model.Inverter
	for n := 0; n < 3; n++ {
		if err := p.Poll(&inv); err != nil {
			t.Fatal(err)
		}
	}
	c := 0
	for _, a := range *asked {
		if a == 0xF02D || a == 0xF046 {
			c++
		}
	}
	if c != 2 { // each rejected once, then skipped
		t.Errorf("rejected blocks asked %d times, want 2", c)
	}
	if inv.Today != nil || inv.BatteryVoltageV == nil {
		t.Errorf("core values must still be reported, today=%v", inv.Today)
	}
}

func TestSRNESecondPVInputAppearsOnceAndStays(t *testing.T) {
	m := srneSample()
	f, _ := fakeSRNE(m, nil)
	p := newSRNE(&modbus.Client{P: f}, 1)
	var inv model.Inverter
	p.Poll(&inv)
	if len(inv.PV) != 1 {
		t.Fatal("pv2 reports nothing yet")
	}
	m[0x10F], m[0x110], m[0x111] = 3000, 50, 1500
	p.Poll(&inv)
	m[0x10F], m[0x110], m[0x111] = 0, 0, 0 // night
	p.Poll(&inv)
	if len(inv.PV) != 2 {
		t.Errorf("pv = %+v", inv.PV)
	}
}

func TestSRNENoReplyAndLookup(t *testing.T) {
	var inv model.Inverter
	if err := newSRNE(&modbus.Client{P: &serialport.Fake{}}, 1).Poll(&inv); err == nil {
		t.Error("a silent inverter must be an error")
	}
	if i, err := LookupInverter("SRNE"); err != nil || i.Name != "srne" || i.DefaultSlave != 1 {
		t.Errorf("lookup: %v %v", i, err)
	}
	if _, err := LookupInverter("nope"); err == nil {
		t.Error("unknown inverter protocol")
	}
}

func TestCheckDevice(t *testing.T) {
	for _, c := range [][2]string{{"battery", "pacemodbus"}, {"battery", "growatt"}, {"inverter", "srne"}, {"battery", "PACE"}} {
		if err := CheckDevice(c[0], c[1]); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	for _, c := range [][2]string{{"", "srne"}, {"", "pacemodbus"}, {"battery", "srne"}, {"inverter", "pacemodbus"}, {"generator", "srne"}, {"inverter", "nope"}, {"battery", "nope"}} {
		if err := CheckDevice(c[0], c[1]); err == nil {
			t.Errorf("%v must be rejected", c)
		}
	}
	if err := CheckDevice("battery", "srne"); err == nil || !strings.Contains(err.Error(), "-device inverter") {
		t.Errorf("wrong-device hint: %v", err)
	}
	// the same name in both lists is fine: each is looked up in its own device's list
	saved := inverters
	defer func() { inverters = saved }()
	inverters = append(append([]InverterInfo(nil), inverters...), InverterInfo{Name: "pacemodbus", Level: 1})
	if CheckDevice("battery", "pacemodbus") != nil || CheckDevice("inverter", "pacemodbus") != nil {
		t.Error("a name in both lists must work for both devices")
	}
}
