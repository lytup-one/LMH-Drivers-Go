package rs485

import (
	"errors"
	"testing"
)

func TestPylonRequestMatchesPublishedExample(t *testing.T) {
	// "Get analog value", pack 2, as printed in the Pylontech RS485 protocol
	// document: ~20024642E00202FD33<CR>
	got := string(EncodePylon(PylonFrame{Ver: 0x20, Adr: 0x02, CID1: 0x46, CID2: 0x42, Info: []byte{0x02}}))
	if want := "~20024642E00202FD33\r"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPylonRoundTripAndErrors(t *testing.T) {
	in := PylonFrame{Ver: 0x20, Adr: 0x02, CID1: 0x46, CID2: 0x00, Info: []byte{1, 2, 3, 0xAB, 0xFF}}
	raw := EncodePylon(in)
	out, err := DecodePylon(raw)
	if err != nil || out.Ver != 0x20 || out.Adr != 2 || out.CID1 != 0x46 || out.CID2 != 0 || string(out.Info) != string(in.Info) {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	empty, err := DecodePylon(EncodePylon(PylonFrame{Ver: 0x20, Adr: 2, CID1: 0x46, CID2: 0x4F}))
	if err != nil || len(empty.Info) != 0 {
		t.Errorf("empty INFO: %+v %v", empty, err)
	}

	bad := append([]byte(nil), raw...)
	bad[10] ^= 1 // corrupt a payload character
	if _, err := DecodePylon(bad); !errors.Is(err, ErrPylonChecksum) {
		t.Errorf("corrupt payload: %v", err)
	}
	if _, err := DecodePylon([]byte("hello\r")); !errors.Is(err, ErrPylonFraming) {
		t.Errorf("not a frame: %v", err)
	}
	// wrong LENGTH check nibble but otherwise valid checksum
	body := "20024600" + "1002" + "0102"
	frame := "~" + body + pylonChecksum(body) + "\r"
	if _, err := DecodePylon([]byte(frame)); !errors.Is(err, ErrPylonLength) {
		t.Errorf("bad LENGTH: %v", err)
	}
}
