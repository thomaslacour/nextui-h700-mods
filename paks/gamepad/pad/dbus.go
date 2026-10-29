package main

// Minimal D-Bus client (system bus, little-endian, no fd passing): enough to
// call bluetoothd (RegisterProfile, RegisterAgent, Properties) and to serve
// the Agent1 / Profile1 objects it calls back. Stdlib only, like the rest of
// the project.

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

type objectPath string
type signature string

// variant is a D-Bus "v": its own signature plus the value.
type variant struct {
	sig string
	val any
}

const (
	msgCall   = 1
	msgReturn = 2
	msgError  = 3
	msgSignal = 4

	flagNoReply = 1
)

type message struct {
	typ, flags  byte
	serial      uint32
	replySerial uint32
	path        objectPath
	iface       string
	member      string
	errName     string
	dest        string
	sender      string
	sig         string
	body        []any
}

// ---- marshalling ----

type encoder struct{ b []byte }

func (e *encoder) align(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}

func (e *encoder) u32(v uint32) {
	e.align(4)
	e.b = binary.LittleEndian.AppendUint32(e.b, v)
}

// nextType splits the first complete type off sig.
func nextType(sig string) (string, string, error) {
	if sig == "" {
		return "", "", errors.New("empty signature")
	}
	switch sig[0] {
	case 'a':
		t, rest, err := nextType(sig[1:])
		return "a" + t, rest, err
	case '(', '{':
		closer := map[byte]byte{'(': ')', '{': '}'}[sig[0]]
		depth := 0
		for i := 0; i < len(sig); i++ {
			switch sig[i] {
			case '(', '{':
				depth++
			case ')', '}':
				depth--
				if depth == 0 {
					if sig[i] != closer {
						return "", "", errors.New("bad signature " + sig)
					}
					return sig[:i+1], sig[i+1:], nil
				}
			}
		}
		return "", "", errors.New("unbalanced signature " + sig)
	}
	return sig[:1], sig[1:], nil
}

func splitTypes(sig string) ([]string, error) {
	var out []string
	for sig != "" {
		t, rest, err := nextType(sig)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		sig = rest
	}
	return out, nil
}

func alignOf(t string) int {
	switch t[0] {
	case 'y', 'g', 'v':
		return 1
	case 'n', 'q':
		return 2
	case 'x', 't', 'd', '(', '{':
		return 8
	}
	return 4
}

func (e *encoder) encode(t string, v any) error {
	bad := func() error { return fmt.Errorf("dbus: cannot encode %T as %s", v, t) }
	switch t[0] {
	case 'y':
		x, ok := v.(byte)
		if !ok {
			return bad()
		}
		e.b = append(e.b, x)
	case 'b':
		x, ok := v.(bool)
		if !ok {
			return bad()
		}
		n := uint32(0)
		if x {
			n = 1
		}
		e.u32(n)
	case 'n', 'q':
		var n uint16
		switch x := v.(type) {
		case int16:
			n = uint16(x)
		case uint16:
			n = x
		default:
			return bad()
		}
		e.align(2)
		e.b = binary.LittleEndian.AppendUint16(e.b, n)
	case 'i', 'u':
		var n uint32
		switch x := v.(type) {
		case int32:
			n = uint32(x)
		case uint32:
			n = x
		default:
			return bad()
		}
		e.u32(n)
	case 'x', 't', 'd':
		var n uint64
		switch x := v.(type) {
		case int64:
			n = uint64(x)
		case uint64:
			n = x
		case float64:
			n = math.Float64bits(x)
		default:
			return bad()
		}
		e.align(8)
		e.b = binary.LittleEndian.AppendUint64(e.b, n)
	case 's', 'o':
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case objectPath:
			s = string(x)
		default:
			return bad()
		}
		e.u32(uint32(len(s)))
		e.b = append(append(e.b, s...), 0)
	case 'g':
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case signature:
			s = string(x)
		default:
			return bad()
		}
		e.b = append(append(append(e.b, byte(len(s))), s...), 0)
	case 'v':
		x, ok := v.(variant)
		if !ok {
			return bad()
		}
		if err := e.encode("g", x.sig); err != nil {
			return err
		}
		return e.encode(x.sig, x.val)
	case 'a':
		elem := t[1:]
		e.u32(0)
		lenAt := len(e.b) - 4
		e.align(alignOf(elem))
		start := len(e.b)
		var err error
		switch x := v.(type) {
		case []byte:
			if elem != "y" {
				return bad()
			}
			e.b = append(e.b, x...)
		case []string:
			for _, s := range x {
				if err = e.encode(elem, s); err != nil {
					return err
				}
			}
		case []any:
			for _, it := range x {
				if err = e.encode(elem, it); err != nil {
					return err
				}
			}
		case map[string]variant:
			if elem != "{sv}" {
				return bad()
			}
			for _, k := range sortedKeys(x) {
				if err = e.encode(elem, []any{k, x[k]}); err != nil {
					return err
				}
			}
		default:
			return bad()
		}
		binary.LittleEndian.PutUint32(e.b[lenAt:], uint32(len(e.b)-start))
	case '(', '{':
		fields, ok := v.([]any)
		inner := t[1 : len(t)-1]
		types, err := splitTypes(inner)
		if !ok || err != nil || len(types) != len(fields) {
			return bad()
		}
		e.align(8)
		for i, ft := range types {
			if err := e.encode(ft, fields[i]); err != nil {
				return err
			}
		}
	default:
		return bad()
	}
	return nil
}

