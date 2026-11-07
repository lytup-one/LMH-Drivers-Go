package rs485

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"lmhcan/internal/serialport"
)

func TestSniffSplitsOnIdleGap(t *testing.T) {
	f := &serialport.Fake{}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		n, _ := WriteSniff(ctx, f, 20*time.Millisecond, &out)
		done <- n
	}()

	f.Feed([]byte{0x01, 0x03, 0x00, 0x00})
	time.Sleep(5 * time.Millisecond)
	f.Feed([]byte{0x00, 0x0a, 0xc5, 0xcd}) // same request, short pause: one chunk
	time.Sleep(80 * time.Millisecond)
	f.Feed([]byte("~2002\r")) // reply after a long pause: second chunk
	time.Sleep(80 * time.Millisecond)
	cancel()

	if n := <-done; n != 2 {
		t.Fatalf("chunks = %d, want 2\n%s", n, out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.Contains(lines[0], "8 B  01 03 00 00 00 0a c5 cd") {
		t.Errorf("first line: %s", lines[0])
	}
	if !strings.Contains(lines[1], "6 B  7e 32 30 30 32 0d") || !strings.HasSuffix(lines[1], "|~2002.") {
		t.Errorf("second line: %s", lines[1])
	}
	if len(f.Writes) != 0 {
		t.Error("the sniffer must never write to the port")
	}
}
