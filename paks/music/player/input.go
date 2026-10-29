package main

// Console buttons, read straight from evdev. Codes are NextUI's on the H700
// "ANBERNIC-keys" node (same table as paks/moonlight/launch.sh):
// 304 A, 305 B, 306 Y, 307 X, 308 L1, 309 R1, 310 SELECT, 311 START,
// 312 MENU, 314 L2, 315 R2. The power key comes from axp2202-pek (116).
// External pads (USB/Bluetooth) are read too, by button position (padState).
// Volume keys stay NextUI's (keymon, which keeps running): the player only
// reads them to show the volume bar.

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type key string

const (
	kUp      key = "up"
	kDown    key = "down"
	kLeft    key = "left"
	kRight   key = "right"
	kA       key = "A"
	kB       key = "B"
	kX       key = "X"
	kY       key = "Y"
	kL1      key = "L1"
	kR1      key = "R1"
	kL2      key = "L2"
	kR2      key = "R2"
	kSelect  key = "SELECT"
	kStart   key = "START"
	kMenu    key = "MENU"
	kPower   key = "POWER"
	kVolUp   key = "VOL+" // only shown: NextUI's keymon changes the volume
	kVolDown key = "VOL-"
)

type keyEvent struct {
	k       key
	pressed bool
}

var evdevKeys = map[uint16]key{
	103: kUp, 108: kDown, 105: kLeft, 106: kRight, // KEY_* d-pad
	544: kUp, 545: kDown, 546: kLeft, 547: kRight, // BTN_DPAD_*
	304: kA, 305: kB, 306: kY, 307: kX,
	308: kL1, 309: kR1, 310: kSelect, 311: kStart, 312: kMenu,
	314: kL2, 315: kR2,
	316: kMenu, // Home of an external pad
	116: kPower,
	115: kVolUp, 114: kVolDown,
}

func translateKey(code uint16, value int32) (keyEvent, bool) {
	k, ok := evdevKeys[code]
	if !ok || value == 2 { // kernel autorepeat: we repeat ourselves
		return keyEvent{}, false
	}
	return keyEvent{k, value != 0}, true
}

// hatEvents turns ABS_HAT0X/Y changes into press/release of a direction.
func hatEvents(code uint16, value int32, prev key) (key, []keyEvent) {
	var next key
	switch {
	case code == 17 && value < 0:
		next = kUp
	case code == 17 && value > 0:
		next = kDown
	case code == 16 && value < 0:
		next = kLeft
	case code == 16 && value > 0:
		next = kRight
	}
	if next == prev {
		return next, nil
	}
	var evs []keyEvent
	if prev != "" {
		evs = append(evs, keyEvent{prev, false})
	}
	if next != "" {
		evs = append(evs, keyEvent{next, true})
	}
	return next, evs
}

func repeatable(k key) bool { return k == kUp || k == kDown || k == kLeft || k == kRight }

const (
	repeatDelay      = 300 * time.Millisecond
	repeatPeriod     = 85 * time.Millisecond
	repeatFastAfter  = 1500 * time.Millisecond
	repeatFastPeriod = 45 * time.Millisecond
)

type held struct{ start, next time.Time }

type repeater struct{ active map[key]held }

func newRepeater() *repeater { return &repeater{active: map[key]held{}} }

func (r *repeater) update(ev keyEvent, now time.Time) {
	if !repeatable(ev.k) {
		return
	}
	if !ev.pressed {
		delete(r.active, ev.k)
		return
	}
	if _, ok := r.active[ev.k]; !ok {
		r.active[ev.k] = held{now, now.Add(repeatDelay)}
	}
}

func (r *repeater) due(now time.Time) []key {
	var out []key
	for _, k := range []key{kUp, kDown, kLeft, kRight} {
		h, ok := r.active[k]
		if !ok || now.Before(h.next) {
			continue
		}
		out = append(out, k)
		p := repeatPeriod
		if now.Sub(h.start) >= repeatFastAfter {
			p = repeatFastPeriod
		}
		h.next = now.Add(p)
		r.active[k] = h
	}
	return out
}

