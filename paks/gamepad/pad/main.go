package main

// gamepad — the RG35XX SP as a Bluetooth classic HID game pad (Tools >
// Gamepad). It opens as a key tester with Bluetooth untouched (it replaces
// Tools > Input). MENU + START turns the pad on: launch.sh --bt-on restarts
// bluetoothd without its input plugin and with the gamepad device class,
// then this program:
//   - publishes the HID SDP record and a Just Works agent (D-Bus),
//   - listens on L2CAP PSM 17 (control) and 19 (interrupt),
//   - reconnects to the last host, or waits for a new one (discoverable),
//   - sends the console buttons as input reports.
// MENU + START turns the pad off again (launch.sh --bt-off), SELECT + START
// quits; MENU alone is the host's Home button.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const usage = `gamepad — RG35XX SP as a Bluetooth game pad
  gamepad                run (screen + Bluetooth)
  gamepad --check        probe: system bus, adapter, PSM 17/19 free?
  gamepad --sdp          print the SDP record
  gamepad --preview DIR  write PNG previews of the screen
`

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+format+"\n", args...)
}

func deviceName() string {
	if n := os.Getenv("GAMEPAD_NAME"); n != "" {
		return n
	}
	return "RG35XX SP Gamepad"
}

func dataDir() string {
	if d := os.Getenv("GAMEPAD_DIR"); d != "" {
		return d
	}
	return "/mnt/SDCARD/.userdata/h700/gamepad"
}

func main() {
	args := os.Args[1:]
	switch {
	case len(args) == 0:
		if err := run(); err != nil {
			logf("error: %v", err)
			os.Exit(1)
		}
	case args[0] == "--sdp":
		fmt.Print(sdpRecord(deviceName()))
	case args[0] == "--check":
		os.Exit(check())
	case args[0] == "--preview" && len(args) == 2:
		if err := preview(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func check() int {
	ok := true
	bus, err := dialSystemBus()
	if err != nil {
		fmt.Println("system bus: FAIL", err)
		return 1
	}
	defer bus.close()
	fmt.Println("system bus: ok")
	for _, p := range []string{"Address", "Alias", "Class", "Powered", "Discoverable", "Pairable", "UUIDs"} {
		v, err := bus.getProp(adapterPath, ifAdapter, p)
		if err != nil {
			fmt.Printf("adapter %s: FAIL %v\n", p, err)
			ok = false
			continue
		}
		if n, isNum := v.(uint32); isNum {
			fmt.Printf("adapter %s: 0x%06x\n", p, n)
		} else {
			fmt.Printf("adapter %s: %v\n", p, v)
		}
	}
	for _, psm := range []uint16{psmHIDControl, psmHIDIntr} {
		fd, err := l2capListen(psm)
		if err != nil {
			fmt.Printf("PSM 0x%02x: FAIL %v\n", psm, err)
			ok = false
			continue
		}
		syscall.Close(fd)
		fmt.Printf("PSM 0x%02x: free\n", psm)
	}
	if !ok {
		return 1
	}
	return 0
}

// ---- known hosts (most recent first) ----

func hostsPath() string { return filepath.Join(dataDir(), "hosts.txt") }

func loadHosts() []bdaddr {
	f, err := os.Open(hostsPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []bdaddr
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if a, err := parseAddr(fields[0]); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// ---- settings (dpad = dpad | stick) ----

func settingsPath() string { return filepath.Join(dataDir(), "settings.txt") }

func loadStick() bool {
	data, _ := os.ReadFile(settingsPath())
	for _, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if ok && strings.TrimSpace(k) == "dpad" {
			return strings.TrimSpace(v) == "stick"
		}
	}
	return false
}

func saveStick(on bool) {
	mode := "dpad"
	if on {
		mode = "stick"
	}
	os.MkdirAll(dataDir(), 0o755)
	tmp := settingsPath() + ".new"
	text := "# Gamepad.pak: d-pad sent as the d-pad or as the left stick (MENU + SELECT)\ndpad = " + mode + "\n"
	if os.WriteFile(tmp, []byte(text), 0o644) == nil {
		os.Rename(tmp, settingsPath())
	}
}

func rememberHost(a bdaddr, name string) {
	lines := []string{a.String() + "\t" + name}
	if data, err := os.ReadFile(hostsPath()); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if l != "" && !strings.HasPrefix(l, a.String()) {
				lines = append(lines, l)
			}
		}
	}
	if len(lines) > 8 {
		lines = lines[:8]
	}
	os.MkdirAll(dataDir(), 0o755)
	tmp := hostsPath() + ".new"
	if os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644) == nil {
		os.Rename(tmp, hostsPath())
	}
}

// ---- session ----

type incoming struct {
	psm  uint16
	fd   int
	addr bdaddr
}

type ctrlMsg struct {
	gen  int
	data []byte
}

type chanDown struct {
	gen int
	err error
}

type named struct {
	addr bdaddr
	name string
}

type session struct {
	addr       bdaddr
	ctrl, intr int
	gen        int
	name       string
	protocol   byte
}

func (s *session) close() {
	closeSocket(s.intr)
	closeSocket(s.ctrl)
}

// closeSocket shuts the socket down first: a plain close does not wake a
// goroutine blocked in read on it.
func closeSocket(fd int) {
	syscall.Shutdown(fd, syscall.SHUT_RDWR)
	syscall.Close(fd)
}

func acceptLoop(fd int, psm uint16, out chan<- incoming, done <-chan struct{}) {
	for {
		c, a, err := l2capAccept(fd)
		if isDone(done) {
			if err == nil {
				syscall.Close(c)
			}
			return
		}
		if errors.Is(err, syscall.EAGAIN) {
			continue // accept timeout, see l2capListen
		}
		if err != nil {
			logf("accept PSM 0x%02x: %v", psm, err)
			if errors.Is(err, syscall.EBADF) || errors.Is(err, syscall.EINVAL) {
				return
			}
			time.Sleep(time.Second)
			continue
		}
		logf("incoming PSM 0x%02x from %s", psm, a)
		select {
		case out <- incoming{psm, c, a}:
		case <-done:
			syscall.Close(c)
			return
		}
	}
}

func readLoop(fd, gen int, ctrl bool, msgs chan<- ctrlMsg, down chan<- chanDown, done <-chan struct{}) {
	b := make([]byte, 1024)
	for {
		n, err := l2capRead(fd, b)
		if err != nil {
			select {
			case down <- chanDown{gen, err}:
			case <-done:
			}
			return
		}
		if ctrl {
			select {
			case msgs <- ctrlMsg{gen, append([]byte(nil), b[:n]...)}:
			case <-done:
				return
			}
		}
	}
}

func isDone(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// connectOut opens control then interrupt to a known host.
func connectOut(a bdaddr, out chan<- incoming, done <-chan struct{}) error {
	c, err := l2capConnect(a, psmHIDControl)
	if err != nil {
		return err
	}
	i, err := l2capConnect(a, psmHIDIntr)
	if err != nil {
		syscall.Close(c)
		return err
	}
	for _, x := range []incoming{{psmHIDControl, c, a}, {psmHIDIntr, i, a}} {
		select {
		case out <- x:
		case <-done:
			syscall.Close(c)
			syscall.Close(i)
			return nil
		}
	}
	return nil
}

// statusErr is a failure shown in the status chip.
type statusErr struct {
	status string
	err    error
}

func (e statusErr) Error() string { return e.status + ": " + e.err.Error() }

// btSwitch runs launch.sh --bt-on / --bt-off (bluetoothd for the pad, or
// the stock one back).
func btSwitch(on bool) error {
	script := os.Getenv("GAMEPAD_LAUNCH")
	if script == "" {
		return statusErr{"Bluetooth failed", errors.New("GAMEPAD_LAUNCH not set (run from launch.sh)")}
	}
	arg := "--bt-off"
	if on {
		arg = "--bt-on"
	}
	cmd := exec.Command("/bin/sh", script, arg)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 2 {
		return statusErr{"Bluetooth is off in Settings", err}
	}
	if err != nil {
		return statusErr{"Bluetooth failed", err}
	}
	return nil
}

// run: the key tester (Bluetooth untouched) until MENU + START turns the
// pad on; padMode runs until the pad is turned off again or the user quits.
func run() error {
	fb, err := openFramebuffer()
	if err != nil {
		return err
	}
	defer fb.close()
	scr := newScreen(fb)
	keys := make(chan evKey, 64)
	if listenConsole(keys) == 0 {
		return errors.New("no console input device")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	var (
		pad    padState
		filter keyFilter
	)
	pad.stick = loadStick()
	v := view{device: "Bluetooth off", state: stOff, stick: pad.stick}
	redraw := func() { scr.draw(v); scr.present() }
	for {
		redraw()
		if quit := tester(keys, sig, &v, &pad, &filter, redraw); quit {
			return nil
		}
		v.on, v.device, v.state, v.reconnect = true, "Starting Bluetooth", stStarting, false
		redraw()
		err := btSwitch(true)
		quit := false
		if err == nil {
			quit, err = padMode(keys, sig, &v, &pad, &filter, redraw)
			v.on, v.device, v.state, v.reconnect = false, "Stopping Bluetooth", stStarting, false
			redraw()
			if e := btSwitch(false); e != nil {
				logf("bluetooth back: %v", e)
			}
		}
		if quit {
			return nil
		}
		v.on, v.device, v.state, v.reconnect = false, "Bluetooth off", stOff, false
		if err != nil {
			logf("%v", err)
			var se statusErr
			if errors.As(err, &se) {
				v.device = se.status
			} else {
				v.device = "Bluetooth failed"
			}
			v.state = stError
		}
	}
}

// tester shows the keys as Tools > Input does; true when the user quits,
// false on MENU + START (turn the pad on).
func tester(keys <-chan evKey, sig <-chan os.Signal, v *view, pad *padState, filter *keyFilter, redraw func()) bool {
	for {
		select {
		case <-sig:
			return true
		case k := <-keys:
			out := filter.feed(k)
			if out.quit {
				return true
			}
			if out.toggleBT {
				return false
			}
			// MENU + SELECT (d-pad as stick) only means something with the
			// pad on: ignored here, like its hint.
			if out.pass {
				pad.apply(k)
			}
			v.pad, v.menu = *pad, filter.menuHeld
			redraw()
		}
	}
}

// padMode is the Bluetooth pad, bluetoothd already restarted for it.
// It returns quit = true when the user quits, false when the pad is
// turned off (MENU + START) or fails.
func padMode(keys <-chan evKey, sig <-chan os.Signal, v *view, pad *padState, filter *keyFilter, redraw func()) (bool, error) {
	done := make(chan struct{})
	name := deviceName()
	z, err := newBluez(logf)
	if err != nil {
		return false, statusErr{"Bluetooth off", err}
	}
	defer z.stop()
	ctrlFd, err := l2capListen(psmHIDControl)
	if err != nil {
		return false, statusErr{"Bluetooth busy", err}
	}
	defer closeSocket(ctrlFd)
	intrFd, err := l2capListen(psmHIDIntr)
	if err != nil {
		return false, statusErr{"Bluetooth busy", err}
	}
	defer closeSocket(intrFd)
	if err := z.start(name); err != nil {
		return false, statusErr{"Bluetooth failed", err}
	}

	in := make(chan incoming, 8)
	msgs := make(chan ctrlMsg, 16)
	down := make(chan chanDown, 4)
	names := make(chan named, 2)
	var accepting sync.WaitGroup
	accepting.Add(2)
	go func() { defer accepting.Done(); acceptLoop(ctrlFd, psmHIDControl, in, done) }()
	go func() { defer accepting.Done(); acceptLoop(intrFd, psmHIDIntr, in, done) }()
	// Runs before the listening sockets are closed: both accept loops must
	// have returned, or the PSMs stay bound for the next pad on.
	defer func() {
		close(done)
		stopped := make(chan struct{})
		go func() { accepting.Wait(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			logf("accept loops still running: PSM 17/19 may stay bound")
		}
	}()

	var (
		sess      *session
		gen       int
		pending   = map[bdaddr]int{}
		connected atomic.Bool
		dialing   atomic.Bool
		dirty     = true
	)
	defer func() {
		for _, fd := range pending {
			syscall.Close(fd)
		}
	}()
	paired := z.paired()
	var last bdaddr
	var lastName string
	haveLast := false
	for _, a := range loadHosts() {
		if n, ok := paired[a]; ok {
			last, lastName, haveLast = a, n, true
			break
		}
	}

	dial := func() {
		if !haveLast || connected.Load() || !dialing.CompareAndSwap(false, true) {
			return
		}
		v.device, v.state, v.reconnect = "Calling "+lastName, stReconnecting, false
		dirty = true
		go func() {
			defer dialing.Store(false)
			if err := connectOut(last, in, done); err != nil {
				logf("reconnect %s: %v", last, err)
				select {
				case names <- named{}: // back to the waiting text
				case <-done:
				}
			}
		}()
	}
	waiting := func() {
		// Waiting: our own name, as the host's Bluetooth settings list it.
		v.device, v.state, v.reconnect = "Visible as "+name, stReady, haveLast
		dirty = true
	}
	waiting()
	dial()

	write := func(r []byte) {
		if sess == nil {
			return
		}
		if _, err := syscall.Write(sess.intr, r); err != nil {
			logf("send: %v", err)
		}
	}
	send := func() { write(pad.report()) }
	endSession := func(why string) {
		if sess == nil {
			return
		}
		logf("disconnected from %s: %s", sess.addr, why)
		sess.close()
		sess = nil
		connected.Store(false)
		gen++
		if err := z.discoverable(true); err != nil {
			logf("discoverable: %v", err)
		}
		waiting()
	}
	defer endSession("pad off")

	tick := time.NewTicker(33 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			return true, nil
		case k := <-keys:
			out := filter.feed(k)
			if out.toggleBT {
				return false, nil
			}
			if v.menu != filter.menuHeld {
				v.menu, dirty = filter.menuHeld, true
			}
			if out.toggleStick {
				pad.stick = !pad.stick
				saveStick(pad.stick)
				logf("d-pad as left stick: %v", pad.stick)
				v.stick, dirty = pad.stick, true
				send()
				continue
			}
			if out.home {
				// Xbox button: in report 1's bitmap and in report 2 (AC Home).
				pad.set(hidHome, true)
				send()
				write(pad.homeReport())
				time.Sleep(60 * time.Millisecond)
				pad.set(hidHome, false)
				send()
				write(pad.homeReport())
			}
			if !out.pass {
				continue
			}
			if sess == nil && k.value != 0 && k.code == codeX {
				dial()
				continue
			}
			if pad.apply(k) {
				send()
				v.pad, dirty = *pad, true
			}
			if out.quit {
				return true, nil
			}
		case c := <-in:
			switch {
			case sess != nil:
				logf("busy, refusing %s", c.addr)
				syscall.Close(c.fd)
			case c.psm == psmHIDControl:
				if old, ok := pending[c.addr]; ok {
					syscall.Close(old)
				}
				pending[c.addr] = c.fd
			default:
				ctl, ok := pending[c.addr]
				if !ok {
					logf("interrupt channel without control from %s", c.addr)
					syscall.Close(c.fd)
					continue
				}
				delete(pending, c.addr)
				for a, fd := range pending { // one host at a time
					syscall.Close(fd)
					delete(pending, a)
				}
				gen++
				sess = &session{addr: c.addr, ctrl: ctl, intr: c.fd, gen: gen, protocol: 1}
				connected.Store(true)
				go readLoop(ctl, gen, true, msgs, down, done)
				go readLoop(c.fd, gen, false, msgs, down, done)
				logf("connected to %s", c.addr)
				if err := z.discoverable(false); err != nil {
					logf("hide: %v", err)
				}
				v.device, v.state, v.reconnect = c.addr.String(), stConnected, false
				dirty = true
				send()
				go func(a bdaddr) {
					select {
					case names <- named{a, z.hostInfo(a)}:
					case <-done:
					}
				}(c.addr)
			}
		case m := <-msgs:
			if sess == nil || m.gen != sess.gen {
				continue
			}
			reply, unplug := controlReply(m.data, *pad, &sess.protocol)
			logf("control % x -> % x", m.data, reply)
			if reply != nil {
				syscall.Write(sess.ctrl, reply)
			}
			if unplug {
				endSession("virtual cable unplug")
			}
		case d := <-down:
			if sess != nil && d.gen == sess.gen {
				endSession(d.err.Error())
			}
		case n := <-names:
			if n.name == "" {
				if sess == nil {
					waiting()
				}
				continue
			}
			if sess != nil && n.addr == sess.addr {
				sess.name = n.name
				v.device = n.name
				dirty = true
				rememberHost(n.addr, n.name)
				last, lastName, haveLast = n.addr, n.name, true
			}
		case <-tick.C:
			if dirty {
				redraw()
				dirty = false
			}
		}
	}
}

func preview(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	scr := newScreen(previewFramebuffer())
	var p padState
	p.set(hidSouth, true)
	p.set(hidL1, true)
	p.up, p.right = true, true
	views := map[string]view{
		"tester":         {device: "Bluetooth off", state: stOff, pad: p},
		"tester-menu":    {device: "Bluetooth off", state: stOff, menu: true},
		"connected-menu": {device: "Thomas’s MacBook Pro", state: stConnected, on: true, menu: true, stick: true},
		"waiting":        {device: "Visible as RG35XX SP Gamepad", state: stReady, on: true, reconnect: true},
		"reconnecting":   {device: "Calling Thomas’s MacBook Pro", state: stReconnecting, on: true},
		"connected":      {device: "Thomas’s MacBook Pro", state: stConnected, on: true, pad: p, menu: true},
		"stick":          {device: "Thomas’s MacBook Pro", state: stConnected, on: true, stick: true},
		"error":          {device: "Bluetooth is off in Settings", state: stError},
	}
	for n, v := range views {
		scr.draw(v)
		if err := savePNG(scr, filepath.Join(dir, n+".png")); err != nil {
			return err
		}
	}
	return nil
}
