package rs485

import (
	"errors"
	"fmt"
	"strings"

	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
)

// SRNE "Energy storage inverter MODBUS communication protocol" V1.7 (2022-07-04),
// also used by the HF / HFP series all-in-one inverters. Standard Modbus RTU,
// function 3, 9600 8N1, at most 32 registers per read. Register addresses in
// the document are hex. 32-bit values take two registers, LOW word first.
//
// Polled every cycle (all read-only):
//
//	0x0100..0x0111  battery SOC / voltage / current / temperature, PV 1 and 2, charge state and power
//	0x0200..0x021F  fault bits and codes, machine state, bus, grid, output, load
//	0x0220..0x0225  heat sink temperatures, PV charge current
//
// Best effort (a block the inverter rejects is not asked again):
//
//	0xF02D..0xF03F  today's and accumulated energy counters
//	0xF046..0xF04B  accumulated mains charge / load energy, working hours
//
// Once: 0x14..0x17 versions, 0x1A..0x1D address / model / protocol, 0x35.. serial.
//
// The register map has not been run against a real inverter yet.
type srne struct {
	c     *modbus.Client
	slave byte

	skipStats, skipTotals bool
	infoDone              bool
	infoTries             int
	info                  model.Inverter
	pv2Seen               bool
}

func newSRNE(c *modbus.Client, slave byte) *srne { return &srne{c: c, slave: slave} }

var srneMachineState = []string{"power-up delay", "waiting", "initialization", "soft start", "mains powered operation",
	"inverter powered operation", "inverter to mains", "mains to inverter", "battery activate", "shutdown by user", "fault"}

var srneChargeState = map[int]string{0: "off", 1: "quick charge", 2: "constant voltage", 4: "float", 6: "lithium battery activate"}

// Fault codes from the HFP series manual (section 6.2); codes not listed are reported as numbers only.
var srneFaults = map[int]string{
	1: "battery under-voltage alarm", 2: "battery discharge over-current (software)", 3: "battery not connected",
	4: "battery under-voltage, discharge stopped", 5: "battery over-current (hardware)", 6: "charging over-voltage protection",
	7: "bus over-voltage (hardware)", 8: "bus over-voltage (software)", 9: "PV over-voltage", 10: "boost over-current (software)",
	11: "boost over-current (hardware)", 13: "bypass overload", 14: "inverter overload", 15: "inverter over-current (hardware)",
	17: "inverter short circuit", 19: "buck heat sink over-temperature", 20: "inverter heat sink over-temperature", 21: "fan failure",
	22: "memory (EEPROM) failure", 23: "model setting error", 26: "inverter output backfeeds to bypass input",
	29: "internal battery boost circuit failure", 30: "battery capacity below 10 %", 31: "battery capacity below 5 %",
	32: "inverter stopped, battery capacity low", 34: "parallel CAN communication fault", 35: "parallel ID setting error",
	37: "parallel current sharing fault", 38: "large battery voltage difference in parallel", 39: "inconsistent AC source in parallel",
	40: "parallel hardware sync error", 41: "inverter DC voltage error", 42: "firmware versions differ in parallel",
	43: "parallel line connection error",
}

func (s *srne) read(start, n uint16) ([]uint16, error) { return s.c.ReadHolding(s.slave, start, n) }

// rejected reports whether the inverter refused the address (not a link problem).
func rejected(err error) bool {
	var ex *modbus.Exception
	return errors.As(err, &ex) && (ex.Code == 2 || ex.Code == 0x0B || ex.Code == 1)
}

func (s *srne) Poll(i *model.Inverter) error {
	a, err := s.read(0x100, 18)
	if err != nil {
		return err
	}
	b, err := s.read(0x200, 32)
	if err != nil {
		return err
	}
	nb := *i
	s.decodeController(a, &nb)
	s.decodeInverter(b, &nb)
	// The rest is best effort: a missed reply keeps the previous values.
	if c, err := s.read(0x220, 6); err == nil {
		decodeSRNETemps(c, &nb)
	}
	if !s.skipStats {
		if r, err := s.read(0xF02D, 19); err == nil {
			decodeSRNEToday(r, &nb)
		} else if rejected(err) {
			s.skipStats = true
		}
	}
	if !s.skipTotals {
		if r, err := s.read(0xF046, 6); err == nil {
			decodeSRNETotals(r, &nb)
		} else if rejected(err) {
			s.skipTotals = true
		}
	}
	if !s.infoDone && s.infoTries < 3 {
		s.infoTries++
		s.readInfo()
	}
	nb.Version, nb.SerialNo, nb.ModelCode, nb.RS485Addr = s.info.Version, s.info.SerialNo, s.info.ModelCode, s.info.RS485Addr
	*i = nb
	return nil
}

