package rs485

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
	"lmhcan/internal/serialport"
)

// Luxpowertek "Communication Protocol between Luxpowertek Hybrid inverter and
// battery, RS485" V0.3 (2023-11-03). 9600 8N1, slave 1, function 3.
//
// The document declares the protocol little-endian, and means it everywhere
// except the CRC convention: the request fields (start address, register
// count) and every data word in the reply are sent low byte first. For the
// standard 16-register read this makes the request
//
//	01 03 00 00 10 00 crc-lo crc-hi        (standard Modbus would be 01 03 00 00 00 10 ...)
//
// and the reply  01 03 20 d0lo d0hi ... d15lo d15hi crc-lo crc-hi  (the length
// byte is a byte count, as in Modbus). modbus.Client sends the count and reads
// the words big-endian, so this protocol has its own client.
//
// Register map (never checked against a real BMS yet):
//
//	0 charge voltage 0.1 V   1 max charge current 0.1 A   2 max discharge current 0.1 A
//	3 SOC % (low byte), SOH % (high byte)   4 full capacity Ah   5 voltage 0.1 V
//	6 current 0.1 A signed (+ charging)     7 status bits (0 charge enable, 1 discharge enable,
//	  2 forced charge, 3 full charge request)   8 fault code   9 warning code
//	10 max cell mV   11 min cell mV   12 max cell temp 0.1 C   13 min cell temp 0.1 C
//	14 parallel number   15 cycle count
const luxRegs = 16

type luxpower struct {
	p       serialport.Port
	slave   byte
	timeout time.Duration
	gap     time.Duration
	lastEnd time.Time
}

func newLuxpower(p serialport.Port, slave byte) *luxpower {
	return &luxpower{p: p, slave: slave, timeout: time.Second, gap: 120 * time.Millisecond}
}

// BuildLuxpowerRead builds the little-endian read request.
func BuildLuxpowerRead(slave byte, start, count uint16) []byte {
	req := []byte{slave, 0x03, byte(start), byte(start >> 8), byte(count), byte(count >> 8)}
	crc := modbus.CRC16(req)
	return append(req, byte(crc), byte(crc>>8))
}

// BuildLuxpowerReply builds a valid reply (for tests and fake BMSs).
func BuildLuxpowerReply(slave byte, regs []uint16) []byte {
	out := []byte{slave, 0x03, byte(2 * len(regs))}
	for _, r := range regs {
		out = append(out, byte(r), byte(r>>8))
	}
	crc := modbus.CRC16(out)
	return append(out, byte(crc), byte(crc>>8))
}

// LuxpowerComplete reports whether b is a whole, CRC-correct reply or error reply.
func LuxpowerComplete(b []byte) bool { return ModbusComplete(b) }

// ModbusComplete reports whether b is a whole, CRC-correct Modbus reply (byte-count length) or exception reply.
func ModbusComplete(b []byte) bool {
	if len(b) == 5 && b[1]&0x80 != 0 {
		return modbus.CRC16(b) == 0
	}
	return len(b) >= 5 && len(b) == 5+int(b[2]) && modbus.CRC16(b) == 0
}

func (l *luxpower) Poll(b *model.Battery) error {
	if wait := l.gap - time.Since(l.lastEnd); wait > 0 {
		time.Sleep(wait)
	}
	rsp, err := Exchange(l.p, BuildLuxpowerRead(l.slave, 0, luxRegs), l.timeout, l.gap, LuxpowerComplete)
	l.lastEnd = time.Now()
	if err != nil {
		return err
	}
	if len(rsp) == 0 {
		return ErrNoReply
	}
	if len(rsp) == 5 && rsp[1]&0x80 != 0 && modbus.CRC16(rsp) == 0 {
		return fmt.Errorf("BMS returned exception %d", rsp[2])
	}
	if !LuxpowerComplete(rsp) || rsp[0] != l.slave || rsp[1] != 0x03 || len(rsp) != 5+2*luxRegs {
		head := rsp
		if len(head) > 16 {
			head = head[:16]
		}
		return fmt.Errorf("bad reply (%d bytes, expected %d): % x", len(rsp), 5+2*luxRegs, head)
	}
	r := make([]uint16, luxRegs)
	for i := range r {
		r[i] = binary.LittleEndian.Uint16(rsp[3+2*i:])
	}
	nb := *b
	if err := decodeLuxpower(r, &nb); err != nil {
		return err
	}
	*b = nb
	return nil
}

func decodeLuxpower(r []uint16, b *model.Battery) error {
	if len(r) < luxRegs {
		return errShort
	}
	if r[5] == 0 {
		return errors.New("battery voltage is 0: BMS not ready or wrong map")
	}
	b.ChargeVoltageLimitV = model.F(round(float64(r[0])/10, 1))
	b.ChargeCurrentLimitA = model.F(round(float64(r[1])/10, 1))
	b.DischargeCurrentLimitA = model.F(round(float64(r[2])/10, 1))
	b.SOCPct = model.F(float64(r[3] & 0xFF))
	b.SOHPct = model.F(float64(r[3] >> 8))
	b.FullAh = model.F(float64(r[4]))
	b.VoltageV = model.F(round(float64(r[5])/10, 1))
	b.CurrentA = model.F(round(float64(s16(r[6]))/10, 1))
	b.StatusRaw = fmt.Sprintf("%04x", r[7])
	b.ProtectionRaw = fmt.Sprintf("fault=%04x", r[8]) // bit meanings are not in the document
	b.AlarmRaw = fmt.Sprintf("warning=%04x", r[9])
	b.CellMaxMV, b.CellMinMV = model.F(float64(r[10])), model.F(float64(r[11]))
	b.CellDeltaMV = model.F(float64(int(r[10]) - int(r[11])))
	b.CellTempMaxC = model.F(round(float64(s16(r[12]))/10, 1))
	b.CellTempMinC = model.F(round(float64(s16(r[13]))/10, 1))
	b.Modules = model.I(int(r[14]))
	b.Cycles = model.I(int(r[15]))

	st := []string{}
	for i, n := range []string{"charge", "discharge"} {
		if r[7]>>uint(i)&1 == 0 {
			st = append(st, n+"_disabled")
		}
	}
	if r[7]&4 != 0 {
		st = append(st, "forced_charge_request")
	}
	if r[7]&8 != 0 {
		st = append(st, "full_charge_request")
	}
	// Fault and warning codes are not decoded, so Warnings / Protections stay nil (unknown, not "none").
	b.Flags = &model.Flags{Status: st}
	return nil
}
