package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadWrapPreservesLines(t *testing.T) {
	got := readWrap(strings.NewReader("abcde\n\nxyz\n"), 3, "")
	want := []string{"abc", "de", "", "xyz"}
	if len(got) != len(want) {
		t.Fatalf("lines=%q, want=%q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("line %d=%q want %q", i, got[i], want[i])
		}
	}
}

// Un fichier illisible n'est pas fatal : le lecteur affiche l'erreur et laisse
// sortir avec B, au lieu de rendre la main sans rien montrer.
func TestOpenMissingFile(t *testing.T) {
	u := &ui{fb: &framebuffer{w: 640, h: 480}}
	u.open(filepath.Join(t.TempDir(), "absent.txt"))
	if len(u.lines) == 0 || !strings.Contains(u.lines[0], "Cannot read") {
		t.Fatalf("lines=%v", u.lines)
	}
}

func TestRgb565(t *testing.T) {
	fb := &framebuffer{chanR: channel{11, 5}, chanG: channel{5, 6}, chanB: channel{0, 5}}
	if got := fb.pack(255, 0, 0); got != 0xf800 {
		t.Fatalf("red=%x", got)
	}
	if got := fb.pack(0, 255, 0); got != 0x07e0 {
		t.Fatalf("green=%x", got)
	}
}

func TestMenuKeyH700(t *testing.T) {
	for _, test := range []struct {
		code   uint16
		action string
	}{
		{354, "menu"}, {304, "open"}, {305, "back"}, {315, "start"},
	} {
		ev, ok := translateEvdev(1, test.code, 1)
		if !ok || ev.action != test.action || !ev.pressed {
			t.Fatalf("key %d: %#v %v", test.code, ev, ok)
		}
	}
	_, ok := translateEvdev(1, 316, 1)
	if ok {
		t.Fatal("BTN_MODE is not MENU")
	}
}

// Le lecteur ne sert qu'à lire : B rend la main au menu du pak, A ne fait rien.
func TestNavigationDirectFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "log.txt")
	var content strings.Builder
	for i := 1; i <= 40; i++ {
		content.WriteString("ligne ")
		content.WriteString(strings.Repeat("x", i%5))
		content.WriteString("\n")
	}
	if err := os.WriteFile(file, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	u := newUI(&framebuffer{w: 640, h: 480}, options{file: file})
	if u.title != "log.txt" {
		t.Fatalf("title=%q (le nom du fichier sert de titre par défaut)", u.title)
	}
	held := map[string]bool{}
	if u.apply(keyEvent{action: "down", pressed: true}, held) {
		t.Fatal("BAS quitte")
	}
	if u.offset != 1 {
		t.Fatalf("offset=%d", u.offset)
	}
	if u.apply(keyEvent{action: "open", pressed: true}, held) {
		t.Fatal("A quitte alors qu'il ne fait rien")
	}
	if u.offset != 1 {
		t.Fatalf("A a bougé l'affichage : offset=%d", u.offset)
	}
	if !u.apply(keyEvent{action: "back", pressed: true}, held) {
		t.Fatal("B doit rendre la main au menu du pak")
	}
	if !u.apply(keyEvent{action: "menu", pressed: true}, held) {
		t.Fatal("MENU doit quitter")
	}
}

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"--file", "/tmp/a.txt", "--title", "Journal"})
	if err != nil || o.file != "/tmp/a.txt" || o.title != "Journal" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	o, err = parseArgs([]string{"--preview", "/tmp/p.png"})
	if err != nil || o.preview != "/tmp/p.png" || o.file != os.DevNull {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if _, err = parseArgs([]string{"--inconnu"}); err == nil {
		t.Fatal("option inconnue acceptée")
	}
	if _, err = parseArgs([]string{"--list", "/tmp/l.txt"}); err == nil {
		t.Fatal("--list n'existe plus : les menus sont des listes minui-list")
	}
	if _, err = parseArgs([]string{"--file"}); err == nil {
		t.Fatal("valeur manquante acceptée")
	}
	if _, err = parseArgs([]string{"--help"}); err != errUsage {
		t.Fatalf("--help: %v", err)
	}
}

func TestDrawNoPanic(t *testing.T) {
	fb := &framebuffer{w: 640, h: 480, virtH: 480, stride: 1280, bpp: 16,
		chanR: channel{11, 5}, chanG: channel{5, 6}, chanB: channel{0, 5}, mapped: make([]byte, 640*480*2)}
	u := &ui{fb: fb, cvs: newCanvas(fb), title: "Journaux"}
	u.draw() // aucun texte chargé
	u.lines = []string{"Celeste ASTC: OK", "Test"}
	u.draw()
	u.lines = make([]string, 400)
	u.offset = 200
	u.draw() // texte plus long qu'un écran : compteur affiché
}

func TestFontAntialiasing(t *testing.T) {
	if len(fontAtlas) < 100 {
		t.Fatal("empty embedded font atlas")
	}
	foundGray := false
	for _, a := range fontAtlas {
		if a > 0 && a < 255 {
			foundGray = true
			break
		}
	}
	if !foundGray {
		t.Fatal("font pixels have no antialiasing")
	}
}

// Le corps du texte doit être à chasse fixe : c'est ce qui distingue le lecteur
// de journaux des écrans de menu.
func TestBodyFontIsMonospace(t *testing.T) {
	for _, s := range []string{"i", "W", "M", "0", " "} {
		if got, want := fontWidth(&faceBody, s), fontWidth(&faceBody, "M"); got != want {
			t.Fatalf("faceBody(%q)=%d, attendu %d (monospace)", s, got, want)
		}
	}
}