func (s *srne) readInfo() {
	ok := true
	if r, err := s.read(0x14, 4); err == nil {
		s.info.Version = fmt.Sprintf("software %.2f / %.2f, hardware %.2f / %.2f",
			float64(r[0])/100, float64(r[1])/100, float64(r[2])/100, float64(r[3])/100)
	} else {
		ok = false
	}
	if r, err := s.read(0x1A, 4); err == nil {
		s.info.RS485Addr, s.info.ModelCode = model.I(int(r[0])), model.I(int(r[1]))
	} else {
		ok = false
	}
	if r, err := s.read(0x35, 20); err == nil {
		s.info.SerialNo = lowBytesString(r)
	} // the serial string is optional: some models leave it empty
	s.infoDone = ok
}

// lowBytesString builds a string from the low byte of each register (the document's string format).
func lowBytesString(regs []uint16) string {
	var sb strings.Builder
	for _, r := range regs {
		c := byte(r)
		if c == 0 {
			break
		}
		if c >= 0x20 && c < 0x7f {
			sb.WriteByte(c)
		}
	}
	return strings.TrimSpace(sb.String())
}

// decodeController applies registers 0x100..0x111.
func (s *srne) decodeController(r []uint16, i *model.Inverter) {
	v := func(addr int) uint16 { return r[addr-0x100] }
	i.BatterySOCPct = model.F(float64(v(0x100)))
	i.BatteryVoltageV = model.F(round(float64(v(0x101))/10, 1))
	i.BatteryCurrentA = model.F(round(float64(s16(v(0x102)))/10, 1))
	// 0x103: high byte controller temperature, low byte battery temperature, whole degC signed.
	hi, lo := int(int8(v(0x103)>>8)), int(int8(v(0x103)&0xFF))
	i.BatteryTempC = nil
	if v(0x103) != 0 { // both bytes 0 means no temperature reported
		i.BatteryTempC = model.F(float64(lo))
		setTemp(i, "controller", float64(hi), true)
	} else {
		setTemp(i, "controller", 0, false)
	}

	pv1 := model.PVInput{VoltageV: round(float64(v(0x107))/10, 1), CurrentA: round(float64(v(0x108))/10, 1), PowerW: float64(v(0x109))}
	pv2 := model.PVInput{VoltageV: round(float64(v(0x10F))/10, 1), CurrentA: round(float64(v(0x110))/10, 1), PowerW: float64(v(0x111))}
	if pv2.VoltageV != 0 || pv2.CurrentA != 0 || pv2.PowerW != 0 {
		s.pv2Seen = true // a second input that has ever reported stays in the output, so night time does not make it flicker
	}
	i.PV = []model.PVInput{pv1}
	if s.pv2Seen {
		i.PV = append(i.PV, pv2)
	}

	st := int(v(0x10B))
	if n, ok := srneChargeState[st]; ok {
		i.ChargeState = n
	} else {
		i.ChargeState = fmt.Sprintf("unknown (%d)", st)
	}
	i.ChargePowerW = model.F(float64(v(0x10E)))
}