var (
	reEvent    = regexp.MustCompile(`event[0-9]+`)
	reJoystick = regexp.MustCompile(`handlers=[^\n]*\bjs[0-9]`)
)

// device is one input node: the console's own keys, or an external pad
// (USB or Bluetooth; anything with a joystick handler besides the console).
type device struct {
	path string
	pad  bool
}

func listDevices() []device {
	data, _ := os.ReadFile("/proc/bus/input/devices")
	var out []device
	seen := map[string]bool{}
	for _, block := range strings.Split(string(data), "\n\n") {
		l := strings.ToLower(block)
		console := strings.Contains(l, "anbernic-keys") || strings.Contains(l, "pek") || strings.Contains(l, "keys-polled")
		pad := !console && reJoystick.MatchString(l)
		if !console && !pad {
			continue
		}
		for _, ev := range reEvent.FindAllString(block, -1) {
			p := "/dev/input/" + ev
			if !seen[p] {
				seen[p] = true
				out = append(out, device{p, pad})
			}
		}
	}
	return out
}

// inputPaths: every node read (diagnostics, --keys).
func inputPaths() []string {
	var out []string
	for _, d := range listDevices() {
		out = append(out, d.path)
	}
	return out
}

// rawEvent is one decoded input_event (aarch64 layout: 16-byte timeval).
type rawEvent struct {
	typ, code uint16
	value     int32
}

func readEvents(fd int, emit func(rawEvent)) {
	var b [24 * 16]byte
	for {
		n, err := syscall.Read(fd, b[:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			return
		}
		for i := 0; i+24 <= n; i += 24 {
			e := b[i : i+24]
			emit(rawEvent{le16(e, 16), le16(e, 18), int32(le32(e, 20))})
		}
	}
}

// External pads, by button position, as paks/ports/padmenu (the bottom
// button confirms, like NextUI's menus with an Xbox-style pad): Linux codes
// of the pad -> console codes, then evdevKeys.
var padKeys = map[uint16]uint16{ // 8BitDo SN30 Pro and other D-input pads
	305: 304, 304: 305, 308: 307, 307: 306, // bottom A, right B, left X, top Y
	310: 308, 311: 309, 312: 314, 313: 315, // L1 R1 L2 R2
	314: 310, 315: 311, 316: 312, // SELECT START Home->MENU
}

var xboxPadKeys = map[uint16]uint16{ // Microsoft (Xbox) pads
	304: 304, 305: 305, 307: 307, 308: 306,
	310: 308, 311: 309, 314: 310, 315: 311, 316: 312,
}

const (
	absX, absY, absBrake, absGas = 0, 1, 10, 9
	ioctlEVIOCGABS               = 0x80184540
	ioctlEVIOCGID                = 0x80084502
	vendorMicrosoft              = 0x045e
)

type absInfo struct{ value, min, max, fuzz, flat, res int32 }

func axisRange(fd int, axis uintptr) (int32, int32) {
	var a absInfo
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlEVIOCGABS+axis, uintptr(unsafe.Pointer(&a))); e != 0 || a.max <= a.min {
		return -32768, 32767
	}
	return a.min, a.max
}

func padVendor(fd int) uint16 {
	var id [4]uint16 // bustype, vendor, product, version
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlEVIOCGID, uintptr(unsafe.Pointer(&id))); e != 0 {
		return 0
	}
	return id[1]
}

// padState turns a pad's events into console keys: buttons by position,
// d-pad hat, left stick as d-pad (past half deflection), analog triggers.
type padState struct {
	keys       map[uint16]uint16
	ranges     map[uint16][2]int32
	dir        map[uint16]key // stick axis -> direction held
	hats       map[uint16]key
	trig       map[uint16]bool
	triggerKey map[uint16]key
}

