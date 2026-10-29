package main

// Bluetooth classic L2CAP sockets through raw syscalls: the syscall package
// has no sockaddr_l2 and its Sockaddr interface cannot be implemented
// outside the package.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	afBluetooth   = 31
	btprotoL2CAP  = 0
	solBluetooth  = 274
	btSecurity    = 4
	btSecMedium   = 2 // encrypted link, any pairing (Just Works included)
	psmHIDControl = 0x11
	psmHIDIntr    = 0x13
)

// bdaddr is stored as the kernel does: least significant byte first.
type bdaddr [6]byte

func parseAddr(s string) (bdaddr, error) {
	var a bdaddr
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return a, fmt.Errorf("bad address %q", s)
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return a, fmt.Errorf("bad address %q", s)
		}
		a[5-i] = byte(n)
	}
	return a, nil
}

func (a bdaddr) String() string {
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", a[5], a[4], a[3], a[2], a[1], a[0])
}

// devicePath is bluetoothd's object for a remote device on hci0.
func (a bdaddr) devicePath() objectPath {
	return objectPath("/org/bluez/hci0/dev_" + strings.ReplaceAll(a.String(), ":", "_"))
}

// sockaddrL2 is struct sockaddr_l2 (14 bytes, packed).
func sockaddrL2(psm uint16, a bdaddr) [14]byte {
	var s [14]byte
	s[0], s[1] = afBluetooth, 0
	s[2], s[3] = byte(psm), byte(psm>>8)
	copy(s[4:10], a[:])
	return s // cid 0, BDADDR_BREDR
}

var errClosed = errors.New("channel closed")
