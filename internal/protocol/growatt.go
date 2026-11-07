package protocol

import (
	"lmhcan/internal/canbus"
	"lmhcan/internal/model"
)

// Growatt mode (big-endian). Frame meanings were derived from a capture of the
// BMS in this mode and cross-checked against the battery's own values:
//
//	0x311 charge V limit, charge I limit, discharge I limit
//	0x313 pack V, current, max temp, SOC, SOH
//	0x314 remaining Ah, full Ah, cell spread (mV), cycle count (u16 at bytes 6-7)
//	0x315-0x318 cell voltages 1-16, four per frame
//	0x319 max cell mV, min cell mV
//	0x322 max temp, min temp
//
// 0x312, 0x320, 0x323 and the flag byte of 0x319 are not decoded.
type growatt struct{ t table }

func newGrowatt() Decoder {
	g := &growatt{}
	cells := func(first int) handler {
		return func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			if len(b.CellsMV) != 16 {
				b.CellsMV = make([]int, 16)
			}
			for i := 0; i < 4; i++ {
				b.CellsMV[first+i] = int(be.Uint16(d[2*i : 2*i+2]))
			}
			return true
		}
	}
	g.t = table{
		0x311: func(d []byte, b *model.Battery) bool {
			if len(d) < 6 {
				return false
			}
			b.ChargeVoltageLimitV = model.F(float64(be.Uint16(d[0:2])) / 10)
			b.ChargeCurrentLimitA = model.F(float64(be.Uint16(d[2:4])) / 10)
			b.DischargeCurrentLimitA = model.F(float64(be.Uint16(d[4:6])) / 10)
			return true
		},
		0x313: func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			b.VoltageV = model.F(float64(be.Uint16(d[0:2])) / 100)
			b.CurrentA = model.F(float64(int16(be.Uint16(d[2:4]))) / 10)
			b.TemperatureC = model.F(float64(int16(be.Uint16(d[4:6]))) / 10)
			b.SOCPct = model.F(float64(d[6]))
			b.SOHPct = model.F(float64(d[7]))
			return true
		},
		0x314: func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			b.RemainingAh = model.F(float64(be.Uint16(d[0:2])) / 100)
			b.FullAh = model.F(float64(be.Uint16(d[2:4])) / 100)
			b.CellDeltaMV = model.F(float64(be.Uint16(d[4:6])))
			b.Cycles = model.I(int(be.Uint16(d[6:8])))
			return true
		},
		0x315: cells(0),
		0x316: cells(4),
		0x317: cells(8),
		0x318: cells(12),
		0x319: func(d []byte, b *model.Battery) bool {
			if len(d) < 5 {
				return false
			}
			b.CellMaxMV, b.CellMinMV = mv(be.Uint16(d[1:3])), mv(be.Uint16(d[3:5]))
			return true
		},
		0x322: func(d []byte, b *model.Battery) bool {
			if len(d) < 4 {
				return false
			}
			b.CellTempMaxC = model.F(float64(int16(be.Uint16(d[0:2]))) / 10)
			b.CellTempMinC = model.F(float64(int16(be.Uint16(d[2:4]))) / 10)
			return true
		},
	}
	return g
}

func (g *growatt) Decode(f canbus.Frame, b *model.Battery) bool {
	return dialect{g.t}.Decode(f, b)
}