func newPadState(fd int) *padState {
	p := &padState{keys: padKeys, ranges: map[uint16][2]int32{}, dir: map[uint16]key{}, hats: map[uint16]key{},
		trig: map[uint16]bool{}, triggerKey: map[uint16]key{absBrake: kL2, absGas: kR2}}
	if padVendor(fd) == vendorMicrosoft {
		p.keys = xboxPadKeys
	}
	for _, ax := range []uint16{absX, absY, absBrake, absGas} {
		lo, hi := axisRange(fd, uintptr(ax))
		p.ranges[ax] = [2]int32{lo, hi}
	}
	return p
}

func (p *padState) translate(e rawEvent) []keyEvent {
	switch {
	case e.typ == 1:
		if con, ok := p.keys[e.code]; ok {
			if ev, ok := translateKey(con, e.value); ok {
				return []keyEvent{ev}
			}
		}
	case e.typ == 3 && (e.code == 16 || e.code == 17):
		var evs []keyEvent
		p.hats[e.code], evs = hatEvents(e.code, e.value, p.hats[e.code])
		return evs
	case e.typ == 3 && (e.code == absX || e.code == absY):
		r := p.ranges[e.code]
		mid, half := (r[0]+r[1])/2, (r[1]-r[0])/4
		var next key
		switch {
		case e.value < mid-half && e.code == absX:
			next = kLeft
		case e.value > mid+half && e.code == absX:
			next = kRight
		case e.value < mid-half:
			next = kUp
		case e.value > mid+half:
			next = kDown
		}
		prev := p.dir[e.code]
		if next == prev {
			return nil
		}
		p.dir[e.code] = next
		var evs []keyEvent
		if prev != "" {
			evs = append(evs, keyEvent{prev, false})
		}
		if next != "" {
			evs = append(evs, keyEvent{next, true})
		}
		return evs
	case e.typ == 3 && (e.code == absBrake || e.code == absGas):
		r := p.ranges[e.code]
		on := e.value > r[0]+(r[1]-r[0])/2
		if on != p.trig[e.code] {
			p.trig[e.code] = on
			return []keyEvent{{p.triggerKey[e.code], on}}
		}
	}
	return nil
}

// listenInput feeds ch with key events from the console and every pad,
// including pads connected later (checked every 2 s).
func listenInput(ch chan<- keyEvent) error {
	var mu sync.Mutex
	open := map[string]bool{}
	send := func(evs []keyEvent) {
		for _, ev := range evs {
			select {
			case ch <- ev:
			default:
			}
		}
	}
	start := func(d device) bool {
		fd, err := syscall.Open(d.path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return false
		}
		mu.Lock()
		open[d.path] = true
		mu.Unlock()
		go func() {
			defer func() {
				syscall.Close(fd)
				mu.Lock()
				delete(open, d.path) // gone (pad off): may come back
				mu.Unlock()
			}()
			if d.pad {
				p := newPadState(fd)
				readEvents(fd, func(e rawEvent) { send(p.translate(e)) })
				return
			}
			hats := map[uint16]key{}
			readEvents(fd, func(e rawEvent) {
				switch {
				case e.typ == 1:
					if ev, ok := translateKey(e.code, e.value); ok {
						send([]keyEvent{ev})
					}
				case e.typ == 3 && (e.code == 16 || e.code == 17):
					var evs []keyEvent
					hats[e.code], evs = hatEvents(e.code, e.value, hats[e.code])
					send(evs)
				}
			})
		}()
		return true
	}
	opened := 0
	for _, d := range listDevices() {
		if start(d) {
			opened++
		}
	}
	if opened == 0 {
		return errors.New("no input device could be opened")
	}
	go func() {
		for range time.Tick(2 * time.Second) {
			for _, d := range listDevices() {
				mu.Lock()
				known := open[d.path]
				mu.Unlock()
				if !known && d.pad {
					start(d)
				}
			}
		}
	}()
	return nil
}
