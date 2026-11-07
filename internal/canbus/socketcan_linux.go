//go:build linux

package canbus

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"time"
	"unsafe"
)

const (
	afCAN           = 29  // AF_CAN
	canRaw          = 1   // CAN_RAW
	solCANRaw       = 101 // SOL_CAN_RAW
	canRawErrFilter = 2   // CAN_RAW_ERR_FILTER
	canEFF          = 0x80000000
	canRTR          = 0x40000000
	canERR          = 0x20000000
	canEFFMask      = 0x1FFFFFFF
	frameSize       = 16 // sizeof(struct can_frame)
)

// struct sockaddr_can, 16 bytes.
type sockaddrCAN struct {
	family  uint16
	_       uint16
	ifindex int32
	_       [8]byte
}

type socketBus struct {
	fd int
}

// OpenSocketCAN opens a raw SocketCAN socket on the named interface (can0, can1, ...).
// The interface must already be configured and up; this function never changes
// bitrate or interface state.
func OpenSocketCAN(ifname string) (Bus, error) {
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("interface %q not found: %w", ifname, err)
	}
	fd, err := syscall.Socket(afCAN, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, canRaw)
	if err != nil {
		return nil, fmt.Errorf("open CAN socket: %w", err)
	}
	sa := sockaddrCAN{family: afCAN, ifindex: int32(ifi.Index)}
	if _, _, e := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa)); e != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("bind %s: %w", ifname, e)
	}
	// Wake up regularly so Recv can notice context cancellation.
	tv := syscall.Timeval{Sec: 0, Usec: 200000}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("set receive timeout: %w", err)
	}
	// Deliver kernel error frames so bus problems can be counted.
	if err := syscall.SetsockoptInt(fd, solCANRaw, canRawErrFilter, canEFFMask); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("set error filter: %w", err)
	}
	return &socketBus{fd: fd}, nil
}

func (b *socketBus) Recv(ctx context.Context) (Frame, error) {
	buf := make([]byte, frameSize)
	for {
		if err := ctx.Err(); err != nil {
			return Frame{}, err
		}
		n, err := syscall.Read(b.fd, buf)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				continue
			}
			return Frame{}, err
		}
		if n < frameSize {
			continue
		}
		raw := binary.NativeEndian.Uint32(buf[0:4])
		if raw&canRTR != 0 {
			continue
		}
		dlc := int(buf[4])
		if dlc > 8 {
			dlc = 8
		}
		f := Frame{
			ID:       raw & canEFFMask,
			Extended: raw&canEFF != 0,
			Error:    raw&canERR != 0,
			Data:     append([]byte(nil), buf[8:8+dlc]...),
			Time:     time.Now(),
		}
		return f, nil
	}
}

func (b *socketBus) Send(f Frame) error {
	if len(f.Data) > 8 {
		return fmt.Errorf("classic CAN frame carries at most 8 bytes, got %d", len(f.Data))
	}
	buf := make([]byte, frameSize)
	id := f.ID
	if f.Extended {
		id |= canEFF
	}
	binary.NativeEndian.PutUint32(buf[0:4], id)
	buf[4] = byte(len(f.Data))
	copy(buf[8:], f.Data)
	_, err := syscall.Write(b.fd, buf)
	return err
}

func (b *socketBus) Close() error { return syscall.Close(b.fd) }