func TestProportionalMenuGlyphs(t *testing.T) {
	wide := fontWidth(&faceMenu, "WWW")
	narrow := fontWidth(&faceMenu, "iii")
	if wide <= narrow {
		t.Fatalf("menu must be proportional: WWW=%d, iii=%d", wide, narrow)
	}
	if len(pixelTruncate(&faceMenu, "Journal du pak", 120)) == 0 {
		t.Fatal("truncated title empty")
	}
}

// Sans rappels de boutons, l'écran 640x480 tient 22 lignes au lieu de 15 :
// c'est tout l'intérêt de l'allègement demandé.
func TestNativeH700Layout(t *testing.T) {
	u := &ui{fb: &framebuffer{w: 640, h: 480}}
	if r := u.rows(); r != 22 {
		t.Fatalf("attendu 22 lignes de texte en 640x480, obtenu %d", r)
	}
}

// Le défilement s'arrête aux deux bouts (pas de bouclage dans un texte).
func TestScrollStopsAtBothEnds(t *testing.T) {
	u := &ui{fb: &framebuffer{w: 640, h: 480}}
	u.lines = make([]string, 30)
	held := map[string]bool{}
	u.apply(keyEvent{action: "up", pressed: true}, held)
	if u.offset != 0 {
		t.Fatalf("HAUT depuis le début : offset=%d", u.offset)
	}
	for i := 0; i < 50; i++ {
		u.apply(keyEvent{action: "down", pressed: true}, held)
	}
	if want := len(u.lines) - u.rows(); u.offset != want {
		t.Fatalf("BAS jusqu'en bas : offset=%d, attendu %d", u.offset, want)
	}
	u.apply(keyEvent{action: "right", pressed: true}, held)
	if want := len(u.lines) - u.rows(); u.offset != want {
		t.Fatalf("page suivante en fin de texte : offset=%d, attendu %d", u.offset, want)
	}
	u.apply(keyEvent{action: "left", pressed: true}, held)
	if u.offset != 0 {
		t.Fatalf("page précédente : offset=%d", u.offset)
	}
}

// Un texte plus court qu'un écran ne défile pas du tout.
func TestShortTextDoesNotScroll(t *testing.T) {
	u := &ui{fb: &framebuffer{w: 640, h: 480}}
	u.lines = []string{"une", "deux", "trois"}
	held := map[string]bool{}
	u.apply(keyEvent{action: "down", pressed: true}, held)
	if u.offset != 0 {
		t.Fatalf("offset=%d sur un texte court", u.offset)
	}
}

func TestKeyHoldRepeatAndRelease(t *testing.T) {
	now := time.Unix(100, 0)
	r := newKeyRepeater()
	r.update(keyEvent{action: "down", pressed: true}, now)
	if got := r.due(now.Add(200 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("early repeat: %v", got)
	}
	if got := r.due(now.Add(320 * time.Millisecond)); len(got) != 1 || got[0] != "down" {
		t.Fatalf("first repeat: %v", got)
	}
	if got := r.due(now.Add(350 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("double repeat: %v", got)
	}
	if got := r.due(now.Add(420 * time.Millisecond)); len(got) != 1 {
		t.Fatalf("second repeat: %v", got)
	}
	r.update(keyEvent{action: "down", pressed: false}, now.Add(430*time.Millisecond))
	if got := r.due(now.Add(3000 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("repeat after release: %v", got)
	}
}

func TestHatDirectionsAndRelease(t *testing.T) {
	previous, events := hatEvents(17, 1, "")
	if previous != "down" || len(events) != 1 || !events[0].pressed {
		t.Fatalf("hat down: %s %v", previous, events)
	}
	previous, events = hatEvents(17, 0, previous)
	if previous != "" || len(events) != 1 || events[0].pressed || events[0].action != "down" {
		t.Fatalf("hat release: %s %v", previous, events)
	}
	previous, events = hatEvents(17, -1, previous)
	if previous != "up" || len(events) != 1 || !events[0].pressed {
		t.Fatalf("hat up: %s %v", previous, events)
	}
}

// Le rendu hors console (--preview) sert de garde-fou de mise en page : le texte
// doit commencer à la marge gauche (~42 px), pas collé au bord ni centré.
func TestPreviewRendersLeftAlignedText(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "journal.txt")
	if err := os.WriteFile(file, []byte("BONJOUR\nDEUXIEME LIGNE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "preview.png")
	if err := renderPreview(options{file: file, title: "Journal", preview: out}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	minX, minY := img.Bounds().Max.X, img.Bounds().Max.Y
	rows := map[int]bool{}
	for y := 0; y < img.Bounds().Max.Y; y++ {
		for x := 0; x < img.Bounds().Max.X; x++ {
			c := img.(*image.RGBA)
			i := c.PixOffset(x, y)
			if c.Pix[i] > 200 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				rows[y] = true
			}
		}
	}
	// Titre et corps commencent tous les deux à la marge gauche (24 px).
	if minX < 18 || minX > 40 {
		t.Fatalf("premier pixel à x=%d (attendu ~24)", minX)
	}
	// En-tête haut et discret : rien au-dessus de y=6, rien de volumineux.
	if minY < 4 || minY > 20 {
		t.Fatalf("premier pixel à y=%d (attendu ~6-12)", minY)
	}
	if len(rows) < 12 {
		t.Fatalf("trop peu de lignes de texte : %d", len(rows))
	}
	// Plus aucune pilule de rappel de bouton : le bas de l'écran reste noir.
	c := img.(*image.RGBA)
	for y := 440; y < img.Bounds().Max.Y; y++ {
		for x := 0; x < img.Bounds().Max.X; x++ {
			if c.Pix[c.PixOffset(x, y)] > 200 {
				t.Fatalf("pixel clair en bas d'écran (%d,%d) : rappels de boutons de retour ?", x, y)
			}
		}
	}
}
