package protocol

import (
	"bufio"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"testing"

	"lmhcan/internal/canbus"
	"lmhcan/internal/model"
)

// replay decodes a deduplicated `candump -L` capture of the BMS in one mode.
func replay(t *testing.T, name string) (model.Battery, map[uint32]bool) {
	t.Helper()
	info, err := Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open("testdata/" + name + ".log")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	dec := info.New()
	var b model.Battery
	undecoded := map[uint32]bool{}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		f, ok := canbus.ParseCandumpLine(sc.Text())
		if !ok {
			continue
		}
		if !dec.Decode(f, &b) {
			undecoded[f.ID] = true
		}
	}
	return b, undecoded
}

func near(t *testing.T, name string, got *float64, want, tol float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s missing", name)
		return
	}
	if math.Abs(*got-want) > tol {
		t.Errorf("%s = %v, want %v +/- %v", name, *got, want, tol)
	}
}

// Every capture is the same battery within a few minutes, so the physical
// values must agree across all dialects. This is the main cross-check.
func TestAllCapturedDialectsAgreeOnTheBattery(t *testing.T) {
	for _, info := range All() {
		// Pylontech has no log of its own: it is covered by TestPylonExactValues.
		if info.Level < Captured || info.Name == "pylontech" {
			continue
		}
		t.Run(info.Name, func(t *testing.T) {
			b, _ := replay(t, info.Name)
			near(t, "voltage_v", b.VoltageV, 54.7, 1.0)
			near(t, "current_a", b.CurrentA, -0.7, 0.05)
			near(t, "soc_pct", b.SOCPct, 100, 0.01)
			near(t, "temperature_c", b.TemperatureC, 32.5, 0.5)
			near(t, "discharge_current_limit_a", b.DischargeCurrentLimitA, 200, 0.01)
			near(t, "charge_current_limit_a", b.ChargeCurrentLimitA, 0, 0.01)
			near(t, "charge_voltage_limit_v", b.ChargeVoltageLimitV, 54.8, 0.5)
			if info.Name != "srd" {
				near(t, "soh_pct", b.SOHPct, 100, 0.01)
			} else if b.SOHPct != nil {
				t.Errorf("srd reports no SOH, got %v", *b.SOHPct)
			}
			if !b.Ready() {
				t.Error("not ready")
			}
		})
	}
}

func TestCellExtremesAndCapacityAcrossDialects(t *testing.T) {
	// dialects that carry min/max cell voltage: the battery's cells sit at ~3.41-3.44 V
	for _, n := range []string{"goodwe", "studer", "growatt", "luxpower", "victron", "victron23", "megarevo", "senergy", "schneider"} {
		b, _ := replay(t, n)
		near(t, n+" cell_min_mv", b.CellMinMV, 3420, 30)
		near(t, n+" cell_max_mv", b.CellMaxMV, 3430, 30)
		if b.CellMinMV != nil && b.CellMaxMV != nil && *b.CellMinMV > *b.CellMaxMV {
			t.Errorf("%s: min %v > max %v", n, *b.CellMinMV, *b.CellMaxMV)
		}
	}
	// dialects that carry a nominal capacity: 279-280 Ah
	for _, n := range []string{"luxpower", "victron", "victron23", "megarevo", "senergy", "sma", "srd", "sofar", "pv"} {
		b, _ := replay(t, n)
		near(t, n+" installed_ah", b.InstalledAh, 279.5, 1)
	}
}

// Frames captured from the LYTVOLT S15000 on can1 (Pylontech mode, 500 kbit/s).
func TestPylonExactValues(t *testing.T) {
	var b model.Battery
	d := dialect{tPylon()}
	for _, c := range []struct {
		id uint32
		h  string
	}{
		{0x359, "0200000001704E00"},
		{0x351, "20020000D007D601"},
		{0x355, "6300640000000000"},
		{0x356, "DB15F9FF4C010000"},
		{0x35C, "0000000000000000"},
		{0x35E, "50594C4F4E202020"},
	} {
		data, _ := hex.DecodeString(c.h)
		if !d.Decode(canbus.Frame{ID: c.id, Data: data}, &b) {
			t.Fatalf("frame 0x%X not decoded", c.id)
		}
	}
	near(t, "voltage", b.VoltageV, 55.95, 1e-9)
	near(t, "current", b.CurrentA, -0.7, 1e-9)
	near(t, "temperature", b.TemperatureC, 33.2, 1e-9)
	near(t, "soc", b.SOCPct, 99, 1e-9)
	near(t, "cvl", b.ChargeVoltageLimitV, 54.4, 1e-9)
	near(t, "dcl", b.DischargeCurrentLimitA, 200, 1e-9)
	near(t, "dvl", b.DischargeVoltageLimitV, 47, 1e-9)
	if b.ProtectionRaw != "0200" || b.RequestFlagsRaw != "0000" || b.ID != "pN" || b.Manufacturer != "PYLON" || *b.Modules != 1 {
		t.Errorf("raw/id/manufacturer: %+v", b)
	}
}

