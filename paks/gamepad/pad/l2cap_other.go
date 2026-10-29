//go:build !linux

package main

// Off Linux (tests on the Mac): no Bluetooth sockets.

import "errors"

var errNoL2CAP = errors.New("L2CAP sockets need Linux")

func l2capListen(uint16) (int, error)          { return -1, errNoL2CAP }
func l2capAccept(int) (int, bdaddr, error)     { return -1, bdaddr{}, errNoL2CAP }
func l2capConnect(bdaddr, uint16) (int, error) { return -1, errNoL2CAP }
func l2capRead(int, []byte) (int, error)       { return 0, errNoL2CAP }
