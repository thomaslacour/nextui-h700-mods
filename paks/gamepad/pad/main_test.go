package main

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	m := &message{typ: msgCall, serial: 7, path: "/org/bluez", iface: "org.bluez.ProfileManager1",
		member: "RegisterProfile", dest: "org.bluez", sig: "osa{sv}",
		body: []any{objectPath("/p"), hidUUID, map[string]variant{
			"Role": {"s", "server"}, "RequireAuthentication": {"b", true}, "Version": {"q", uint16(0x0101)},
			"Bytes": {"ay", []byte{1, 2, 3}}, "Nested": {"a{sv}", map[string]variant{"x": {"u", uint32(5)}}},
		}}}
	data, err := m.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%8 != 0 && len(m.body) == 0 {
		t.Fatal("header not padded")
	}
	got, err := readMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got.serial != 7 || got.path != m.path || got.member != m.member || got.dest != m.dest || got.sig != m.sig {
		t.Fatalf("header mismatch: %+v", got)
	}
	if !reflect.DeepEqual(got.body, m.body) {
		t.Fatalf("body mismatch:\n got %#v\nwant %#v", got.body, m.body)
	}
}

func TestDecodeManagedObjects(t *testing.T) {
	// a{oa{sa{sv}}}, as GetManagedObjects returns it.
	var e encoder
	dev := map[string]variant{"Address": {"s", "A4:CF:99:12:34:56"}, "Paired": {"b", true}, "Alias": {"s", "Mac"}}
	inner := []any{[]any{"org.bluez.Device1", dev}}
	if err := e.encode("a{oa{sa{sv}}}", []any{[]any{objectPath("/org/bluez/hci0/dev_A4"), inner}}); err != nil {
		t.Fatal(err)
	}
	v, err := (&decoder{b: e.b}).decode("a{oa{sa{sv}}}")
	if err != nil {
		t.Fatal(err)
	}
	objs := v.(map[any]any)
	ifaces := objs[objectPath("/org/bluez/hci0/dev_A4")].(map[any]any)
	props := ifaces["org.bluez.Device1"].(map[string]variant)
	if props["Alias"].val != "Mac" || props["Paired"].val != true {
		t.Fatalf("props %#v", props)
	}
}

// fakeBus is a minimal dbus-daemon: EXTERNAL auth, Hello, and one method
// that calls the client back (like bluetoothd calling the agent).
func fakeBus(t *testing.T, l net.Listener) {
	c, err := l.Accept()
	if err != nil {
		return
	}
	defer c.Close()
	r := bufio.NewReader(c)
	line, _ := r.ReadString('\n')
	if !strings.HasPrefix(line, "\x00AUTH EXTERNAL ") {
		t.Errorf("auth line %q", line)
		return
	}
	c.Write([]byte("OK 1234\r\n"))
	if line, _ = r.ReadString('\n'); line != "BEGIN\r\n" {
		t.Errorf("begin %q", line)
		return
	}
	serial := uint32(100)
	write := func(m *message) {
		serial++
		m.serial = serial
		b, _ := m.marshal()
		c.Write(b)
	}
	for {
		m, err := readMessage(r)
		if err != nil {
			return
		}
		switch {
		case m.member == "Hello":
			write(&message{typ: msgReturn, replySerial: m.serial, sig: "s", body: []any{":1.5"}})
		case m.member == "Get":
			write(&message{typ: msgCall, path: agentPath, iface: "org.bluez.Agent1", member: "RequestPasskey",
				sig: "o", body: []any{objectPath("/org/bluez/hci0/dev_X")}})
			cb, err := readMessage(r)
			if err != nil || cb.typ != msgReturn || len(cb.body) != 1 || cb.body[0] != uint32(0) {
				t.Errorf("agent reply %+v %v", cb, err)
			}
			write(&message{typ: msgReturn, replySerial: m.serial, sig: "v", body: []any{variant{"s", "SP"}}})
		case m.member == "Set":
			write(&message{typ: msgError, replySerial: m.serial, errName: "org.bluez.Error.Failed", sig: "s", body: []any{"nope"}})
		}
	}
}

func TestConnCallsAndCallbacks(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "bus")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable:", err)
	}
	defer l.Close()
	go fakeBus(t, l)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+sock)
	z, err := newBluez(func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer z.bus.close()
	v, err := z.bus.getProp(adapterPath, ifAdapter, "Alias")
	if err != nil || v != "SP" {
		t.Fatalf("get: %v %v", v, err)
	}
	err = z.bus.setProp(adapterPath, ifAdapter, "Alias", variant{"s", "x"})
	if de, ok := err.(*dbusError); !ok || de.name != "org.bluez.Error.Failed" || de.text != "nope" {
		t.Fatalf("set error %v", err)
	}
}

