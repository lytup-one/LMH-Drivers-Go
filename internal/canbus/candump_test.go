package canbus

import "testing"

func TestParseCandumpLine(t *testing.T) {
	f, ok := ParseCandumpLine("(1793290950.196751) can1 359#0200000001704E00")
	if !ok || f.ID != 0x359 || f.Extended || len(f.Data) != 8 || f.Data[0] != 0x02 || f.Data[6] != 0x4E {
		t.Fatalf("-L standard: %+v ok=%v", f, ok)
	}
	if f.Time.Unix() != 1793290950 {
		t.Errorf("time = %v", f.Time)
	}
	f, ok = ParseCandumpLine("(1.000000) can1 18FF50E5#0102")
	if !ok || f.ID != 0x18FF50E5 || !f.Extended || len(f.Data) != 2 {
		t.Fatalf("-L extended: %+v ok=%v", f, ok)
	}
	f, ok = ParseCandumpLine("  can1  351   [8]  20 02 00 00 D0 07 D6 01")
	if !ok || f.ID != 0x351 || len(f.Data) != 8 || f.Data[4] != 0xD0 {
		t.Fatalf("plain: %+v ok=%v", f, ok)
	}
	for _, bad := range []string{"", "garbage", "(1.0) can1 123##1DEADBEEF", "(1.0) can1 123#R", "(1.0) can1 123#ZZ"} {
		if _, ok := ParseCandumpLine(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
}