// decodeInverter applies registers 0x200..0x21F.
func (s *srne) decodeInverter(r []uint16, i *model.Inverter) {
	v := func(addr int) uint16 { return r[addr-0x200] }
	i.FaultBitsRaw = fmt.Sprintf("%04x%04x%04x%04x", v(0x200), v(0x201), v(0x202), v(0x203))
	i.FaultCodes, i.Faults = nil, nil
	for a := 0x204; a <= 0x207; a++ {
		if c := int(v(a)); c != 0 {
			i.FaultCodes = append(i.FaultCodes, c)
			if n, ok := srneFaults[c]; ok {
				i.Faults = append(i.Faults, n)
			} else {
				i.Faults = append(i.Faults, fmt.Sprintf("fault %d", c))
			}
		}
	}
	code := int(v(0x210))
	i.StateCode = model.I(code)
	if code < len(srneMachineState) {
		i.State = srneMachineState[code]
	} else {
		i.State = fmt.Sprintf("unknown (%d)", code)
	}
	i.BusVoltageV = model.F(round(float64(v(0x212))/10, 1))
	i.GridVoltageV = model.F(round(float64(v(0x213))/10, 1))
	i.GridCurrentA = model.F(round(float64(v(0x214))/10, 1))
	i.GridFrequencyHz = model.F(round(float64(v(0x215))/100, 2))
	i.OutputVoltageV = model.F(round(float64(v(0x216))/10, 1))
	i.OutputCurrentA = model.F(round(float64(v(0x217))/10, 1))
	i.OutputFreqHz = model.F(round(float64(v(0x218))/100, 2))
	i.LoadCurrentA = model.F(round(float64(v(0x219))/10, 1))
	i.LoadPF = model.F(round(float64(s16(v(0x21A)))/100, 2))
	i.LoadPowerW = model.F(float64(v(0x21B)))
	i.LoadApparentVA = model.F(float64(v(0x21C)))
	i.MainsChargeCurrent = model.F(round(float64(v(0x21E))/10, 1))
	i.LoadPct = model.F(float64(v(0x21F)))
}

// decodeSRNETemps applies registers 0x220..0x225. A sensor that reads 0 is not reported.
func decodeSRNETemps(r []uint16, i *model.Inverter) {
	for n, name := range []string{"heatsink_dcdc", "heatsink_dcac", "heatsink_transformer", "heatsink_d"} {
		setTemp(i, name, round(float64(s16(r[n]))/10, 1), r[n] != 0)
	}
	i.PVChargeCurrentA = model.F(round(float64(r[4])/10, 1))
}

func setTemp(i *model.Inverter, name string, v float64, present bool) {
	if !present {
		delete(i.TempsC, name)
		return
	}
	if i.TempsC == nil {
		i.TempsC = map[string]float64{}
	}
	i.TempsC[name] = v
}

func u32(lo, hi uint16) float64 { return float64(uint32(hi)<<16 | uint32(lo)) } // low word first

// decodeSRNEToday applies registers 0xF02D..0xF03F.
func decodeSRNEToday(r []uint16, i *model.Inverter) {
	v := func(addr int) uint16 { return r[addr-0xF02D] }
	i.Today = &model.InverterEnergy{
		BatteryChargeAh:    model.F(float64(v(0xF02D))),
		BatteryDischargeAh: model.F(float64(v(0xF02E))),
		PVEnergyKWh:        model.F(round(float64(v(0xF02F))/10, 1)),
		LoadEnergyKWh:      model.F(round(float64(v(0xF030))/10, 1)),
		MainsChargeAh:      model.F(float64(v(0xF03C))),
		MainsLoadEnergyKWh: model.F(round(float64(v(0xF03D))/10, 1)),
		InverterMinutes:    model.F(float64(v(0xF03E))),
		BypassMinutes:      model.F(float64(v(0xF03F))),
	}
	if i.Totals == nil {
		i.Totals = &model.InverterEnergy{}
	}
	i.Totals.BatteryChargeAh = model.F(u32(v(0xF034), v(0xF035)))
	i.Totals.BatteryDischargeAh = model.F(u32(v(0xF036), v(0xF037)))
	i.Totals.PVEnergyKWh = model.F(round(u32(v(0xF038), v(0xF039))/10, 1))
	i.Totals.LoadEnergyKWh = model.F(round(u32(v(0xF03A), v(0xF03B))/10, 1))
	i.RunningDays = model.I(int(v(0xF031)))
}

// decodeSRNETotals applies registers 0xF046..0xF04B.
func decodeSRNETotals(r []uint16, i *model.Inverter) {
	if i.Totals == nil {
		i.Totals = &model.InverterEnergy{}
	}
	i.Totals.MainsChargeEnergyKWh = model.F(round(u32(r[0], r[1])/10, 1))
	i.Totals.MainsLoadEnergyKWh = model.F(round(u32(r[2], r[3])/10, 1))
	i.Totals.InverterHours = model.F(float64(r[4]))
	i.Totals.BypassHours = model.F(float64(r[5]))
}
