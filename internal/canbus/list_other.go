//go:build !linux

package canbus

type Link struct {
	Name  string
	State string
}

func ListCAN() []Link { return nil }
