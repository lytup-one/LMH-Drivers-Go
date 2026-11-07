//go:build linux

package serialport

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
	"unsafe"
)

const (
	tcflsh   = 0x540B // TCFLSH
	tciflush = 0      // TCIFLUSH
	crtscts  = 0x80000000
	cbaud    = 0x100f
)

type linuxPort struct{ f *os.File }

// Open opens path in raw mode with the given line format.
func Open(path string, c Config) (Port, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := configure(f, c); err != nil {
		f.Close()
		return nil, fmt.Errorf("configure %s: %w", path, err)
	}
	return &linuxPort{f: f}, nil
}

func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	return rawIoctl(f, req, func() uintptr { return uintptr(arg) })
}

func ioctlInt(f *os.File, req uintptr, arg uintptr) error {
	return rawIoctl(f, req, func() uintptr { return arg })
}

func rawIoctl(f *os.File, req uintptr, arg func() uintptr) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if cerr := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg())
	}); cerr != nil {
		return cerr
	}
	if errno != 0 {
		return errno
	}
	return nil
}

func configure(f *os.File, c Config) error {
	var t syscall.Termios
	if err := ioctl(f, syscall.TCGETS, unsafe.Pointer(&t)); err != nil {
		return err
	}
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF | syscall.IXANY | syscall.INPCK
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.PARODD | syscall.CSTOPB | cbaud | crtscts
	t.Cflag |= syscall.CS8 | syscall.CREAD | syscall.CLOCAL | baudBits[c.Baud]
	switch c.Parity {
	case 'E':
		t.Cflag |= syscall.PARENB
	case 'O':
		t.Cflag |= syscall.PARENB | syscall.PARODD
	}
	if c.StopBits == 2 {
		t.Cflag |= syscall.CSTOPB
	}
	// VMIN=1 with O_NONBLOCK: an empty read returns EAGAIN (waits in the Go poller, honours
	// deadlines). VMIN=0 would make read return 0, which Go reports as EOF.
	t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
	return ioctl(f, syscall.TCSETS, unsafe.Pointer(&t))
}

func (p *linuxPort) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *linuxPort) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *linuxPort) SetReadDeadline(t time.Time) error {
	return p.f.SetReadDeadline(t)
}
func (p *linuxPort) ResetInput() error {
	return ioctlInt(p.f, tcflsh, tciflush)
}
func (p *linuxPort) Close() error { return p.f.Close() }

// List returns the serial devices that can be RS485 ports on the LMH
// (/dev/ttyACM*, /dev/ttyUSB*), sorted.
func List() []string {
	var out []string
	for _, pat := range []string{"/dev/ttyACM*", "/dev/ttyUSB*"} {
		m, _ := filepath.Glob(pat)
		out = append(out, m...)
	}
	sort.Strings(out)
	return out
}
