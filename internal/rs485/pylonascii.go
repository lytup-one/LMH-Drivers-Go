package rs485

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Pylontech-style RS485 frames are ASCII:
//
//	~ VER ADR CID1 CID2 LENGTH INFO CHKSUM \r
//
// Every field except ~ and \r is written as hex ASCII. LENGTH is a 4-bit
// check (LCHKSUM) followed by the 12-bit INFO length in ASCII characters.
// CHKSUM is the two's complement 16-bit sum of the characters between ~ and
// CHKSUM. Requests carry a command in CID2; replies carry a return code there
// (00 = normal).

const (
	pylonSOI = '~'
	pylonEOI = '\r'
)

// PylonFrame is a decoded frame. Info holds the binary INFO bytes.
type PylonFrame struct {
	Ver, Adr, CID1, CID2 byte
	Info                 []byte
}

func pylonLenField(n int) string {
	d := []int{n >> 8 & 0xF, n >> 4 & 0xF, n & 0xF}
	chk := byte((d[0]+d[1]+d[2])%16) ^ 0xF
	chk = (chk + 1) & 0xF
	return fmt.Sprintf("%X%03X", chk, n)
}

func pylonChecksum(s string) string {
	sum := 0
	for i := 0; i < len(s); i++ {
		sum += int(s[i])
	}
	return fmt.Sprintf("%04X", uint16(^uint16(sum)+1))
}

// EncodePylon builds the frame bytes, including ~ and the final \r.
func EncodePylon(f PylonFrame) []byte {
	info := strings.ToUpper(hex.EncodeToString(f.Info))
	body := fmt.Sprintf("%02X%02X%02X%02X%s%s", f.Ver, f.Adr, f.CID1, f.CID2, pylonLenField(len(info)), info)
	return []byte(string(rune(pylonSOI)) + body + pylonChecksum(body) + string(rune(pylonEOI)))
}

// Errors from DecodePylon.
var (
	ErrPylonFraming  = errors.New("not a Pylon ASCII frame")
	ErrPylonLength   = errors.New("Pylon LENGTH check failed")
	ErrPylonChecksum = errors.New("Pylon CHKSUM mismatch")
)

// DecodePylon parses and validates one frame (leading ~, trailing \r).
func DecodePylon(raw []byte) (PylonFrame, error) {
	var f PylonFrame
	if len(raw) < 18 || raw[0] != pylonSOI || raw[len(raw)-1] != pylonEOI {
		return f, ErrPylonFraming
	}
	body := string(raw[1 : len(raw)-5])
	if pylonChecksum(body) != string(raw[len(raw)-5:len(raw)-1]) {
		return f, ErrPylonChecksum
	}
	head, err := hex.DecodeString(body[:8])
	if err != nil {
		return f, fmt.Errorf("%w: %v", ErrPylonFraming, err)
	}
	var n int
	if _, err := fmt.Sscanf(body[8:12], "%X", &n); err != nil {
		return f, ErrPylonLength
	}
	if pylonLenField(n&0xFFF) != body[8:12] || len(body)-12 != n&0xFFF {
		return f, ErrPylonLength
	}
	info, err := hex.DecodeString(body[12:])
	if err != nil {
		return f, fmt.Errorf("%w: INFO is not hex: %v", ErrPylonFraming, err)
	}
	return PylonFrame{Ver: head[0], Adr: head[1], CID1: head[2], CID2: head[3], Info: info}, nil
}
