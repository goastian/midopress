// Command fetch-images downloads the image corpus used to benchmark the image
// engine: `midopress bench <dir>`.
//
// The images are not part of the repository. Each one comes from a public
// source and keeps its own license; the Wikimedia Commons ones link to their
// file page, where the author and the license are stated. They are downloaded
// as the web serves them (server-made JPEG thumbnails, for example), which is
// what a proxy sees.
//
// Usage: go run ./tools/fetch-images [-dir testdata/images] [-delay 300ms]
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type item struct {
	file    string // name to save as, with the right extension
	url     string
	license string
}

func commons(file string, width int, saveAs string) item {
	u := "https://commons.wikimedia.org/wiki/Special:FilePath/" + url.PathEscape(file)
	if width > 0 {
		u += fmt.Sprintf("?width=%d", width)
	}
	return item{
		file:    saveAs,
		url:     u,
		license: "https://commons.wikimedia.org/wiki/File:" + url.PathEscape(file),
	}
}

func manifest() []item {
	var m []item

	// Photographs as a web server delivers them: JPEG thumbnails from Commons.
	for _, p := range []struct{ file, as string }{
		{"Hopetoun_falls.jpg", "photo-hopetoun-falls"},
		{"Flower_jtca001.jpg", "photo-flower"},
		{"Sunflower_from_Silesia2.jpg", "photo-sunflower"},
		{"Jupiter_by_Cassini-Huygens.jpg", "photo-jupiter"},
		{"The_Earth_seen_from_Apollo_17.jpg", "photo-earth"},
		{"Mona_Lisa,_by_Leonardo_da_Vinci,_from_C2RMF_retouched.jpg", "photo-mona-lisa"},
		{"Tour_Eiffel_Wikimedia_Commons.jpg", "photo-eiffel"},
		{"Golden_Gate_Bridge_as_seen_from_Battery_East.jpg", "photo-golden-gate"},
		{"Lion_waiting_in_Namibia.jpg", "photo-lion"},
		{"Great_Wave_off_Kanagawa2.jpg", "photo-great-wave"},
		{"Crab_Nebula.jpg", "photo-crab-nebula"},
		{"Hubble_ultra_deep_field.jpg", "photo-hubble"},
		{"Matterhorn_from_Domhütte_-_2.jpg", "photo-matterhorn"},
		{"Mount_Everest_as_seen_from_Drukair2_PLW_edit.jpg", "photo-everest"},
		{"Eurasian_Wigeon.jpg", "photo-wigeon"},
		{"Milky_Way_Night_Sky_Black_Rock_Desert_Nevada.jpg", "photo-milky-way"},
		{"Common_Kingfisher.jpg", "photo-kingfisher"},
		{"Monarch_In_May.jpg", "photo-monarch"},
		{"Colosseum_in_Rome,_Italy_-_April_2007.jpg", "photo-colosseum"},
		{"Taj_Mahal_in_March_2004.jpg", "photo-taj-mahal"},
	} {
		m = append(m, commons(p.file, 1200, p.as+".jpg"))
	}

	// Graphics, with and without transparency.
	m = append(m,
		commons("PNG_transparency_demonstration_1.png", 0, "graphic-alpha-demo.png"),
		commons("Wikipedia-logo-v2.svg", 500, "graphic-logo.png"),
		commons("Gnome-edit-clear.svg", 256, "graphic-icon.png"),
		commons("Ruby_on_Rails_logo.svg", 600, "graphic-rails-logo.png"),
		commons("Gnome-applications-science.svg", 512, "graphic-gnome-science.png"),
		commons("Python-logo-notext.svg", 400, "graphic-python-logo.png"),
	)

	// Animations.
	m = append(m,
		commons("Rotating_earth_(large).gif", 0, "anim-rotating-earth.gif"),
		commons("Spinning_globe.gif", 0, "anim-spinning-globe.gif"),
	)

	// Google's WebP test gallery: real photographs as JPEG and PNG.
	for i := 1; i <= 5; i++ {
		m = append(m, item{
			file:    fmt.Sprintf("gallery-%d.jpg", i),
			url:     fmt.Sprintf("https://www.gstatic.com/webp/gallery/%d.jpg", i),
			license: "https://developers.google.com/speed/webp/gallery1",
		})
	}
	for i := 1; i <= 5; i++ {
		m = append(m, item{
			file:    fmt.Sprintf("gallery3-%d.png", i),
			url:     fmt.Sprintf("https://www.gstatic.com/webp/gallery3/%d.png", i),
			license: "https://developers.google.com/speed/webp/gallery2",
		})
	}

	// The Kodak lossless true colour image suite, which Kodak released for
	// unrestricted use. Photographic PNG, the usual reference for codecs.
	for i := 1; i <= 24; i++ {
		m = append(m, item{
			file:    fmt.Sprintf("kodak-%02d.png", i),
			url:     fmt.Sprintf("https://r0k.us/graphics/kodak/kodak/kodim%02d.png", i),
			license: "https://r0k.us/graphics/kodak/ (released by Kodak for unrestricted use)",
		})
	}
	return m
}

const maxSize = 8 << 20

func main() {
	dir := flag.String("dir", "testdata/images", "directory to save the corpus in")
	delay := flag.Duration("delay", 300*time.Millisecond, "pause between downloads, to be kind to the servers")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fatal(err)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	var sources strings.Builder
	sources.WriteString("# Image corpus sources\n\nEach image keeps the license stated at its source.\n\n")

	ok, skipped, failed := 0, 0, 0
	for _, it := range manifest() {
		path := filepath.Join(*dir, it.file)
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			skipped++
			fmt.Fprintf(&sources, "- %s: %s\n", it.file, it.license)
			continue
		}
		n, err := download(client, it.url, path)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "skip %-34s %v\n", it.file, err)
			continue
		}
		ok++
		fmt.Fprintf(os.Stderr, "ok   %-34s %8d bytes\n", it.file, n)
		fmt.Fprintf(&sources, "- %s: %s\n", it.file, it.license)
		time.Sleep(*delay)
	}
	if err := os.WriteFile(filepath.Join(*dir, "SOURCES.md"), []byte(sources.String()), 0o644); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "%d downloaded, %d already there, %d failed, in %s\n", ok, skipped, failed, *dir)
}

func download(c *http.Client, u, path string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "midopress-corpus/1.0 (https://github.com/goastian/midopress; benchmark images)")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return 0, fmt.Errorf("not an image: %s", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return 0, err
	}
	if len(body) > maxSize {
		return 0, fmt.Errorf("larger than %d bytes", maxSize)
	}
	return len(body), os.WriteFile(path, body, 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fetch-images:", err)
	os.Exit(1)
}
