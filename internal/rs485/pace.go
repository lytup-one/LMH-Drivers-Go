package rs485

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
)

// Bit names, bit 0 first; "" = reserved. From the PACE BMS Modbus protocol V1.3.
var (
	paceWarningBits = []string{"cell_ov", "cell_uv", "pack_ov", "pack_uv", "charge_oc", "discharge_oc",
		"", "", "charge_ot", "discharge_ot", "charge_ut", "discharge_ut", "env_ot", "env_ut", "mosfet_ot", "soc_low"}
	paceProtectionBits = []string{"cell_ov", "cell_uv", "pack_ov", "pack_uv", "charge_oc", "discharge_oc",
		"short_circuit", "charger_ov", "charge_ot", "discharge_ot", "charge_ut", "discharge_ut", "mosfet_ot", "env_ot", "env_ut", ""}
	paceStatusBits = []string{"fault_charge_mosfet", "fault_discharge_mosfet", "fault_temp_sensor", "",
		"fault_cell", "fault_sampling_comm", "", "",
		"state_charging", "state_discharging", "charge_mosfet_on", "discharge_mosfet_on", "charge_limiter_on", "", "charger_reversed", "heater_on"}
)

const paceRegs = 40 // registers 0..39

type pace struct {
	c        *modbus.Client
	slave    byte
	info     model.Battery // version / serials, read once
	infoDone bool
}

func newPace(c *modbus.Client, slave byte) *pace { return &pace{c: c, slave: slave} }

func (p *pace) Poll(b *model.Battery) error {
	r, err := p.c.ReadHolding(p.slave, 0, paceRegs)
	if err != nil {
		return err
	}
	decodePace(r, b)
	if !p.infoDone {
		// Strings are static: read once. A failure here is not fatal; try again next poll.
		ok := true
		for _, f := range []struct {
			start uint16
			set   func(string)
		}{
			{150, func(s string) { p.info.Version = s }},
			{160, func(s string) { p.info.ModelSerial = s }},
			{170, func(s string) { p.info.Serial = s }},
		} {
			regs, err := p.c.ReadHolding(p.slave, f.start, 10)
			if err != nil {
				ok = false
				break
			}
			f.set(asciiRegs(regs))
		}
		p.infoDone = ok
	}
	b.Version, b.ModelSerial, b.Serial = p.info.Version, p.info.ModelSerial, p.info.Serial
	return nil
}

func s16(v uint16) int { return int(int16(v)) }

func round(v float64, places int) float64 {
	k := math.Pow(10, float64(places))
	return math.Round(v*k) / k
}

// temp decodes a 0.1 degC signed register; 0x8000 means no sensor.
func temp(v uint16) (float64, bool) {
	if v == 0x8000 {
		return 0, false
	}
	return round(float64(s16(v))/10, 1), true
}

func flagNames(v uint16, names []string) []string {
	out := []string{}
	for i, n := range names {
		if n != "" && v>>uint(i)&1 == 1 {
			out = append(out, n)
		}
	}
	return out
}

func asciiRegs(regs []uint16) string {
	raw := make([]byte, 2*len(regs))
	for i, r := range regs {
		binary.BigEndian.PutUint16(raw[2*i:], r)
	}
	if i := strings.IndexByte(string(raw), 0); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(string(raw))
}

// decodePace applies registers 0..39 to b. Register layout (V1.3):
//
//	0 current 0.01 A signed (+ charging)   1 voltage 0.01 V   2 SOC %   3 SOH %
//	4 remaining 0.01 Ah   5 full 0.01 Ah   6 design 0.01 Ah   7 cycles
//	9 warning flags   10 protection flags   11 status flags   12 balance mask
//	15..30 cell voltages mV   31..34 cell temps   35 MOSFET temp   36 ambient temp (0.1 degC, 0x8000 = none)
//	37 charge voltage limit 0.1 V   38 charge current limit 0.1 A   39 discharge current limit 0.1 A
func decodePace(r []uint16, b *model.Battery) {
	b.CurrentA = model.F(round(float64(s16(r[0]))*0.01, 2))
	b.VoltageV = model.F(round(float64(r[1])*0.01, 2))
	b.SOCPct = model.F(float64(r[2]))
	b.SOHPct = model.F(float64(r[3]))
	b.RemainingAh = model.F(round(float64(r[4])*0.01, 2))
	b.FullAh = model.F(round(float64(r[5])*0.01, 2))
	b.InstalledAh = model.F(round(float64(r[6])*0.01, 2))
	b.Cycles = model.I(int(r[7]))

	// Unused cell slots read 0 or 0xFFFF; keep the leading valid cells.
	b.CellsMV = nil
	for _, v := range r[15:31] {
		if v == 0 || v == 0xFFFF {
			break
		}
		b.CellsMV = append(b.CellsMV, int(v))
	}
	if n := len(b.CellsMV); n > 0 {
		lo, hi := b.CellsMV[0], b.CellsMV[0]
		for _, v := range b.CellsMV {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		b.CellMinMV, b.CellMaxMV, b.CellDeltaMV = model.F(float64(lo)), model.F(float64(hi)), model.F(float64(hi-lo))
	}

	b.CellTempsC = nil
	var tmin, tmax float64
	for _, v := range r[31:35] {
		if t, ok := temp(v); ok {
			if len(b.CellTempsC) == 0 || t < tmin {
				tmin = t
			}
			if len(b.CellTempsC) == 0 || t > tmax {
				tmax = t
			}
			b.CellTempsC = append(b.CellTempsC, t)
		}
	}
	b.CellTempMinC, b.CellTempMaxC = nil, nil
	if len(b.CellTempsC) > 0 {
		b.CellTempMinC, b.CellTempMaxC = model.F(tmin), model.F(tmax)
	}
	b.MosfetTempC, b.AmbientTempC = nil, nil
	if t, ok := temp(r[35]); ok {
		b.MosfetTempC = model.F(t)
	}
	if t, ok := temp(r[36]); ok {
		b.AmbientTempC = model.F(t)
	}

	b.ChargeVoltageLimitV = model.F(round(float64(r[37])*0.1, 1))
	b.ChargeCurrentLimitA = model.F(round(float64(s16(r[38]))*0.1, 1))
	b.DischargeCurrentLimitA = model.F(round(float64(s16(r[39]))*0.1, 1))

	b.AlarmRaw = fmt.Sprintf("%04X", r[9])
	b.ProtectionRaw = fmt.Sprintf("%04X", r[10])
	b.StatusRaw = fmt.Sprintf("%04X", r[11])
	b.BalanceMaskRaw = fmt.Sprintf("%04X", r[12])
	b.Flags = &model.Flags{
		Warnings:    flagNames(r[9], paceWarningBits),
		Protections: flagNames(r[10], paceProtectionBits),
		Status:      flagNames(r[11], paceStatusBits),
	}
}
