package main

// Just enough of the MPD protocol, one connection per request (mpd accepts
// many; nothing to keep alive or reconnect). Commands run as a command list
// so a batch (clear + 50 addid + play) is one round trip.

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type mpd struct{ socket string }

func newMPD(socket string) *mpd { return &mpd{socket} }

type mpdError struct{ msg string }

func (e *mpdError) Error() string { return "mpd: " + e.msg }

func quoteArg(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}

func cmdLine(args ...string) string {
	parts := []string{args[0]}
	for _, a := range args[1:] {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func (m *mpd) dial() (net.Conn, *bufio.Reader, error) {
	network := "unix"
	if strings.Contains(m.socket, ":") && !strings.HasPrefix(m.socket, "/") {
		network = "tcp"
	}
	conn, err := net.DialTimeout(network, m.socket, 2*time.Second)
	if err != nil {
		return nil, nil, errors.New("mpd is not running")
	}
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	r := bufio.NewReader(conn)
	hello, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(hello, "OK MPD") {
		conn.Close()
		return nil, nil, errors.New("mpd did not greet")
	}
	return conn, r, nil
}

// pair is one "key: value" answer line.
type pair struct{ k, v string }

// run sends commands as one list; returns every answer line, in order.
func (m *mpd) run(cmds ...string) ([]pair, error) {
	conn, r, err := m.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var b strings.Builder
	if len(cmds) > 1 {
		b.WriteString("command_list_begin\n")
	}
	for _, c := range cmds {
		b.WriteString(c)
		b.WriteByte('\n')
	}
	if len(cmds) > 1 {
		b.WriteString("command_list_end\n")
	}
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return nil, err
	}
	var out []pair
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return out, err
		}
		line = strings.TrimRight(line, "\n")
		if line == "OK" {
			return out, nil
		}
		if strings.HasPrefix(line, "ACK ") {
			msg := line
			if i := strings.Index(line, "} "); i >= 0 {
				msg = line[i+2:]
			}
			return out, &mpdError{msg}
		}
		k, v, _ := strings.Cut(line, ": ")
		out = append(out, pair{k, v})
	}
}

func toMap(ps []pair) map[string]string {
	m := map[string]string{}
	for _, p := range ps {
		m[p.k] = p.v
	}
	return m
}

func (m *mpd) status() (map[string]string, error) {
	ps, err := m.run("status")
	return toMap(ps), err
}

func (m *mpd) current() (map[string]string, error) {
	ps, err := m.run("currentsong")
	return toMap(ps), err
}

// queueEntry is one song of mpd's queue (playlistinfo).
type queueEntry struct {
	pos, id int
	file    string
	title   string
	artist  string
}

func (m *mpd) queue() ([]queueEntry, error) {
	ps, err := m.run("playlistinfo")
	if err != nil {
		return nil, err
	}
	var out []queueEntry
	for _, p := range ps {
		switch p.k {
		case "file":
			out = append(out, queueEntry{file: p.v})
		case "Pos", "Id", "Title", "Artist":
			if len(out) == 0 {
				continue
			}
			e := &out[len(out)-1]
			switch p.k {
			case "Pos":
				e.pos, _ = strconv.Atoi(p.v)
			case "Id":
				e.id, _ = strconv.Atoi(p.v)
			case "Title":
				e.title = p.v
			case "Artist":
				e.artist = p.v
			}
		}
	}
	return out, nil
}

// track is what mpd needs to queue a song: its URL and the tags shown by
// mpd clients (remote streams have no tags of their own).
type track struct {
	url, title, artist, album string
}

func addCmds(t track) []string {
	return []string{cmdLine("addid", t.url)}
}

// replace clears the queue, adds tracks and starts playing at index start.
func (m *mpd) replace(tracks []track, start int) error {
	cmds := []string{"clear"}
	for _, t := range tracks {
		cmds = append(cmds, addCmds(t)...)
	}
	if _, err := m.run(cmds...); err != nil {
		return err
	}
	if err := m.tagQueue(0, tracks); err != nil {
		return err
	}
	_, err := m.run(fmt.Sprintf("play %d", start))
	return err
}

// add appends tracks at the end of the queue.
func (m *mpd) add(tracks []track) error {
	st, err := m.status()
	if err != nil {
		return err
	}
	first, _ := strconv.Atoi(st["playlistlength"])
	var cmds []string
	for _, t := range tracks {
		cmds = append(cmds, addCmds(t)...)
	}
	if _, err := m.run(cmds...); err != nil {
		return err
	}
	return m.tagQueue(first, tracks)
}

// tagQueue sets Title/Artist/Album on the songs from position first on.
func (m *mpd) tagQueue(first int, tracks []track) error {
	q, err := m.queue()
	if err != nil {
		return err
	}
	var cmds []string
	for i, t := range tracks {
		if first+i >= len(q) {
			break
		}
		id := strconv.Itoa(q[first+i].id)
		for _, tag := range [][2]string{{"Title", t.title}, {"Artist", t.artist}, {"Album", t.album}} {
			if tag[1] != "" {
				cmds = append(cmds, cmdLine("addtagid", id, tag[0], tag[1]))
			}
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	_, err = m.run(cmds...)
	return err
}

func (m *mpd) simple(cmd string) error {
	_, err := m.run(cmd)
	return err
}
