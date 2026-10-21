package pngopt

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// spec describes a synthetic PNG built by buildPNG.
type spec struct {
	w, h         int
	ctype, depth byte
	interlace    bool
	plte, trns   []byte
	extra        []chunk
	gen          func(x, y int, s []uint16)
}

func buildPNG(sp spec) []byte {
	rng := rand.New(rand.NewPCG(1, uint64(sp.w*31+sp.h)))
	hd := header{w: sp.w, h: sp.h, depth: sp.depth, ctype: sp.ctype}
	ch := hd.channels()
	passes := adam7
	if !sp.interlace {
		passes = []adamPass{{0, 0, 1, 1}}
	}
	var raw []byte
	s := make([]uint16, ch)
	for _, ps := range passes {
		pw, ph := passSize(hd, ps)
		if pw == 0 || ph == 0 {
			continue
		}
		rb := hd.rowBytes(pw)
		prev := make([]byte, rb)
		for r := range ph {
			cur := make([]byte, rb)
			y := ps.yo + r*ps.ys
			for i := range pw {
				x := ps.xo + i*ps.xs
				sp.gen(x, y, s)
				for c := range ch {
					bit := (i*ch + c) * int(sp.depth)
					switch sp.depth {
					case 16:
						binary.BigEndian.PutUint16(cur[bit/8:], s[c])
					case 8:
						cur[bit/8] = byte(s[c])
					default:
						cur[bit/8] |= byte(s[c]) << (8 - int(sp.depth) - bit%8)
					}
				}
			}
			ft := rng.IntN(5)
			dst := make([]byte, rb)
			applyFilter(ft, dst, cur, prev, hd.filterBPP())
			raw = append(raw, byte(ft))
			raw = append(raw, dst...)
			prev = cur
		}
	}
	var zb bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&zb, 5)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	z := zb.Bytes()

	var out []byte
	out = append(out, pngSig...)
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:], uint32(sp.w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(sp.h))
	ihdr[8], ihdr[9] = sp.depth, sp.ctype
	if sp.interlace {
		ihdr[12] = 1
	}
	out = appendChunk(out, "IHDR", ihdr[:])
	for _, c := range sp.extra {
		out = appendChunk(out, c.typ, c.data)
	}
	if sp.plte != nil {
		out = appendChunk(out, "PLTE", sp.plte)
	}
	if sp.trns != nil {
		out = appendChunk(out, "tRNS", sp.trns)
	}
	out = appendChunk(out, "tEXt", []byte("Comment\x00hello world, this is a text chunk"))
	half := len(z) / 2
	out = appendChunk(out, "IDAT", z[:half])
	out = appendChunk(out, "IDAT", z[half:])
	return appendChunk(out, "IEND", nil)
}

func decodeStd(t testing.TB, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	return img
}

func requireSamePixels(t testing.TB, want, got []byte) {
	t.Helper()
	a, b := decodeStd(t, want), decodeStd(t, got)
	require.Equal(t, a.Bounds(), b.Bounds())
	bd := a.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			ca := color.NRGBA64Model.Convert(a.At(x, y))
			cb := color.NRGBA64Model.Convert(b.At(x, y))
			if ca != cb {
				t.Fatalf("pixel (%d,%d) differs: %v vs %v", x, y, ca, cb)
			}
		}
	}
}

func ffmpegPixels(t testing.TB, data []byte, format string) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", "pipe:0", "-f", "rawvideo", "-pix_fmt", format, "pipe:1")
	cmd.Stdin = bytes.NewReader(data)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	require.NoError(t, cmd.Run(), errb.String())
	px := out.Bytes()
	bpp := 4
	if format == "rgba64be" {
		bpp = 8
	}
	for i := 0; i+bpp <= len(px); i += bpp {
		transparent := px[i+bpp-1] == 0 && (bpp == 4 || px[i+bpp-2] == 0)
		if transparent {
			clear(px[i : i+bpp])
		}
	}
	return px
}

// requireSameFFmpeg compares the decodes of ffmpeg. 16-bit output is
// compared at 16 bits, everything else at 8 bits, because swscale rounds
// 8-to-16-bit conversions differently depending on the source pixel format.
func requireSameFFmpeg(t testing.TB, want, got []byte) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return
	}
	format := "rgba"
	pi, ierr := parse(want)
	pg, gerr := parse(got)
	if ierr == nil && gerr == nil && pi.hdr.depth == 16 {
		if pg.hdr.depth != 16 {
			// swscale dithers 16-bit to 8-bit conversions, so there is no exact reference.
			return
		}
		format = "rgba64be"
	}
	require.True(t, bytes.Equal(ffmpegPixels(t, want, format), ffmpegPixels(t, got, format)), "ffmpeg decodes differ")
}

func noise(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed*7+1)) }

func nrgbaToPNG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	require.NoError(t, enc.Encode(&b, img))
	return b.Bytes()
}

// readCorpus returns the PNG bodies recorded in the HAR corpus.
func readCorpus() (map[string][]byte, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "corpus", "corpus.har"))
	if err != nil {
		return nil, err
	}
	var har struct {
		Log struct {
			Entries []struct {
				Request  struct{ URL string }
				Response struct {
					Content struct {
						MimeType, Text, Encoding string
					}
				}
			}
		}
	}
	if err := json.Unmarshal(data, &har); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range har.Log.Entries {
		c := e.Response.Content
		if !strings.Contains(c.MimeType, "image/png") || c.Text == "" || c.Encoding != "base64" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(c.Text)
		if err != nil {
			continue
		}
		name := e.Request.URL[strings.LastIndex(e.Request.URL, "/")+1:]
		out[name] = b
	}
	return out, nil
}

func corpusPNGs(t testing.TB) map[string][]byte {
	t.Helper()
	out, err := readCorpus()
	if err != nil {
		t.Skip("corpus not available")
	}
	return out
}

func kodakPNGs(t testing.TB) map[string][]byte {
	t.Helper()
	files, _ := filepath.Glob("/tmp/opencode/img/kodim*.png")
	out := map[string][]byte{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err == nil {
			out[filepath.Base(f)] = b
		}
	}
	if len(out) == 0 {
		t.Skip("kodim images not available")
	}
	return out
}

func optipngSize(t testing.TB, data []byte, args ...string) (int, bool) {
	t.Helper()
	if _, err := exec.LookPath("optipng"); err != nil {
		return 0, false
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	outp := filepath.Join(dir, "out.png")
	require.NoError(t, os.WriteFile(in, data, 0o600))
	a := append([]string{"-quiet", "-out", outp}, args...)
	a = append(a, in)
	if err := exec.Command("optipng", a...).Run(); err != nil {
		return 0, false
	}
	st, err := os.Stat(outp)
	require.NoError(t, err)
	return int(st.Size()), true
}
