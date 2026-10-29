package main

// HID side: the report descriptor, the input reports and the SDP record that
// bluetoothd publishes for us (ProfileManager1.RegisterProfile).
//
// The pad presents itself as an Xbox One S controller over Bluetooth classic
// (045E:02FD, firmware 4.8, Device ID set by launch.sh's main.conf), with that
// controller's own report descriptor (Linux-mode dump from xpadneo,
// docs/descriptors/xb1s_linux.md). macOS / iOS (GameController), Android,
// Windows and SDL (ES-DE, Steam, emulators) all know it as a game pad, while
// a generic HID pad is only a joystick without mapping there (checked on
// macOS: SDL saw no game pad at all).
// Faces are by position: the SP's bottom button (B) is the Xbox A.

import (
	"encoding/hex"
	"fmt"
	"strings"
)

const hidUUID = "00001124-0000-1000-8000-00805f9b34fb"

// xboxDescriptor: report 1 = X Y Z Rz (16 bits, 0-65534), brake (LT) and
// accelerator (RT) (10 bits), hat (1-8, 0 = none), 15 buttons, AC Back
// (View); report 2 = AC Home (Xbox button); 3 = rumble (output, ignored);
// 4 = battery.
const xboxDescriptor = "05010905a10185010901a10009300931150027ffff0000950275108102c00901a10009320935150027ffff0000950275108102c0050209c5150026ff039501750a810215002500750695018103050209c4150026ff039501750a81021500250075069501810305010939150125083500463b016614007504950181427504950115002500350045006500810305091901290f150025017501950f810215002500750195018103050c0a24021500250195017501810215002500750795018103050c09018502a101050c0a23021500250195017501810215002500750795018103c0050f09218503a102099715002501750495019102150025007504950191030970150025647508950491020950660110550e150026ff0075089501910209a7150026ff0075089501910265005500097c150026ff00750895019102c0050609208504150026ff00750895018102c0"

var reportDescriptor, _ = hex.DecodeString(xboxDescriptor)

// HID button numbers (1-based).
const (
	hidSouth  = 1
	hidEast   = 2
	hidWest   = 4
	hidNorth  = 5
	hidL1     = 7
	hidR1     = 8
	hidL2     = 9
	hidR2     = 10
	hidSelect = 11
	hidStart  = 12
	hidHome   = 13
)

// padState is what the host sees.
type padState struct {
	buttons               uint16 // bit n-1 = HID button n
	up, down, left, right bool
	// stick: the d-pad drives the left stick instead of the hat, for games
	// that only move on the stick (OpenGOAL's Jak and Daxter).
	stick bool
}

func (s *padState) set(button int, on bool) {
	if on {
		s.buttons |= 1 << (button - 1)
	} else {
		s.buttons &^= 1 << (button - 1)
	}
}

// hat: 1 = up, clockwise to 8 = up-left, 0 = released (Xbox convention).
func (s padState) hat() byte {
	up, down, left, right := s.up && !s.down, s.down && !s.up, s.left && !s.right, s.right && !s.left
	switch {
	case up && right:
		return 2
	case down && right:
		return 4
	case down && left:
		return 6
	case up && left:
		return 8
	case up:
		return 1
	case right:
		return 3
	case down:
		return 5
	case left:
		return 7
	}
	return 0
}

func (s padState) on(b int) bool { return s.buttons&(1<<(b-1)) != 0 }

// report is the interrupt-channel frame of report 1: HIDP DATA|Input
// header, report ID, then 16 bytes. Sticks stay centred (the SP has none);
// L2 / R2 pull the analog triggers fully; SELECT is View (AC Back); the
// button bitmap carries A B X Y LB RB Menu (12) and Xbox (13).
func (s padState) report() []byte {
	r := make([]byte, 18)
	r[0], r[1] = 0xA1, 0x01
	for i := 0; i < 4; i++ {
		r[2+2*i], r[3+2*i] = 0xFF, 0x7F // 32767 = centre
	}
	axis := func(at int, neg, pos bool) {
		switch {
		case neg && !pos:
			r[at], r[at+1] = 0, 0 // 0 = left / up
		case pos && !neg:
			r[at], r[at+1] = 0xFE, 0xFF // 65534 = right / down
		}
	}
	if s.stick {
		axis(2, s.left, s.right)
		axis(4, s.up, s.down)
	}
	trigger := func(at int, on bool) {
		if on {
			r[at], r[at+1] = 0xFF, 0x03 // 1023
		}
	}
	trigger(10, s.on(hidL2))
	trigger(12, s.on(hidR2))
	if !s.stick {
		r[14] = s.hat()
	}
	bits := s.buttons &^ (1<<(hidL2-1) | 1<<(hidR2-1) | 1<<(hidSelect-1))
	r[15], r[16] = byte(bits), byte(bits>>8)&0x7F
	if s.on(hidSelect) {
		r[17] = 0x01
	}
	return r
}

// homeReport is report 2 (AC Home), sent with the Xbox button bit.
func (s padState) homeReport() []byte {
	r := []byte{0xA1, 0x02, 0}
	if s.on(hidHome) {
		r[2] = 0x01
	}
	return r
}

