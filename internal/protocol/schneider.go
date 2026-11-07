package protocol

import (
	"lmhcan/internal/canbus"
	"lmhcan/internal/model"
)

// Schneider mode uses 29-bit frame IDs 0x320-0x333, big-endian, with 32-bit
// values scaled by 0.01. Meanings derived from a capture of the BMS in this
// mode and cross-checked against the battery's own values:
//
//	0x321 charge V limit (u32), discharge V limit (u32)
//	0x322 charge I limit (u32), discharge I limit (u32)
//	0x323 pack V (u32), current (s32)
//	0x324 temperature (u16, 0.1 C), SOC (u16, 0.1 %)
//	0x326 modules?, cycles, SOH, full capacity (0.1 Ah)
//	0x327 max temp, min temp (0.1 C), max cell mV, min cell mV
//	0x330-0x332 pack serial: three ASCII chunks, each reversed, last frame first
//
// 0x320 and 0x325 (flags) and 0x333 are not decoded.
type schneider struct {
	t      table
	chunks [3]string
}

func newSchneider() Decoder {
	s := &schneider{}
	chunk := func(i int) handler {
		return func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			s.chunks[i] = string(d[i0(i):])
			if s.chunks[0] != "" && s.chunks[1] != "" && s.chunks[2] != "" {
				b.Serial = reverse(s.chunks[2]) + reverse(s.chunks[1]) + reverse(s.chunks[0])
			}
			return true
		}
	}
	s.t = table{
		0x321: func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			b.ChargeVoltageLimitV = model.F(float64(be.Uint32(d[0:4])) / 100)
			b.DischargeVoltageLimitV = model.F(float64(be.Uint32(d[4:8])) / 100)
			return true
		},
		0x322: func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			b.ChargeCurrentLimitA = model.F(float64(int32(be.Uint32(d[0:4]))) / 100)
			b.DischargeCurrentLimitA = model.F(float64(int32(be.Uint32(d[4:8]))) / 100)
			return true
		},
		0x323: func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			b.VoltageV = model.F(float64(be.Uint32(d[0:4])) / 100)
			b.CurrentA = model.F(float64(int32(be.Uint32(d[4:8]))) / 100)
			return true
		},
		0x324: func(d []byte, b *model.Battery) bool {
			if len(d) < 4 {
				return false
			}
			b.TemperatureC = model.F(float64(int16(be.Uint16(d[0:2]))) / 10)
			b.SOCPct = model.F(float64(be.Uint16(d[2:4])) / 10)
			return true
		},
		0x326: func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			b.Cycles = model.I(int(be.Uint16(d[2:4])))
			b.SOHPct = model.F(float64(be.Uint16(d[4:6])))
			b.FullAh = model.F(float64(be.Uint16(d[6:8])) / 10)
			return true
		},
		0x327: func(d []byte, b *model.Battery) bool {
			if len(d) < 8 {
				return false
			}
			b.CellTempMaxC = model.F(float64(int16(be.Uint16(d[0:2]))) / 10)
			b.CellTempMinC = model.F(float64(int16(be.Uint16(d[2:4]))) / 10)
			b.CellMaxMV, b.CellMinMV = mv(be.Uint16(d[4:6])), mv(be.Uint16(d[6:8]))
			return true
		},
		0x330: chunk(0),
		0x331: chunk(1),
		0x332: chunk(2),
	}
	return s
}

// i0 is the offset of the ASCII text inside each serial frame: 0x330 carries
// it in bytes 4-7, 0x331 and 0x332 in bytes 0-7.
func i0(i int) int {
	if i == 0 {
		return 4
	}
	return 0
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func (s *schneider) Decode(f canbus.Frame, b *model.Battery) bool {
	return extDialect{s.t}.Decode(f, b)
}
