package protocol

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"strings"

	"lmhcan/internal/canbus"
	"lmhcan/internal/model"
)

// handler decodes the data bytes of one frame ID into the battery state.
type handler func(d []byte, b *model.Battery) bool

// table maps frame IDs to handlers; a dialect is a table.
type table map[uint32]handler

func merge(ts ...table) table {
	out := table{}
	for _, t := range ts {
		for k, v := range t {
			out[k] = v
		}
	}
	return out
}

// dialect decodes classic 11-bit frames with a table.
type dialect struct{ t table }

func (x dialect) Decode(f canbus.Frame, b *model.Battery) bool {
	if f.Extended || f.Error {
		return false
	}
	h := x.t[f.ID]
	return h != nil && h(f.Data, b)
}

// extDialect decodes 29-bit frames with a table (Schneider).
type extDialect struct{ t table }

func (x extDialect) Decode(f canbus.Frame, b *model.Battery) bool {
	if !f.Extended || f.Error {
		return false
	}
	h := x.t[f.ID]
	return h != nil && h(f.Data, b)
}

var (
	le = binary.LittleEndian
	be = binary.BigEndian
)

func f1(v float64) *float64 { return model.F(math.Round(v*10) / 10) }

func hexOf(d []byte, n int) string {
	if len(d) < n {
		n = len(d)
	}
	return hex.EncodeToString(d[:n])
}