// HIDP transaction types (high nibble of the first byte).
const (
	hidpHandshake   = 0x0
	hidpControl     = 0x1
	hidpGetReport   = 0x4
	hidpSetReport   = 0x5
	hidpGetProtocol = 0x6
	hidpSetProtocol = 0x7
	hidpData        = 0xA

	hsSuccessful    = 0x0
	hsInvalidReport = 0x2
	hsUnsupported   = 0x3

	ctrlVirtualCableUnplug = 0x5
)

// controlReply answers one control-channel message; unplug is true when the
// host drops the pairing (virtual cable unplug).
func controlReply(msg []byte, s padState, protocol *byte) (reply []byte, unplug bool) {
	if len(msg) == 0 {
		return nil, false
	}
	typ, param := msg[0]>>4, msg[0]&0x0f
	switch typ {
	case hidpControl:
		return nil, param == ctrlVirtualCableUnplug
	case hidpGetReport:
		if param&0x03 != 1 || len(msg) < 2 { // input reports, by ID
			return []byte{hidpHandshake<<4 | hsInvalidReport}, false
		}
		var r []byte
		switch msg[1] {
		case 0x01:
			r = s.report()
		case 0x02:
			r = s.homeReport()
		case 0x04:
			r = []byte{0xA1, 0x04, 0xFF} // battery: full
		default:
			return []byte{hidpHandshake<<4 | hsInvalidReport}, false
		}
		r[0] = hidpData<<4 | 0x01
		return r, false
	case hidpGetProtocol:
		return []byte{hidpData<<4 | 0x00, *protocol}, false
	case hidpSetProtocol:
		*protocol = param & 0x01
		return []byte{hidpHandshake<<4 | hsSuccessful}, false
	case hidpSetReport:
		return []byte{hidpHandshake<<4 | hsSuccessful}, false // rumble / LEDs: none
	case hidpHandshake, hidpData:
		return nil, false
	}
	return []byte{hidpHandshake<<4 | hsUnsupported}, false
}

// sdpRecord is the HID service record (BlueZ XML form).
func sdpRecord(name string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace
	attr := func(id int, inner string) string {
		return fmt.Sprintf("<attribute id=\"0x%04x\">%s</attribute>\n", id, inner)
	}
	u8 := func(v int) string { return fmt.Sprintf("<uint8 value=\"0x%02x\"/>", v) }
	u16 := func(v int) string { return fmt.Sprintf("<uint16 value=\"0x%04x\"/>", v) }
	uuid := func(v int) string { return fmt.Sprintf("<uuid value=\"0x%04x\"/>", v) }
	boolean := func(v bool) string { return fmt.Sprintf("<boolean value=\"%t\"/>", v) }
	text := func(s string) string { return "<text value=\"" + esc(s) + "\"/>" }
	seq := func(items ...string) string { return "<sequence>" + strings.Join(items, "") + "</sequence>" }
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\" ?>\n<record>\n")
	b.WriteString(attr(0x0001, seq(uuid(0x1124))))                                               // ServiceClassIDList: HID
	b.WriteString(attr(0x0004, seq(seq(uuid(0x0100), u16(psmHIDControl)), seq(uuid(0x0011)))))   // L2CAP 17, HIDP
	b.WriteString(attr(0x0005, seq(uuid(0x1002))))                                               // BrowseGroupList: public
	b.WriteString(attr(0x0006, seq(u16(0x656e), u16(0x006a), u16(0x0100))))                      // LanguageBase: en, UTF-8
	b.WriteString(attr(0x0009, seq(seq(uuid(0x1124), u16(0x0101)))))                             // Profile: HID 1.1
	b.WriteString(attr(0x000d, seq(seq(seq(uuid(0x0100), u16(psmHIDIntr)), seq(uuid(0x0011)))))) // L2CAP 19
	b.WriteString(attr(0x0100, text(name)))
	b.WriteString(attr(0x0101, text("Game pad")))
	b.WriteString(attr(0x0102, text("NextUI")))
	b.WriteString(attr(0x0201, u16(0x0111)))   // HIDParserVersion 1.11
	b.WriteString(attr(0x0202, u8(0x08)))      // HIDDeviceSubclass: gamepad
	b.WriteString(attr(0x0203, u8(0x00)))      // HIDCountryCode
	b.WriteString(attr(0x0204, boolean(true))) // HIDVirtualCable
	b.WriteString(attr(0x0205, boolean(true))) // HIDReconnectInitiate
	b.WriteString(attr(0x0206, seq(seq(u8(0x22), "<text encoding=\"hex\" value=\""+hex.EncodeToString(reportDescriptor)+"\"/>"))))
	b.WriteString(attr(0x0207, seq(seq(u16(0x0409), u16(0x0100))))) // HIDLANGIDBaseList: en-US
	b.WriteString(attr(0x0209, boolean(true)))                      // HIDBatteryPower
	b.WriteString(attr(0x020a, boolean(true)))                      // HIDRemoteWake
	b.WriteString(attr(0x020b, u16(0x0100)))                        // HIDProfileVersion
	b.WriteString(attr(0x020c, u16(0x0c80)))                        // HIDSupervisionTimeout (2 s)
	b.WriteString(attr(0x020d, boolean(true)))                      // HIDNormallyConnectable
	b.WriteString(attr(0x020e, boolean(false)))                     // HIDBootDevice
	b.WriteString(attr(0x020f, u16(0x0640)))                        // HIDSSRHostMaxLatency
	b.WriteString(attr(0x0210, u16(0x0320)))                        // HIDSSRHostMinTimeout
	b.WriteString("</record>\n")
	return b.String()
}