func TestReportAndHat(t *testing.T) {
	var s padState
	idle := []byte{0xA1, 1, 0xFF, 0x7F, 0xFF, 0x7F, 0xFF, 0x7F, 0xFF, 0x7F, 0, 0, 0, 0, 0, 0, 0}
	if r := s.report(); !bytes.Equal(r, idle) {
		t.Fatalf("idle report % x", r)
	}
	if len(idle)-2 != 15 {
		t.Fatal("report 1 is 15 bytes")
	}
	s.apply(evKey{codeB, 1})      // south = Xbox A, bit 0
	s.apply(evKey{codeX, 1})      // north = Xbox Y, bit 3
	s.apply(evKey{codeStart, 1})  // Menu, bit 7
	s.apply(evKey{codeSelect, 1}) // View, bit 6
	s.apply(evKey{codeL2, 1})     // left trigger full
	s.apply(evKey{codeUp, 1})
	s.apply(evKey{codeRight, 1}) // up-right = 2
	r := s.report()
	if !bytes.Equal(r[10:], []byte{0xFF, 0x03, 0, 0, 2, 0xC9, 0}) {
		t.Fatalf("report % x", r)
	}
	s.apply(evKey{codeUp, 0})
	if s.hat() != 3 {
		t.Fatalf("hat %d", s.hat())
	}
	s.left = true // left + right cancel
	if s.hat() != 0 {
		t.Fatalf("opposite hat %d", s.hat())
	}
	if s.apply(evKey{116, 1}) {
		t.Fatal("power key must not be forwarded")
	}
	s.set(hidHome, true)
	if r := s.report(); r[16]&0xFC != 0 || !bytes.Equal(s.homeReport(), []byte{0xA1, 2, 1}) {
		t.Fatalf("home % x / % x", r, s.homeReport())
	}
}

func TestDescriptorIsXbox(t *testing.T) {
	if len(reportDescriptor) != 306 || reportDescriptor[len(reportDescriptor)-1] != 0xC0 {
		t.Fatalf("descriptor %d bytes", len(reportDescriptor))
	}
}

func TestWindowsReportMatchesFirefoxXboxOneSMapping(t *testing.T) {
	// Firefox's 045E:02E0 remapper uses contiguous HID buttons and desktop
	// axes Z/Rz for triggers. Check every physical key against that protocol,
	// independently of our internal hid* state-bit constants.
	for _, tc := range []struct {
		name string
		code uint16
		bit  uint
	}{
		{"B/south", codeB, 0}, {"A/east", codeA, 1},
		{"Y/west", codeY, 2}, {"X/north", codeX, 3},
		{"L1", codeL1, 4}, {"R1", codeR1, 5},
		{"Select", codeSelect, 6}, {"Start", codeStart, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var state padState
			state.apply(evKey{tc.code, 1})
			report := state.report()
			bits := uint16(report[15]) | uint16(report[16])<<8
			if bits != 1<<tc.bit || report[10] != 0 || report[12] != 0 {
				t.Fatalf("wrong Windows button: % x", report)
			}
			state.apply(evKey{tc.code, 0})
			if !bytes.Equal(state.report(), (padState{}).report()) {
				t.Fatal("button stuck after release")
			}
		})
	}
	for _, tc := range []struct {
		code uint16
		at   int
	}{{codeL2, 10}, {codeR2, 12}} {
		var state padState
		state.apply(evKey{tc.code, 1})
		r := state.report()
		if r[tc.at] != 255 || r[tc.at+1] != 3 || r[15] != 0 || r[16] != 0 {
			t.Fatalf("trigger must be a 10-bit axis, not a button: % x", r)
		}
	}
	// Real Windows descriptor: right stick Rx/Ry, triggers desktop Z/Rz,
	// ten contiguous buttons; no simulation-page brake/accelerator usages.
	for _, hex := range []string{"09330934", "05010932150026ff03", "05010935150026ff03", "05091901290a"} {
		if !strings.Contains(xboxDescriptor, hex) {
			t.Fatalf("missing Windows HID field %s", hex)
		}
	}
	if strings.Contains(xboxDescriptor, "050209c5") || strings.Contains(xboxDescriptor, "050209c4") {
		t.Fatal("Linux simulation triggers are not handled by Firefox's Xbox remapper")
	}
}