// printable keeps ASCII graphic characters and spaces, stopping at the first NUL.
func printable(p []byte) string {
	var sb strings.Builder
	for _, c := range p {
		if c == 0 {
			break
		}
		if c >= 0x20 && c < 0x7F {
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func mv(v uint16) *float64 { return model.F(float64(v)) }

// ---- Pylontech-family frames (little-endian) ----

// 0x351: charge V limit, charge I limit, discharge I limit, discharge V limit.
// A discharge voltage limit of 0 means "not provided" (Goodwe/Studer modes send 0).
func h351(d []byte, b *model.Battery) bool {
	if len(d) < 8 {
		return false
	}
	b.ChargeVoltageLimitV = model.F(float64(le.Uint16(d[0:2])) / 10)
	b.ChargeCurrentLimitA = model.F(float64(int16(le.Uint16(d[2:4]))) / 10)
	b.DischargeCurrentLimitA = model.F(float64(int16(le.Uint16(d[4:6]))) / 10)
	if dv := le.Uint16(d[6:8]); dv > 0 {
		b.DischargeVoltageLimitV = model.F(float64(dv) / 10)
	}
	return true
}

// 0x355: SOC %, SOH %. SOH 0 means "not provided" (SRD mode sends 0).
func h355(d []byte, b *model.Battery) bool {
	if len(d) < 4 {
		return false
	}
	b.SOCPct = model.F(float64(le.Uint16(d[0:2])))
	if soh := le.Uint16(d[2:4]); soh > 0 {
		b.SOHPct = model.F(float64(soh))
	}
	return true
}

// 0x355 with SOC in 0.01 % at bytes 4-5 (Victron/SMA style).
func h355HiRes(d []byte, b *model.Battery) bool {
	if !h355(d, b) {
		return false
	}
	if len(d) >= 6 {
		if hi := le.Uint16(d[4:6]); hi > 0 && hi <= 10000 {
			b.SOCPct = model.F(float64(hi) / 100)
		}
	}
	return true
}

// 0x355 with max/min cell voltage (mV) at bytes 4-7 (Luxpower style).
func h355Cells(d []byte, b *model.Battery) bool {
	if !h355(d, b) {
		return false
	}
	if len(d) >= 8 {
		b.CellMaxMV, b.CellMinMV = mv(le.Uint16(d[4:6])), mv(le.Uint16(d[6:8]))
	}
	return true
}

// 0x356: pack V (0.01 V), current (0.1 A, + = charging), temperature (0.1 C).
func h356(d []byte, b *model.Battery) bool {
	if len(d) < 6 {
		return false
	}
	b.VoltageV = model.F(float64(le.Uint16(d[0:2])) / 100)
	b.CurrentA = model.F(float64(int16(le.Uint16(d[2:4]))) / 10)
	b.TemperatureC = model.F(float64(int16(le.Uint16(d[4:6]))) / 10)
	return true
}

// 0x359: protection (2 B), alarm (2 B), module count, 2-char ID.
func h359(d []byte, b *model.Battery) bool {
	if len(d) < 7 {
		return false
	}
	b.ProtectionRaw = hex.EncodeToString(d[0:2])
	b.AlarmRaw = hex.EncodeToString(d[2:4])
	b.Modules = model.I(int(d[4]))
	b.ID = printable(d[5:7])
	return true
}

// 0x359 in Luxpower mode: bytes 5-6 hold the installed capacity (Ah, LE), not an ID.
func h359Lux(d []byte, b *model.Battery) bool {
	if len(d) < 7 {
		return false
	}
	b.ProtectionRaw = hex.EncodeToString(d[0:2])
	b.AlarmRaw = hex.EncodeToString(d[2:4])
	b.Modules = model.I(int(d[4]))
	b.InstalledAh = installed(le.Uint16(d[5:7]))
	return true
}

func installed(v uint16) *float64 {
	if v == 0 || v > 5000 {
		return nil
	}
	return model.F(float64(v))
}

// 0x35C: request flags.
func h35C(d []byte, b *model.Battery) bool {
	if len(d) < 1 {
		return false
	}
	b.RequestFlagsRaw = hexOf(d, 2)
	return true
}

// 0x35C in Luxpower mode: flags plus the cycle count at bytes 2-3 (LE).
func h35CCycles(d []byte, b *model.Battery) bool {
	if !h35C(d, b) {
		return false
	}
	if len(d) >= 4 {
		b.Cycles = model.I(int(le.Uint16(d[2:4])))
	}
	return true
}

// 0x35E: manufacturer name, ASCII.
func h35E(d []byte, b *model.Battery) bool {
	b.Manufacturer = strings.TrimSpace(printable(d))
	return true
}

// 0x35A as raw alarm/warning bytes (SMA/Victron/SRD/Sofar/PV modes).
func h35ARaw(d []byte, b *model.Battery) bool {
	if len(d) < 1 {
		return false
	}
	b.AlarmRaw = hex.EncodeToString(d)
	return true
}

// 0x35F: installed capacity (Ah, LE) at bytes 4-5.
func h35F(d []byte, b *model.Battery) bool {
	if len(d) < 6 {
		return false
	}
	b.InstalledAh = installed(le.Uint16(d[4:6]))
	return true
}

// 0x370 (Goodwe/Studer): max temp, min temp (0.1 C), max cell, min cell (mV), all LE.
func h370GoodweStuder(d []byte, b *model.Battery) bool {
	if len(d) < 8 {
		return false
	}
	b.CellTempMaxC = model.F(float64(int16(le.Uint16(d[0:2]))) / 10)
	b.CellTempMinC = model.F(float64(int16(le.Uint16(d[2:4]))) / 10)
	b.CellMaxMV, b.CellMinMV = mv(le.Uint16(d[4:6])), mv(le.Uint16(d[6:8]))
	return true
}

// 0x373 (Victron-style): min cell mV, max cell mV, min temp K, max temp K (whole kelvin).
func h373(d []byte, b *model.Battery) bool {
	if len(d) < 8 {
		return false
	}
	b.CellMinMV, b.CellMaxMV = mv(le.Uint16(d[0:2])), mv(le.Uint16(d[2:4]))
	b.CellTempMinC = f1(float64(le.Uint16(d[4:6])) - 273.15)
	b.CellTempMaxC = f1(float64(le.Uint16(d[6:8])) - 273.15)
	return true
}

// 0x379 (Victron-style): installed capacity in Ah (LE).
func h379(d []byte, b *model.Battery) bool {
	if len(d) < 2 {
		return false
	}
	b.InstalledAh = installed(le.Uint16(d[0:2]))
	return true
}

// ---- dialect tables ----

func tCore() table { return table{0x351: h351, 0x355: h355, 0x356: h356} }

func tPylon() table {
	return merge(tCore(), table{0x359: h359, 0x35C: h35C, 0x35E: h35E})
}

// SMA / Victron / SRD / Sofar / PV style: no 0x359/0x35C, raw alarms in 0x35A.
func tSMAFamily(hiRes bool) table {
	t := merge(tCore(), table{0x35A: h35ARaw, 0x35E: h35E, 0x35F: h35F})
	if hiRes {
		t[0x355] = h355HiRes
	}
	return t
}
