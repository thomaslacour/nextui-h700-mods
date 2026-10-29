package main

// Console buttons from evdev, "ANBERNIC-keys" node (same codes as
// paks/music/player/input.go and paks/moonlight/launch.sh). External pads
// are not mirrored: only the console's own keys drive the host.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type evKey struct {
	code  uint16
	value int32 // 1 pressed, 0 released (autorepeat dropped)
}

const (
	evKeyType = 1
	evAbsType = 3
	absHat0X  = 16
	absHat0Y  = 17

	codeA      = 304
	codeB      = 305
	codeY      = 306
	codeX      = 307
	codeL1     = 308
	codeR1     = 309
	codeSelect = 310
	codeStart  = 311
	codeMenu   = 312
	codeL2     = 314
	codeR2     = 315

	// Pseudo codes for the d-pad (KEY_* and BTN_DPAD_* fold into these).
	codeUp    = 103
	codeDown  = 108
	codeLeft  = 105
	codeRight = 106
)

// Position-based: the SP's bottom face button is B, the host's south is A.
var hidButtonOf = map[uint16]int{
	codeB: hidSouth, codeA: hidEast, codeY: hidWest, codeX: hidNorth,
	codeL1: hidL1, codeR1: hidR1, codeL2: hidL2, codeR2: hidR2,
	codeSelect: hidSelect, codeStart: hidStart,
}

func foldDpad(code uint16) uint16 {
	switch code {
	case 544:
		return codeUp
	case 545:
		return codeDown
	case 546:
		return codeLeft
	case 547:
		return codeRight
	}
	return code
}

var reEvent = regexp.MustCompile(`event[0-9]+`)

// consoleInputs lists the event nodes of the console's own buttons.
func consoleInputs() []string {
	data, _ := os.ReadFile("/proc/bus/input/devices")
	var out []string
	for _, block := range strings.Split(string(data), "\n\n") {
		l := strings.ToLower(block)
		if !strings.Contains(l, "anbernic-keys") && !strings.Contains(l, "keys-polled") {
			continue
		}
		for _, ev := range reEvent.FindAllString(block, -1) {
			out = append(out, "/dev/input/"+ev)
		}
	}
	if len(out) == 0 {
		out, _ = filepath.Glob("/dev/input/event*")
	}
	return out
}

// hatToKeys turns ABS_HAT0X/Y values into d-pad key changes.
func hatToKeys(code uint16, value int32, prev map[uint16]int32) []evKey {
	neg, pos := uint16(codeLeft), uint16(codeRight)
	if code == absHat0Y {
		neg, pos = codeUp, codeDown
	}
	old := prev[code]
	prev[code] = value
	var out []evKey
	if old < 0 && value >= 0 {
		out = append(out, evKey{neg, 0})
	}
	if old > 0 && value <= 0 {
		out = append(out, evKey{pos, 0})
	}
	if value < 0 && old >= 0 {
		out = append(out, evKey{neg, 1})
	}
	if value > 0 && old <= 0 {
		out = append(out, evKey{pos, 1})
	}
	return out
}

// decodeEvents parses aarch64 input_event records (24 bytes each).
func decodeEvents(b []byte, hats map[uint16]int32, emit func(evKey)) {
	for i := 0; i+24 <= len(b); i += 24 {
		typ := binary.LittleEndian.Uint16(b[i+16:])
		code := binary.LittleEndian.Uint16(b[i+18:])
		value := int32(binary.LittleEndian.Uint32(b[i+20:]))
		switch {
		case typ == evKeyType && value != 2:
			emit(evKey{foldDpad(code), value})
		case typ == evAbsType && (code == absHat0X || code == absHat0Y):
			for _, k := range hatToKeys(code, value, hats) {
				emit(k)
			}
		}
	}
}

// listenConsole feeds ch from every console node; returns how many opened.
func listenConsole(ch chan<- evKey) int {
	opened := 0
	for _, p := range consoleInputs() {
		fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		opened++
		go func(fd int) {
			defer syscall.Close(fd)
			hats := map[uint16]int32{}
			var b [24 * 16]byte
			for {
				n, err := syscall.Read(fd, b[:])
				if err == syscall.EINTR {
					continue
				}
				if err != nil || n <= 0 {
					return
				}
				decodeEvents(b[:n], hats, func(k evKey) { ch <- k })
			}
		}(fd)
	}
	return opened
}

// keyFilter applies the pak's own combos on top of the raw keys:
// SELECT + START quits, as in Tools > Input (both keys still reach the
// host); MENU + START turns the Bluetooth pad on or off and MENU + SELECT
// switches the d-pad between d-pad and left stick (START / SELECT are not
// sent); MENU alone, tapped, is the host's Home button (sent on release so
// the combos never reach the host).
type keyFilter struct {
	menuHeld, menuUsed    bool
	selectHeld, startHeld bool
}

type filterOut struct {
	quit, home, toggleBT, toggleStick bool
	pass                              bool // forward k to the pad state
}

func (f *keyFilter) feed(k evKey) filterOut {
	if k.code == codeMenu {
		if k.value != 0 {
			f.menuHeld, f.menuUsed = true, false
			return filterOut{}
		}
		home := f.menuHeld && !f.menuUsed
		f.menuHeld = false
		return filterOut{home: home}
	}
	if f.menuHeld && k.value != 0 {
		f.menuUsed = true
		if k.code == codeStart {
			return filterOut{toggleBT: true}
		}
		if k.code == codeSelect {
			return filterOut{toggleStick: true}
		}
	}
	switch k.code {
	case codeSelect:
		f.selectHeld = k.value != 0
		if f.selectHeld && f.startHeld {
			return filterOut{quit: true, pass: true}
		}
	case codeStart:
		f.startHeld = k.value != 0
		if f.selectHeld && f.startHeld {
			return filterOut{quit: true, pass: true}
		}
	}
	return filterOut{pass: true}
}

// apply sets one console key on the pad state; false if the key is unused.
func (s *padState) apply(k evKey) bool {
	on := k.value != 0
	switch k.code {
	case codeUp:
		s.up = on
	case codeDown:
		s.down = on
	case codeLeft:
		s.left = on
	case codeRight:
		s.right = on
	default:
		b, ok := hidButtonOf[k.code]
		if !ok {
			return false
		}
		s.set(b, on)
	}
	return true
}
