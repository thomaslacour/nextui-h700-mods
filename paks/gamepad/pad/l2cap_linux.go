//go:build linux

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

func l2capSocket() (int, error) {
	fd, err := syscall.Socket(afBluetooth, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, btprotoL2CAP)
	if err != nil {
		return -1, fmt.Errorf("l2cap socket: %w", err)
	}
	sec := [2]byte{btSecMedium, 0} // struct bt_security {level, key_size}
	if _, _, e := syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd), solBluetooth, btSecurity,
		uintptr(unsafe.Pointer(&sec[0])), 2, 0); e != 0 {
		syscall.Close(fd)
		return -1, fmt.Errorf("l2cap security: %w", e)
	}
	return fd, nil
}

// l2capListen listens on psm for any local adapter.
func l2capListen(psm uint16) (int, error) {
	fd, err := l2capSocket()
	if err != nil {
		return -1, err
	}
	sa := sockaddrL2(psm, bdaddr{})
	if _, _, e := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(&sa[0])), uintptr(len(sa))); e != 0 {
		syscall.Close(fd)
		if e == syscall.EADDRINUSE {
			return -1, fmt.Errorf("PSM 0x%02x is taken (bluetoothd input plugin still on?): %w", psm, e)
		}
		return -1, fmt.Errorf("bind PSM 0x%02x: %w", psm, e)
	}
	// accept gives up every 250 ms (EAGAIN) so its loop can stop: a close
	// does not wake a blocked accept, and the PSM stays bound until it returns.
	tv := syscall.Timeval{Usec: 250000}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		syscall.Close(fd)
		return -1, fmt.Errorf("accept timeout: %w", err)
	}
	if err := syscall.Listen(fd, 1); err != nil {
		syscall.Close(fd)
		return -1, fmt.Errorf("listen PSM 0x%02x: %w", psm, err)
	}
	return fd, nil
}

func l2capAccept(fd int) (int, bdaddr, error) {
	var sa [14]byte
	n := uint32(len(sa))
	for {
		c, _, e := syscall.Syscall6(syscall.SYS_ACCEPT4, uintptr(fd), uintptr(unsafe.Pointer(&sa[0])),
			uintptr(unsafe.Pointer(&n)), syscall.SOCK_CLOEXEC, 0, 0)
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return -1, bdaddr{}, e
		}
		var a bdaddr
		copy(a[:], sa[4:10])
		return int(c), a, nil
	}
}

// l2capConnect opens an outgoing channel (blocking; the kernel gives up
// after its page timeout, ~5 s, when the host is away).
func l2capConnect(a bdaddr, psm uint16) (int, error) {
	fd, err := l2capSocket()
	if err != nil {
		return -1, err
	}
	sa := sockaddrL2(psm, a)
	for {
		_, _, e := syscall.Syscall(syscall.SYS_CONNECT, uintptr(fd), uintptr(unsafe.Pointer(&sa[0])), uintptr(len(sa)))
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			syscall.Close(fd)
			return -1, e
		}
		return fd, nil
	}
}

func l2capRead(fd int, b []byte) (int, error) {
	for {
		n, err := syscall.Read(fd, b)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, errClosed
		}
		return n, nil
	}
}
