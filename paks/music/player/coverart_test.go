package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

var pic = []byte("\xff\xd8\xff\xe0JPEGDATA")

func syncsafe(n int) []byte {
	return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

func TestID3Picture(t *testing.T) {
	for _, ver := range []byte{3, 4} {
		body := append([]byte{0}, "image/jpeg\x00\x03cover\x00"...)
		body = append(body, pic...)
		var frame bytes.Buffer
		frame.WriteString("TIT2")
		frame.Write([]byte{0, 0, 0, 2, 0, 0, 0, 'x'})
		frame.WriteString("APIC")
		if ver == 4 {
			frame.Write(syncsafe(len(body)))
		} else {
			binary.Write(&frame, binary.BigEndian, uint32(len(body)))
		}
		frame.Write([]byte{0, 0})
		frame.Write(body)
		tag := append([]byte{'I', 'D', '3', ver, 0, 0}, syncsafe(frame.Len())...)
		tag = append(tag, frame.Bytes()...)
		got, err := id3Picture(tag)
		if err != nil || !bytes.Equal(got, pic) {
			t.Fatalf("v2.%d: %q %v", ver, got, err)
		}
	}
	// UTF-16 description
	body := append([]byte{1}, "image/png\x00\x03\xff\xfec\x00\x00\x00"...)
	body = append(body, pic...)
	tag := append([]byte{'I', 'D', '3', 3, 0, 0}, syncsafe(len(body)+10)...)
	tag = append(tag, "APIC"...)
	tag = binary.BigEndian.AppendUint32(tag, uint32(len(body)))
	tag = append(tag, 0, 0)
	tag = append(tag, body...)
	if got, err := id3Picture(tag); err != nil || !bytes.Equal(got, pic) {
		t.Fatalf("utf16: %q %v", got, err)
	}
}

func atom(name string, body ...[]byte) []byte {
	b := bytes.Join(body, nil)
	return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(b))), append([]byte(name), b...)...)
}

func TestMP4Covr(t *testing.T) {
	data := atom("data", []byte{0, 0, 0, 13, 0, 0, 0, 0}, pic)
	moov := bytes.Join([][]byte{atom("mvhd", make([]byte, 12)),
		atom("udta", atom("meta", []byte{0, 0, 0, 0}, atom("hdlr", make([]byte, 8)), atom("ilst", atom("\xa9nam", atom("data", make([]byte, 9))), atom("covr", data))))}, nil)
	got, err := mp4Covr(moov)
	if err != nil || !bytes.Equal(got, pic) {
		t.Fatalf("covr: %q %v", got, err)
	}
	if _, err := mp4Covr(atom("mvhd")); err != errNoArt {
		t.Fatal("no udta must give errNoArt")
	}
}

func TestParseRadios(t *testing.T) {
	got := parseRadios(bufio.NewScanner(strings.NewReader(defaultRadios + "bad line\nX | ftp://no\n")))
	if len(got) != 8 || got[0].name != "FIP" || !strings.HasPrefix(got[0].url, "https://") {
		t.Fatalf("radios: %+v", got)
	}
}

func TestStatusRow(t *testing.T) {
	s := statusState{charge: 50, wifi: 2, vpn: vpnOn, music: true}
	l, w := statusRow(s, rgba{255, 255, 255, 255}, 0)
	// battery 22 + wifi 16 + vpn 11 + note 14 units, 3 gaps, padding
	if want := 22 + 16 + 11 + 14 + 3*ssGap + 2*ssPadX; w != want || l.h != ssGlyph+2*ssPadY {
		t.Fatalf("width %d, want %d", w, want)
	}
	lit := 0
	for _, a := range l.a {
		if a > 0.5 {
			lit++
		}
	}
	if lit < 200 {
		t.Fatalf("row looks empty: %d pixels", lit)
	}
}

func TestParseICY(t *testing.T) {
	cases := []struct{ raw, title, artist string }{
		{"Nina Simone - Feeling Good", "Feeling Good", "Nina Simone"},
		{"StreamTitle='Daft Punk - One More Time';StreamUrl='';", "One More Time", "Daft Punk"},
		{"'Air – La femme d'argent'", "La femme d'argent", "Air"},
		{"Serge Gainsbourg - Je t'aime... moi non plus - Live", "Je t'aime... moi non plus - Live", "Serge Gainsbourg"},
		{"Simon &amp; Garfunkel - The Boxer", "The Boxer", "Simon & Garfunkel"},
		{"Beyonc\xe9 - Halo", "Halo", "Beyoncé"}, // Latin-1
		{"  Justice   -   D.A.N.C.E.  ", "D.A.N.C.E.", "Justice"},
		{"Le 7/10", "Le 7/10", "FIP"},
		{"", "FIP", "Live radio"},
		{"FIP", "FIP", "Live radio"},
		{"https://www.fip.fr", "FIP", "Live radio"},
		{" - ", "FIP", "Live radio"},
		{"Unknown - Unknown", "FIP", "Live radio"},
		{"12345", "FIP", "Live radio"},
		{"Massive Attack - ", "Massive Attack", "FIP"},
		{"Bonobo - Unknown", "Bonobo", "FIP"},
		{"line\x00with\x01control - chars\n", "chars", "line with control"},
	}
	for _, c := range cases {
		title, artist := parseICY(c.raw, "FIP")
		if title != c.title || artist != c.artist {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", c.raw, title, artist, c.title, c.artist)
		}
	}
}

// Random and truncated inputs must never panic.
func TestParsersSurviveGarbage(t *testing.T) {
	seed := uint32(1)
	rnd := func() byte { seed = seed*1664525 + 1013904223; return byte(seed >> 24) }
	for n := 0; n < 20000; n++ {
		b := make([]byte, int(rnd())%300)
		for i := range b {
			b[i] = rnd()
		}
		if len(b) >= 10 && n%2 == 0 {
			copy(b, "ID3\x03\x00\x00")
		}
		if len(b) >= 10 {
			id3Picture(b)
		}
		flacPictureData(b)
		mp4Covr(b)
		parseICY(string(b), "X")
	}
}