func TestHatEvents(t *testing.T) {
	hats := map[uint16]int32{}
	var got []evKey
	emit := func(k evKey) { got = append(got, k) }
	ev := func(typ, code uint16, value int32) []byte {
		b := make([]byte, 24)
		b[16], b[17] = byte(typ), byte(typ>>8)
		b[18], b[19] = byte(code), byte(code>>8)
		b[20], b[21], b[22], b[23] = byte(value), byte(value>>8), byte(value>>16), byte(value>>24)
		return b
	}
	var b []byte
	b = append(b, ev(evAbsType, absHat0Y, -1)...)
	b = append(b, ev(evAbsType, absHat0Y, 1)...)
	b = append(b, ev(evAbsType, absHat0Y, 0)...)
	b = append(b, ev(evKeyType, codeA, 1)...)
	b = append(b, ev(evKeyType, codeA, 2)...) // autorepeat dropped
	b = append(b, ev(evKeyType, 544, 1)...)   // BTN_DPAD_UP folds to up
	decodeEvents(b, hats, emit)
	want := []evKey{{codeUp, 1}, {codeUp, 0}, {codeDown, 1}, {codeDown, 0}, {codeA, 1}, {codeUp, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestKeyFilter(t *testing.T) {
	var f keyFilter
	if o := f.feed(evKey{codeMenu, 1}); o.pass || o.home {
		t.Fatal("menu press must wait")
	}
	if o := f.feed(evKey{codeMenu, 0}); !o.home {
		t.Fatal("menu tap = home")
	}
	f.feed(evKey{codeMenu, 1})
	if o := f.feed(evKey{codeStart, 1}); !o.toggleBT || o.quit || o.pass {
		t.Fatal("menu+start = bluetooth on/off, start not sent")
	}
	f.feed(evKey{codeStart, 0})
	f.feed(evKey{codeMenu, 0})
	f = keyFilter{}
	f.feed(evKey{codeMenu, 1})
	if o := f.feed(evKey{codeA, 1}); !o.pass {
		t.Fatal("menu+A passes A")
	}
	if o := f.feed(evKey{codeMenu, 0}); o.home {
		t.Fatal("menu used in a combo is not home")
	}
	if o := f.feed(evKey{codeStart, 1}); !o.pass || o.quit {
		t.Fatal("start alone passes")
	}
	// SELECT + START quits, as in Tools > Input, in either order; both
	// keys still reach the host.
	if o := f.feed(evKey{codeSelect, 1}); !o.quit || !o.pass {
		t.Fatal("start then select = quit")
	}
	f = keyFilter{}
	f.feed(evKey{codeSelect, 1})
	f.feed(evKey{codeSelect, 0})
	if o := f.feed(evKey{codeStart, 1}); o.quit {
		t.Fatal("select released: start alone")
	}
	f.feed(evKey{codeStart, 0})
	f.feed(evKey{codeSelect, 1})
	if o := f.feed(evKey{codeStart, 1}); !o.quit {
		t.Fatal("select then start = quit")
	}
}

func TestControlReply(t *testing.T) {
	var s padState
	s.set(hidEast, true)
	proto := byte(1)
	if r, _ := controlReply([]byte{0x71}, s, &proto); !bytes.Equal(r, []byte{0x00}) {
		t.Fatalf("set protocol % x", r)
	}
	if r, _ := controlReply([]byte{0x60}, s, &proto); !bytes.Equal(r, []byte{0xA0, 1}) {
		t.Fatalf("get protocol % x", r)
	}
	if r, _ := controlReply([]byte{0x41, 0x01}, s, &proto); len(r) != 17 || r[0] != 0xA1 || r[15] != 0x02 {
		t.Fatalf("get report % x", r)
	}
	if r, _ := controlReply([]byte{0x41, 0x04}, s, &proto); !bytes.Equal(r, []byte{0xA1, 4, 0xFF}) {
		t.Fatalf("battery % x", r)
	}
	if r, _ := controlReply([]byte{0x43, 0x01}, s, &proto); !bytes.Equal(r, []byte{0x02}) {
		t.Fatalf("feature report % x", r)
	}
	if _, unplug := controlReply([]byte{0x15}, s, &proto); !unplug {
		t.Fatal("virtual cable unplug")
	}
	if r, _ := controlReply([]byte{0x90}, s, &proto); !bytes.Equal(r, []byte{0x03}) {
		t.Fatalf("unknown % x", r)
	}
}

func TestSDPRecordIsXML(t *testing.T) {
	rec := sdpRecord(`Pad "<&>"`)
	// Balanced tags, every attribute value quoted, the name escaped.
	depth, n := 0, 0
	for _, tag := range regexp.MustCompile(`<[^>]*>`).FindAllString(rec, -1) {
		switch {
		case strings.HasPrefix(tag, "<?"), strings.HasSuffix(tag, "/>"):
		case strings.HasPrefix(tag, "</"):
			depth--
		default:
			depth++
			if strings.HasPrefix(tag, "<attribute") {
				n++
			}
		}
		if depth < 0 {
			t.Fatal("unbalanced", tag)
		}
	}
	if depth != 0 || n < 20 || !strings.Contains(rec, "05010905a101") || !strings.Contains(rec, "Pad &quot;&lt;&amp;&gt;&quot;") {
		t.Fatalf("depth %d, %d attributes\n%s", depth, n, rec)
	}
}

func TestAddr(t *testing.T) {
	a, err := parseAddr("A4:CF:99:12:34:56")
	if err != nil || a[0] != 0x56 || a.String() != "A4:CF:99:12:34:56" {
		t.Fatalf("%v %v", a, err)
	}
	if a.devicePath() != "/org/bluez/hci0/dev_A4_CF_99_12_34_56" {
		t.Fatal(a.devicePath())
	}
	sa := sockaddrL2(psmHIDIntr, a)
	if sa[0] != 31 || sa[2] != 0x13 || sa[4] != 0x56 {
		t.Fatalf("% x", sa)
	}
}

func TestHosts(t *testing.T) {
	t.Setenv("GAMEPAD_DIR", t.TempDir())
	a, _ := parseAddr("11:22:33:44:55:66")
	b, _ := parseAddr("AA:BB:CC:DD:EE:FF")
	rememberHost(a, "Mac")
	rememberHost(b, "Phone")
	rememberHost(a, "Mac")
	if got := loadHosts(); !reflect.DeepEqual(got, []bdaddr{a, b}) {
		t.Fatalf("%v", got)
	}
}

func TestPreview(t *testing.T) {
	dir := t.TempDir()
	if err := preview(dir); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"tester", "waiting", "connected", "error"} {
		if st, err := os.Stat(filepath.Join(dir, n+".png")); err != nil || st.Size() < 1000 {
			t.Fatalf("%s: %v", n, err)
		}
	}

}

func TestStickMode(t *testing.T) {
	s := padState{stick: true}
	s.apply(evKey{codeLeft, 1})
	s.apply(evKey{codeUp, 1})
	r := s.report()
	// X = 0 (left), Y = 0 (up), right stick centred, no hat.
	if !bytes.Equal(r[2:10], []byte{0, 0, 0, 0, 0xFF, 0x7F, 0xFF, 0x7F}) || r[14] != 0 {
		t.Fatalf("stick report % x", r)
	}
	s.apply(evKey{codeLeft, 0})
	s.apply(evKey{codeRight, 1})
	s.apply(evKey{codeUp, 0})
	s.apply(evKey{codeDown, 1})
	if r := s.report(); !bytes.Equal(r[2:6], []byte{0xFE, 0xFF, 0xFE, 0xFF}) {
		t.Fatalf("right/down % x", r)
	}
	s.stick = false
	if r := s.report(); !bytes.Equal(r[2:6], []byte{0xFF, 0x7F, 0xFF, 0x7F}) || r[14] != 4 {
		t.Fatalf("back to hat % x", r)
	}
}

func TestStickToggleAndSetting(t *testing.T) {
	var f keyFilter
	f.feed(evKey{codeMenu, 1})
	if o := f.feed(evKey{codeSelect, 1}); !o.toggleStick || o.pass {
		t.Fatal("menu+select toggles, select not sent")
	}
	if o := f.feed(evKey{codeMenu, 0}); o.home {
		t.Fatal("no home after a combo")
	}
	t.Setenv("GAMEPAD_DIR", t.TempDir())
	if loadStick() {
		t.Fatal("default is the d-pad")
	}
	saveStick(true)
	if !loadStick() {
		t.Fatal("stick saved")
	}
	saveStick(false)
	if loadStick() {
		t.Fatal("d-pad saved")
	}
}
