package main

// bluetoothd side: the adapter in pairing mode, a Just Works agent, the HID
// SDP record. bluetoothd must run without its input plugin (launch.sh
// restarts it with -P input), otherwise PSM 17/19 belong to it.

import (
	"fmt"
	"strings"
	"time"
)

const (
	adapterPath = objectPath("/org/bluez/hci0")
	agentPath   = objectPath("/org/nextui/gamepad/agent")
	profilePath = objectPath("/org/nextui/gamepad/profile")
	ifAdapter   = "org.bluez.Adapter1"
	ifDevice    = "org.bluez.Device1"
)

type bluez struct {
	bus   *conn
	saved map[string]variant // adapter properties to put back
	log   func(string, ...any)
}

func newBluez(log func(string, ...any)) (*bluez, error) {
	bus, err := dialSystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	z := &bluez{bus: bus, saved: map[string]variant{}, log: log}
	bus.objects[agentPath] = z.agent
	bus.objects[profilePath] = z.profile
	return z, nil
}

// agent accepts everything: Just Works pairing while the pak is open.
func (z *bluez) agent(m *message) (string, []any, *dbusError) {
	z.log("agent: %s %v", m.member, m.body)
	switch m.member {
	case "RequestPinCode":
		return "s", []any{"0000"}, nil
	case "RequestPasskey":
		return "u", []any{uint32(0)}, nil
	}
	return "", nil, nil // Release, Display*, RequestConfirmation, RequestAuthorization, AuthorizeService, Cancel
}

func (z *bluez) profile(m *message) (string, []any, *dbusError) {
	z.log("profile: %s", m.member)
	if m.member == "NewConnection" { // not expected: we own the PSMs ourselves
		return "", nil, &dbusError{"org.bluez.Error.Rejected", "connections go to the L2CAP sockets"}
	}
	return "", nil, nil
}

// start: adapter on, named, discoverable; SDP record and agent registered.
func (z *bluez) start(name string) error {
	for _, p := range []string{"Alias", "Discoverable", "DiscoverableTimeout", "Pairable", "PairableTimeout"} {
		if v, err := z.bus.getProp(adapterPath, ifAdapter, p); err == nil {
			sig := "b"
			switch v.(type) {
			case string:
				sig = "s"
			case uint32:
				sig = "u"
			}
			z.saved[p] = variant{sig, v}
		}
	}
	if err := z.powerOn(); err != nil {
		return fmt.Errorf("power on: %w", err)
	}
	if err := z.bus.setProp(adapterPath, ifAdapter, "Alias", variant{"s", name}); err != nil {
		z.log("alias: %v", err)
	}
	opts := map[string]variant{
		"Name":                  {"s", name},
		"Role":                  {"s", "server"},
		"ServiceRecord":         {"s", sdpRecord(name)},
		"RequireAuthentication": {"b", true},
		"RequireAuthorization":  {"b", false},
		"AutoConnect":           {"b", false},
	}
	if _, err := z.bus.call("org.bluez", "/org/bluez", "org.bluez.ProfileManager1", "RegisterProfile", "osa{sv}",
		profilePath, hidUUID, opts); err != nil {
		return fmt.Errorf("SDP record: %w", err)
	}
	if _, err := z.bus.call("org.bluez", "/org/bluez", "org.bluez.AgentManager1", "RegisterAgent", "os",
		agentPath, "NoInputNoOutput"); err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	if _, err := z.bus.call("org.bluez", "/org/bluez", "org.bluez.AgentManager1", "RequestDefaultAgent", "o", agentPath); err != nil {
		z.log("default agent: %v", err)
	}
	for _, p := range []struct {
		name string
		v    variant
	}{{"PairableTimeout", variant{"u", uint32(0)}}, {"Pairable", variant{"b", true}},
		{"DiscoverableTimeout", variant{"u", uint32(0)}}} {
		if err := z.bus.setProp(adapterPath, ifAdapter, p.name, p.v); err != nil {
			z.log("%s: %v", p.name, err)
		}
	}
	return z.discoverable(true)
}

// powerOn waits for the adapter: right after its restart bluetoothd is
// still powering it on (AutoEnable) and answers Busy meanwhile.
func (z *bluez) powerOn() error {
	var err error
	for i := 0; i < 50; i++ {
		if on, _ := z.bus.getProp(adapterPath, ifAdapter, "Powered"); on == true {
			return nil
		}
		err = z.bus.setProp(adapterPath, ifAdapter, "Powered", variant{"b", true})
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

func (z *bluez) discoverable(on bool) error {
	return z.bus.setProp(adapterPath, ifAdapter, "Discoverable", variant{"b", on})
}

// stop puts the adapter properties back (the D-Bus objects go away with the
// connection).
func (z *bluez) stop() {
	z.bus.call("org.bluez", "/org/bluez", "org.bluez.ProfileManager1", "UnregisterProfile", "o", profilePath)
	z.bus.call("org.bluez", "/org/bluez", "org.bluez.AgentManager1", "UnregisterAgent", "o", agentPath)
	for _, p := range []string{"DiscoverableTimeout", "Discoverable", "PairableTimeout", "Pairable", "Alias"} {
		if v, ok := z.saved[p]; ok {
			if err := z.bus.setProp(adapterPath, ifAdapter, p, v); err != nil {
				z.log("restore %s: %v", p, err)
			}
		}
	}
	z.bus.close()
}

// hostInfo returns the host's name and trusts it, so it reconnects without
// a prompt.
func (z *bluez) hostInfo(a bdaddr) string {
	path := a.devicePath()
	if err := z.bus.setProp(path, ifDevice, "Trusted", variant{"b", true}); err != nil {
		z.log("trust %s: %v", a, err)
	}
	for _, p := range []string{"Alias", "Name"} {
		if v, err := z.bus.getProp(path, ifDevice, p); err == nil {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return a.String()
}

// paired lists the bonded devices (candidates for reconnection).
func (z *bluez) paired() map[bdaddr]string {
	out := map[bdaddr]string{}
	r, err := z.bus.call("org.bluez", "/", "org.freedesktop.DBus.ObjectManager", "GetManagedObjects", "")
	if err != nil || len(r) != 1 {
		return out
	}
	objs, _ := r[0].(map[any]any)
	for _, ifaces := range objs {
		m, _ := ifaces.(map[any]any)
		props, ok := m[ifDevice].(map[string]variant)
		if !ok {
			continue
		}
		if p, _ := props["Paired"].val.(bool); !p {
			continue
		}
		s, _ := props["Address"].val.(string)
		a, err := parseAddr(s)
		if err != nil {
			continue
		}
		name, _ := props["Alias"].val.(string)
		out[a] = name
	}
	return out
}
