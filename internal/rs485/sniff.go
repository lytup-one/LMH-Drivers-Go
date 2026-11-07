package rs485

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"lmhcan/internal/serialport"
)

// Chunk is a run of bytes that arrived without an idle gap: normally one
// request or one reply.
type Chunk struct {
	T    time.Time // when the first byte arrived
	Data []byte
}

const maxChunk = 4096

// Sniff reads p without ever writing and calls emit for each chunk of bytes
// separated from the next by at least gap of silence. It returns when ctx is
// cancelled (nil) or the port fails.
func Sniff(ctx context.Context, p serialport.Port, gap time.Duration, emit func(Chunk)) error {
	var buf []byte
	var first, last time.Time
	tmp := make([]byte, 512)
	flush := func() {
		if len(buf) > 0 {
			emit(Chunk{T: first, Data: buf})
			buf = nil
		}
	}
	for {
		if ctx.Err() != nil {
			flush()
			return nil
		}
		wait := 100 * time.Millisecond
		if len(buf) > 0 {
			wait = gap
		}
		if err := p.SetReadDeadline(time.Now().Add(wait)); err != nil {
			return err
		}
		n, err := p.Read(tmp)
		now := time.Now()
		if n > 0 {
			if len(buf) == 0 {
				first = now
			}
			buf = append(buf, tmp[:n]...)
			last = now
			if len(buf) >= maxChunk {
				flush()
			}
		}
		if err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				flush()
				return err
			}
			if len(buf) > 0 && now.Sub(last) >= gap {
				flush()
			}
		}
	}
}

// FormatChunk renders one chunk as a log line:
//
//	20:31:11.482  +0.103s   8 B  01 03 00 00 00 0a c5 cd  |........
//
// where +0.103s is the time since the previous chunk (0 for the first).
func FormatChunk(c Chunk, prev time.Time) string {
	dt := 0.0
	if !prev.IsZero() {
		dt = c.T.Sub(prev).Seconds()
	}
	hex := make([]string, len(c.Data))
	asc := make([]byte, len(c.Data))
	for i, b := range c.Data {
		hex[i] = fmt.Sprintf("%02x", b)
		asc[i] = '.'
		if b >= 0x20 && b < 0x7f {
			asc[i] = b
		}
	}
	return fmt.Sprintf("%s  +%.3fs  %4d B  %s  |%s", c.T.Format("15:04:05.000"), dt, len(c.Data), strings.Join(hex, " "), asc)
}

// WriteSniff runs Sniff and writes FormatChunk lines to w.
func WriteSniff(ctx context.Context, p serialport.Port, gap time.Duration, w io.Writer) (chunks int, err error) {
	var prev time.Time
	err = Sniff(ctx, p, gap, func(c Chunk) {
		fmt.Fprintln(w, FormatChunk(c, prev))
		prev = c.T
		chunks++
	})
	return chunks, err
}
