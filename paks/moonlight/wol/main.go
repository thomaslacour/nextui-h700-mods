// wol <mac> [adresse de diffusion…] : réveille un PC par Wake-on-LAN.
//
// Paquet magique (6 × 0xFF puis 16 fois l'adresse MAC) envoyé en UDP, en
// diffusion, sur les ports 9 et 7, à chaque adresse donnée (par défaut
// 255.255.255.255). Utilisé par le pak Moonlight (Wake PC, et réveil avant un
// stream). Code de sortie 0 si au moins un envoi a réussi.
package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wol <mac> [broadcast…]")
		os.Exit(2)
	}
	mac, err := net.ParseMAC(os.Args[1])
	if err != nil || len(mac) != 6 {
		fmt.Fprintln(os.Stderr, "wol: bad MAC address:", os.Args[1])
		os.Exit(2)
	}
	packet := append(bytes.Repeat([]byte{0xff}, 6), bytes.Repeat(mac, 16)...)
	targets := os.Args[2:]
	if len(targets) == 0 {
		targets = []string{"255.255.255.255"}
	}

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "wol:", err)
		os.Exit(1)
	}
	defer conn.Close()
	if raw, err := conn.(*net.UDPConn).SyscallConn(); err == nil {
		raw.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
		})
	}

	sent := 0
	for _, t := range targets {
		for _, port := range []int{9, 7} {
			addr := &net.UDPAddr{IP: net.ParseIP(t), Port: port}
			if addr.IP == nil {
				continue
			}
			if _, err := conn.WriteTo(packet, addr); err == nil {
				sent++
			} else {
				fmt.Fprintf(os.Stderr, "wol: %s:%d: %v\n", t, port, err)
			}
		}
	}
	fmt.Printf("wol: magic packet for %s sent %d time(s)\n", mac, sent)
	if sent == 0 {
		os.Exit(1)
	}
}
