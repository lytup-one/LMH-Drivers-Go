package rs485

import (
	"testing"
	"time"

	"lmhcan/internal/model"
	"lmhcan/internal/serialport"
)

// Replies captured from the LYTVOLT S15000 (PACE BMS) set to Pylon, address 2,
// with `lmh-can probe /dev/ttyUSB4 -style pylon` on 2026-11-04. The checksum
// of each one is verified by DecodePylon, which also guards the transcription.
const (
	capVersion = "~200246000000FDB2\r"
	capMfr     = "~20024600C04034383130302D3935333801005368656E5A68656E205041434520202020202020F093\r"
	capAnalog  = "~2002460070720002100D320D300D2D0D2B0D340D300D320D320D320D2F0D300D2F0D300D2F0D2F0D32050C1A0BED0BEB0BF00BEBFFF9D3026BC4026D570032E48A\r"
	capLimits  = "~20024600B01402D480B798000007D040F970\r"
	capSerial  = "~20024600C02202475053523150433230304C5332383035F6C5\r"
)

func decodeCap(t *testing.T, s string) PylonFrame {
	t.Helper()
	f, err := DecodePylon([]byte(s))
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return f
}

func TestPylonCapturedFramesDecode(t *testing.T) {
	if f := decodeCap(t, capVersion); f.Ver != 0x20 || len(f.Info) != 0 {
		t.Errorf("version reply: %+v", f)
	}
	var b model.Battery
	if err := decodePylonAnalog(decodeCap(t, capAnalog).Info, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.CellsMV) != 16 || b.CellsMV[0] != 3378 || b.CellsMV[3] != 3371 || b.CellsMV[15] != 3378 {
		t.Errorf("cells = %v", b.CellsMV)
	}
	if *b.CellMinMV != 3371 || *b.CellMaxMV != 3380 || *b.CellDeltaMV != 9 {
		t.Errorf("cell min/max/delta = %v/%v/%v", *b.CellMinMV, *b.CellMaxMV, *b.CellDeltaMV)
	}
	wantT := []float64{36.7, 32.2, 32.0, 32.5, 32.0}
	if len(b.TempsC) != 5 {
		t.Fatalf("temps = %v", b.TempsC)
	}
	for i, w := range wantT {
		if b.TempsC[i] != w {
			t.Errorf("temp[%d] = %v, want %v", i, b.TempsC[i], w)
		}
	}
	if *b.CurrentA != -0.7 || *b.VoltageV != 54.018 || *b.RemainingAh != 275.88 || *b.FullAh != 279.91 || *b.Cycles != 50 {
		t.Errorf("I=%v V=%v rem=%v full=%v cycles=%v", *b.CurrentA, *b.VoltageV, *b.RemainingAh, *b.FullAh, *b.Cycles)
	}
	if *b.SOCPct != 98.6 {
		t.Errorf("derived SOC = %v, want 98.6", *b.SOCPct)
	}

	if err := decodePylonLimits(decodeCap(t, capLimits).Info, &b); err != nil {
		t.Fatal(err)
	}
	if *b.ChargeVoltageLimitV != 54.4 || *b.DischargeVoltageLimitV != 47 || *b.ChargeCurrentLimitA != 0 ||
		*b.DischargeCurrentLimitA != 200 || b.RequestFlagsRaw != "40" {
		t.Errorf("limits: %v %v %v %v %q", *b.ChargeVoltageLimitV, *b.DischargeVoltageLimitV, *b.ChargeCurrentLimitA, *b.DischargeCurrentLimitA, b.RequestFlagsRaw)
	}

	decodePylonManufacturer(decodeCap(t, capMfr).Info, &b)
	decodePylonSerial(decodeCap(t, capSerial).Info, &b)
	if b.ID != "48100-9538" || b.Manufacturer != "ShenZhen PACE" || b.Version != "1.0" || b.Serial != "GPSR1PC200LS2805" {
		t.Errorf("info: %q %q %q %q", b.ID, b.Manufacturer, b.Version, b.Serial)
	}
}

func TestPylonTruncatedAnalogIsAnError(t *testing.T) {
	info := decodeCap(t, capAnalog).Info
	for _, n := range []int{0, 2, 10, 40, len(info) - 1} {
		var b model.Battery
		if err := decodePylonAnalog(info[:n], &b); err == nil {
			t.Errorf("%d of %d bytes decoded without error", n, len(info))
		}
		if b.VoltageV != nil {
			t.Errorf("%d bytes: state must stay untouched on error", n)
		}
	}
}

// fakePylon answers like the BMS tested: address 2 only; 0x44 and 0x47 stay silent.
func fakePylon() *serialport.Fake {
	replies := map[byte]string{0x4F: capVersion, 0x51: capMfr, 0x42: capAnalog, 0x92: capLimits, 0x93: capSerial}
	return &serialport.Fake{Respond: func(req []byte) []byte {
		f, err := DecodePylon(req)
		if err != nil || f.Adr != 2 {
			return nil
		}
		return []byte(replies[f.CID2])
	}}
}

func TestPylonPoll(t *testing.T) {
	f := fakePylon()
	q := newPylon(f, 2)
	q.timeout, q.gap = 50*time.Millisecond, time.Millisecond
	var b model.Battery
	if err := q.Poll(&b); err != nil {
		t.Fatal(err)
	}
	if !b.Ready() || *b.SOCPct != 98.6 || *b.DischargeCurrentLimitA != 200 || b.Serial != "GPSR1PC200LS2805" || b.Manufacturer != "ShenZhen PACE" {
		t.Errorf("state = %+v", b)
	}
	if len(f.Writes) != 4 {
		t.Errorf("first poll: analog, limits, manufacturer, serial = 4 requests, got %d", len(f.Writes))
	}
	if err := q.Poll(&b); err != nil || len(f.Writes) != 6 {
		t.Errorf("later polls: 2 requests each; err=%v writes=%d", err, len(f.Writes))
	}
	if _, err := DecodePylon(f.Writes[0]); err != nil || string(f.Writes[0]) != "~20024642E00202FD33\r" {
		t.Errorf("analog request = %q", f.Writes[0])
	}
}

func TestPylonWrongAddressIsSilence(t *testing.T) {
	q := newPylon(fakePylon(), 1)
	q.timeout, q.gap = 20*time.Millisecond, time.Millisecond
	var b model.Battery
	if err := q.Poll(&b); err != ErrNoReply {
		t.Errorf("err = %v, want ErrNoReply", err)
	}
	if b.VoltageV != nil {
		t.Error("state must stay untouched")
	}
}