func sortedKeys(m map[string]variant) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ { // insertion sort: tiny maps, stable output
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

type decoder struct {
	b   []byte
	pos int
}

var errShort = errors.New("dbus: short message")

func (d *decoder) align(n int) error {
	for d.pos%n != 0 {
		d.pos++
	}
	if d.pos > len(d.b) {
		return errShort
	}
	return nil
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || d.pos+n > len(d.b) {
		return nil, errShort
	}
	out := d.b[d.pos : d.pos+n]
	d.pos += n
	return out, nil
}

func (d *decoder) u32() (uint32, error) {
	if err := d.align(4); err != nil {
		return 0, err
	}
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// decode returns: y byte, b bool, n int16, q uint16, i int32, u uint32,
// x int64, t uint64, d float64, s/g string, o objectPath, v variant,
// ay []byte, a{sv} map[string]variant, other a{..} map[any]any, other arrays
// []any, structs []any.
func (d *decoder) decode(t string) (any, error) {
	switch t[0] {
	case 'y':
		b, err := d.take(1)
		if err != nil {
			return nil, err
		}
		return b[0], nil
	case 'b':
		n, err := d.u32()
		return n != 0, err
	case 'n', 'q':
		if err := d.align(2); err != nil {
			return nil, err
		}
		b, err := d.take(2)
		if err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint16(b)
		if t[0] == 'n' {
			return int16(n), nil
		}
		return n, nil
	case 'i':
		n, err := d.u32()
		return int32(n), err
	case 'u':
		return d.u32()
	case 'x', 't', 'd':
		if err := d.align(8); err != nil {
			return nil, err
		}
		b, err := d.take(8)
		if err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint64(b)
		switch t[0] {
		case 'x':
			return int64(n), nil
		case 'd':
			return math.Float64frombits(n), nil
		}
		return n, nil
	case 's', 'o':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		b, err := d.take(int(n) + 1)
		if err != nil {
			return nil, err
		}
		if t[0] == 'o' {
			return objectPath(b[:n]), nil
		}
		return string(b[:n]), nil
	case 'g':
		l, err := d.take(1)
		if err != nil {
			return nil, err
		}
		b, err := d.take(int(l[0]) + 1)
		if err != nil {
			return nil, err
		}
		return string(b[:l[0]]), nil
	case 'v':
		s, err := d.decode("g")
		if err != nil {
			return nil, err
		}
		sig := s.(string)
		if _, rest, err := nextType(sig); err != nil || rest != "" {
			return nil, fmt.Errorf("dbus: bad variant signature %q", sig)
		}
		v, err := d.decode(sig)
		return variant{sig, v}, err
	case 'a':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		elem := t[1:]
		if err := d.align(alignOf(elem)); err != nil {
			return nil, err
		}
		end := d.pos + int(n)
		if end > len(d.b) {
			return nil, errShort
		}
		if elem == "y" {
			b, _ := d.take(int(n))
			return append([]byte(nil), b...), nil
		}
		var list []any
		for d.pos < end {
			v, err := d.decode(elem)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		if elem[0] != '{' {
			return list, nil
		}
		if elem == "{sv}" {
			m := map[string]variant{}
			for _, kv := range list {
				p := kv.([]any)
				m[p[0].(string)], _ = p[1].(variant)
			}
			return m, nil
		}
		m := map[any]any{}
		for _, kv := range list {
			p := kv.([]any)
			m[p[0]] = p[1]
		}
		return m, nil
	case '(', '{':
		if err := d.align(8); err != nil {
			return nil, err
		}
		types, err := splitTypes(t[1 : len(t)-1])
		if err != nil {
			return nil, err
		}
		var out []any
		for _, ft := range types {
			v, err := d.decode(ft)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("dbus: unsupported type %q", t)
}

// marshal builds the wire form of m (serial already set).
func (m *message) marshal() ([]byte, error) {
	var body encoder
	if m.sig != "" {
		types, err := splitTypes(m.sig)
		if err != nil {
			return nil, err
		}
		if len(types) != len(m.body) {
			return nil, fmt.Errorf("dbus: %d values for signature %q", len(m.body), m.sig)
		}
		for i, t := range types {
			if err := body.encode(t, m.body[i]); err != nil {
				return nil, err
			}
		}
	}
	var fields []any
	add := func(code byte, sig string, v any) { fields = append(fields, []any{code, variant{sig, v}}) }
	if m.path != "" {
		add(1, "o", m.path)
	}
	if m.iface != "" {
		add(2, "s", m.iface)
	}
	if m.member != "" {
		add(3, "s", m.member)
	}
	if m.errName != "" {
		add(4, "s", m.errName)
	}
	if m.replySerial != 0 {
		add(5, "u", m.replySerial)
	}
	if m.dest != "" {
		add(6, "s", m.dest)
	}
	if m.sig != "" {
		add(8, "g", m.sig)
	}
	h := encoder{b: []byte{'l', m.typ, m.flags, 1}}
	h.u32(uint32(len(body.b)))
	h.u32(m.serial)
	if err := h.encode("a(yv)", fields); err != nil {
		return nil, err
	}
	h.align(8)
	return append(h.b, body.b...), nil
}

// readMessage reads one message from r.
func readMessage(r io.Reader) (*message, error) {
	var fixed [16]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return nil, err
	}
	if fixed[0] != 'l' {
		return nil, errors.New("dbus: big-endian messages are not supported")
	}
	bodyLen := binary.LittleEndian.Uint32(fixed[4:])
	fieldsLen := binary.LittleEndian.Uint32(fixed[12:])
	if bodyLen > 1<<24 || fieldsLen > 1<<20 {
		return nil, errors.New("dbus: message too large")
	}
	hdrLen := 16 + int(fieldsLen)
	pad := (8 - hdrLen%8) % 8
	buf := make([]byte, hdrLen+pad+int(bodyLen))
	copy(buf, fixed[:])
	if _, err := io.ReadFull(r, buf[16:]); err != nil {
		return nil, err
	}
	m := &message{typ: fixed[1], flags: fixed[2], serial: binary.LittleEndian.Uint32(fixed[8:])}
	d := &decoder{b: buf[:hdrLen], pos: 12}
	fv, err := d.decode("a(yv)")
	if err != nil {
		return nil, err
	}
	for _, f := range fv.([]any) {
		p := f.([]any)
		v := p[1].(variant).val
		switch p[0].(byte) {
		case 1:
			m.path, _ = v.(objectPath)
		case 2:
			m.iface, _ = v.(string)
		case 3:
			m.member, _ = v.(string)
		case 4:
			m.errName, _ = v.(string)
		case 5:
			m.replySerial, _ = v.(uint32)
		case 6:
			m.dest, _ = v.(string)
		case 7:
			m.sender, _ = v.(string)
		case 8:
			m.sig, _ = v.(string)
		}
	}
	if m.sig != "" {
		types, err := splitTypes(m.sig)
		if err != nil {
			return nil, err
		}
		bd := &decoder{b: buf[hdrLen+pad:]}
		for _, t := range types {
			v, err := bd.decode(t)
			if err != nil {
				return nil, err
			}
			m.body = append(m.body, v)
		}
	}
	return m, nil
}

// ---- connection ----

type dbusError struct{ name, text string }

func (e *dbusError) Error() string { return e.name + ": " + e.text }

// handler serves a method call on one of our objects: it returns the reply
// signature and values, or a D-Bus error.
type handler func(m *message) (string, []any, *dbusError)

type conn struct {
	c       net.Conn
	r       *bufio.Reader
	wmu     sync.Mutex
	mu      sync.Mutex
	serial  uint32
	pending map[uint32]chan *message
	objects map[objectPath]handler
	signals func(*message)
	closed  chan struct{}
}

func systemBusPath() string {
	if a := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS"); strings.HasPrefix(a, "unix:path=") {
		p := strings.TrimPrefix(a, "unix:path=")
		if i := strings.IndexByte(p, ','); i >= 0 {
			p = p[:i]
		}
		return p
	}
	for _, p := range []string{"/run/dbus/system_bus_socket", "/var/run/dbus/system_bus_socket"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/run/dbus/system_bus_socket"
}

func dialSystemBus() (*conn, error) {
	c, err := net.Dial("unix", systemBusPath())
	if err != nil {
		return nil, err
	}
	b := &conn{c: c, r: bufio.NewReader(c), pending: map[uint32]chan *message{},
		objects: map[objectPath]handler{}, closed: make(chan struct{})}
	uid := hex.EncodeToString([]byte(strconv.Itoa(os.Getuid())))
	if _, err := c.Write([]byte("\x00AUTH EXTERNAL " + uid + "\r\n")); err != nil {
		c.Close()
		return nil, err
	}
	line, err := b.r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "OK ") {
		c.Close()
		return nil, fmt.Errorf("dbus auth: %q %v", strings.TrimSpace(line), err)
	}
	if _, err := c.Write([]byte("BEGIN\r\n")); err != nil {
		c.Close()
		return nil, err
	}
	go b.loop()
	if _, err := b.call("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Hello", ""); err != nil {
		c.Close()
		return nil, fmt.Errorf("dbus hello: %w", err)
	}
	return b, nil
}

func (b *conn) close() { b.c.Close() }

func (b *conn) send(m *message) error {
	b.mu.Lock()
	b.serial++
	m.serial = b.serial
	b.mu.Unlock()
	data, err := m.marshal()
	if err != nil {
		return err
	}
	b.wmu.Lock()
	defer b.wmu.Unlock()
	_, err = b.c.Write(data)
	return err
}

func (b *conn) loop() {
	defer close(b.closed)
	for {
		m, err := readMessage(b.r)
		if err != nil {
			b.mu.Lock()
			for s, ch := range b.pending {
				close(ch)
				delete(b.pending, s)
			}
			b.mu.Unlock()
			return
		}
		switch m.typ {
		case msgReturn, msgError:
			b.mu.Lock()
			ch := b.pending[m.replySerial]
			delete(b.pending, m.replySerial)
			b.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case msgCall:
			go b.serve(m)
		case msgSignal:
			if b.signals != nil {
				b.signals(m)
			}
		}
	}
}

func (b *conn) serve(m *message) {
	h := b.objects[m.path]
	var sig string
	var body []any
	var derr *dbusError
	switch {
	case m.iface == "org.freedesktop.DBus.Peer" && m.member == "Ping":
	case h == nil:
		derr = &dbusError{"org.freedesktop.DBus.Error.UnknownObject", "no object " + string(m.path)}
	default:
		sig, body, derr = h(m)
	}
	if m.flags&flagNoReply != 0 {
		return
	}
	r := &message{typ: msgReturn, replySerial: m.serial, dest: m.sender, sig: sig, body: body}
	if derr != nil {
		r = &message{typ: msgError, replySerial: m.serial, dest: m.sender, errName: derr.name, sig: "s", body: []any{derr.text}}
	}
	b.send(r)
}

// call sends a method call and waits for its reply body.
func (b *conn) call(dest string, path objectPath, iface, member, sig string, args ...any) ([]any, error) {
	ch := make(chan *message, 1)
	m := &message{typ: msgCall, dest: dest, path: path, iface: iface, member: member, sig: sig, body: args}
	b.mu.Lock()
	b.serial++
	m.serial = b.serial
	b.pending[m.serial] = ch
	b.mu.Unlock()
	data, err := m.marshal()
	if err == nil {
		b.wmu.Lock()
		_, err = b.c.Write(data)
		b.wmu.Unlock()
	}
	if err != nil {
		b.mu.Lock()
		delete(b.pending, m.serial)
		b.mu.Unlock()
		return nil, err
	}
	r, ok := <-ch
	if !ok {
		return nil, errors.New("dbus: connection closed")
	}
	if r.typ == msgError {
		text := ""
		if len(r.body) > 0 {
			text, _ = r.body[0].(string)
		}
		return nil, &dbusError{r.errName, text}
	}
	return r.body, nil
}

// getProp / setProp: org.freedesktop.DBus.Properties on bluetoothd objects.
func (b *conn) getProp(path objectPath, iface, name string) (any, error) {
	out, err := b.call("org.bluez", path, "org.freedesktop.DBus.Properties", "Get", "ss", iface, name)
	if err != nil {
		return nil, err
	}
	if len(out) != 1 {
		return nil, errors.New("dbus: bad Get reply")
	}
	v, _ := out[0].(variant)
	return v.val, nil
}

func (b *conn) setProp(path objectPath, iface, name string, v variant) error {
	_, err := b.call("org.bluez", path, "org.freedesktop.DBus.Properties", "Set", "ssv", iface, name, v)
	return err
}

// addMatch subscribes to signals (for device connect/disconnect names).
func (b *conn) addMatch(rule string) error {
	_, err := b.call("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "AddMatch", "s", rule)
	return err
}
