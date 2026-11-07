package rs485

import (
	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
)

// wow polls a BMS in "WOW" RS485 mode: Modbus RTU, holding registers (function 3).
//
// A register dump of the LYTVOLT S15000 in this mode (9600 8N1, slave 1) shows
// that addresses 0..39 hold the same block as the PACE Modbus map, with the
// PACE scales: current 0.01 A, voltage 0.01 V, cells in mV, temperatures
// 0.1 degC, limits 0.1 V / 0.1 A. The vendor's "WOW Comprehensive Modbus Map"
// disagrees (current 0.1 A, cells 0.01 V) and its "limit" registers 62, 70,
// 78 and 81 hold protection thresholds in other units, so that table is not
// used. Cross-checks against Pylon replies from the same BMS: pack voltage
// equals the sum of the cells, full capacity 279.91 Ah, 50 cycles, current
// -0.7 A, charge limit 54.4 V, charge current limit 0 A, discharge limit 200 A.
//
// The flag registers (9, 10, 11) are reported raw only: their bit meanings in
// this mode have not been confirmed. Version / serial strings are not read.
type wow struct {
	c     *modbus.Client
	slave byte
}

func newWow(c *modbus.Client, slave byte) *wow { return &wow{c: c, slave: slave} }

func (w *wow) Poll(b *model.Battery) error {
	r, err := w.c.ReadHolding(w.slave, 0, paceRegs)
	if err != nil {
		return err
	}
	nb := *b
	decodePace(r, &nb)
	nb.Flags = nil
	*b = nb
	return nil
}