func TestGrowattDetails(t *testing.T) {
	b, undecoded := replay(t, "growatt")
	if b.Cycles == nil || *b.Cycles != 50 {
		t.Errorf("cycles = %v, want 50 (RS485 reads 50 for this battery)", b.Cycles)
	}
	near(t, "full_ah", b.FullAh, 279.91, 1e-9)
	near(t, "remaining_ah", b.RemainingAh, 279.35, 0.1)
	n := b.Normalized()
	if len(n.CellsMV) != 16 {
		t.Fatalf("cells = %v", n.CellsMV)
	}
	for i, c := range n.CellsMV {
		if c < 3400 || c > 3450 {
			t.Errorf("cell %d = %d mV out of range", i+1, c)
		}
	}
	for _, id := range []uint32{0x311, 0x313, 0x314, 0x315, 0x316, 0x317, 0x318, 0x319, 0x322} {
		if undecoded[id] {
			t.Errorf("0x%X should be decoded", id)
		}
	}
	// a partly received cell list must not be published
	var half model.Battery
	half.CellsMV = make([]int, 16)
	half.CellsMV[0] = 3400
	if half.Normalized().CellsMV != nil {
		t.Error("incomplete cell list must be dropped")
	}
}

func TestSchneiderDetails(t *testing.T) {
	b, _ := replay(t, "schneider")
	if b.Serial != "GPSR1PC200LS28051029" {
		t.Errorf("serial = %q, want the pack serial the RS485 reader shows", b.Serial)
	}
	if b.Cycles == nil || *b.Cycles != 50 {
		t.Errorf("cycles = %v, want 50", b.Cycles)
	}
	near(t, "full_ah", b.FullAh, 279.9, 0.05)
	near(t, "voltage", b.VoltageV, 54.68, 0.01)
	near(t, "current", b.CurrentA, -0.71, 0.011)
	near(t, "dvl", b.DischargeVoltageLimitV, 47, 0.01)
	// 29-bit frames only: the same IDs as standard frames must be ignored
	var x model.Battery
	if (extDialect{newSchneider().(*schneider).t}).Decode(canbus.Frame{ID: 0x323, Data: make([]byte, 8)}, &x) {
		t.Error("standard-ID frame must not decode as Schneider")
	}
}

func TestLuxpowerCycles(t *testing.T) {
	b, _ := replay(t, "luxpower")
	if b.Cycles == nil || *b.Cycles != 50 {
		t.Errorf("cycles = %v, want 50", b.Cycles)
	}
}

func TestOptionalValuesNotReportedAsZero(t *testing.T) {
	b, _ := replay(t, "goodwe")
	if b.DischargeVoltageLimitV != nil {
		t.Errorf("goodwe mode sends no discharge voltage limit, got %v", *b.DischargeVoltageLimitV)
	}
}

func TestIgnoresUnknownShortAndWrongIDType(t *testing.T) {
	var b model.Battery
	d := dialect{tPylon()}
	if d.Decode(canbus.Frame{ID: 0x305, Data: make([]byte, 8)}, &b) {
		t.Error("0x305 keepalive must not be decoded")
	}
	if d.Decode(canbus.Frame{ID: 0x356, Data: []byte{0xDB, 0x15}}, &b) {
		t.Error("short 0x356 must be rejected")
	}
	data, _ := hex.DecodeString("DB15F9FF4C010000")
	if d.Decode(canbus.Frame{ID: 0x356, Extended: true, Data: data}, &b) {
		t.Error("extended-ID frame must be ignored by a standard-ID dialect")
	}
	if b.VoltageV != nil {
		t.Error("state must stay untouched")
	}
}

func TestRegistry(t *testing.T) {
	if info, err := Lookup("Pylon"); err != nil || info.Name != "pylontech" || info.Level != Verified {
		t.Errorf("pylon alias: %v %v", info, err)
	}
	for alias, want := range map[string]string{"jin_lang": "jinlang", "senergyinv": "senergy", "tbb_lithium": "tbb"} {
		if info, err := Lookup(alias); err != nil || info.Name != want {
			t.Errorf("alias %s: %v %v", alias, info, err)
		}
	}
	if _, err := Lookup("deye"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("deye must be reported as not implemented, got %v", err)
	}
	if _, err := Lookup("nonsense"); err == nil || errors.Is(err, ErrNotImplemented) {
		t.Errorf("unknown protocol must be a plain error, got %v", err)
	}
	for _, i := range All() {
		if (i.New == nil) != (i.Level == Unsupported) {
			t.Errorf("%s: decoder presence does not match level", i.Name)
		}
		if i.New != nil && i.New() == nil {
			t.Errorf("%s: New returned nil", i.Name)
		}
	}
}
