// Package imgmeta reads and strips image metadata (EXIF, ICC, XMP, PNG
// ancillary chunks) without decoding any pixel. It is designed for a hot path
// that processes untrusted input: every function is bounded by the input size,
// never panics and allocates as little as possible.
package imgmeta

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var (
	errNotJPEG   = errors.New("imgmeta: not a JPEG")
	errTruncated = errors.New("imgmeta: truncated data")
	errMarker    = errors.New("imgmeta: invalid JPEG marker")
	errNoSOF     = errors.New("imgmeta: no frame header before scan")
	errBadSOF    = errors.New("imgmeta: invalid frame header")
)

const (
	markerSOI = 0xD8
	markerEOI = 0xD9
	markerSOS = 0xDA
	markerCOM = 0xFE
	markerAPP = 0xE0
)

var (
	jfifPrefix  = []byte("JFIF\x00")
	iccPrefix   = []byte("ICC_PROFILE\x00")
	xmpPrefix   = []byte("http://ns.adobe.com/xap/1.0/\x00")
	xmpExtPrefx = []byte("http://ns.adobe.com/xmp/extension/\x00")
	psPrefix    = []byte("Photoshop 3.0\x00")
	adobePrefix = []byte("Adobe")
)

// JPEG describes the header-level properties of a JPEG file.
type JPEG struct {
	Width, Height int
	// Components is the number of color components declared by the frame
	// header (1, 3 or 4 for any well-formed file).
	Components  int
	Progressive bool
	// Orientation is the EXIF orientation 1..8, 1 when absent or invalid.
	Orientation int
	// ICC is the reassembled ICC profile from the APP2 chunks, nil when absent
	// or when the chunk sequence is inconsistent.
	ICC                                       []byte
	HasEXIF, HasXMP, HasPhotoshop, HasComment bool
	// Adobe reports an APP14 "Adobe" marker.
	Adobe bool
	// AdobeTransform is the color transform flag of the Adobe marker, -1 when
	// there is no Adobe marker.
	AdobeTransform int
	// MetadataBytes is the number of bytes StripJPEG removes with the default
	// options.
	MetadataBytes int
}

// StripOptions selects what StripJPEG preserves.
type StripOptions struct {
	// KeepICC preserves the APP2 ICC_PROFILE segments.
	KeepICC bool
	// KeepOrientation preserves the orientation by writing a minimal APP1 EXIF
	// segment holding only tag 0x0112 when the orientation is not 1.
	KeepOrientation bool
	// KeepJFIF preserves the JFIF APP0 segment.
	KeepJFIF bool
}

// DefaultStripOptions returns options that keep everything that can influence
// how the picture is displayed.
func DefaultStripOptions() StripOptions {
	return StripOptions{KeepICC: true, KeepOrientation: true, KeepJFIF: true}
}

type segment struct {
	marker     byte
	start, end int
	data       []byte
}

// nextSegment reads the marker segment starting at pos. start is the offset of
// the 0xFF byte immediately preceding the marker code, so fill bytes are not
// part of the segment.
func nextSegment(raw []byte, pos int) (segment, error) {
	if pos >= len(raw) {
		return segment{}, errTruncated
	}
	if raw[pos] != 0xFF {
		return segment{}, errMarker
	}
	for pos < len(raw) && raw[pos] == 0xFF {
		pos++
	}
	if pos >= len(raw) {
		return segment{}, errTruncated
	}
	m := raw[pos]
	if m == 0 {
		return segment{}, errMarker
	}
	seg := segment{marker: m, start: pos - 1}
	if m == 0x01 || (m >= 0xD0 && m <= markerEOI) {
		seg.end = pos + 1
		return seg, nil
	}
	if pos+3 > len(raw) {
		return segment{}, errTruncated
	}
	l := int(binary.BigEndian.Uint16(raw[pos+1:]))
	if l < 2 {
		return segment{}, errMarker
	}
	if pos+1+l > len(raw) {
		return segment{}, errTruncated
	}
	seg.end = pos + 1 + l
	seg.data = raw[pos+3 : seg.end]
	return seg, nil
}

func isSOF(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC
}

type segAction int

const (
	actKeep segAction = iota
	actDrop
	actEXIF
)

func classify(seg *segment, opts StripOptions) segAction {
	m := seg.marker
	switch {
	case m == markerCOM:
		return actDrop
	case m < markerAPP || m > markerAPP+0x0F:
		return actKeep
	case m == markerAPP:
		if opts.KeepJFIF && bytes.HasPrefix(seg.data, jfifPrefix) {
			return actKeep
		}
	case m == markerAPP+1:
		if bytes.HasPrefix(seg.data, exifPrefix) {
			return actEXIF
		}
	case m == markerAPP+2:
		if opts.KeepICC && bytes.HasPrefix(seg.data, iccPrefix) {
			return actKeep
		}
	case m == markerAPP+14:
		if bytes.HasPrefix(seg.data, adobePrefix) {
			return actKeep
		}
	}
	return actDrop
}

type iccChunk struct {
	seq, count byte
	data       []byte
}

type scan struct {
	width, height, comps           int
	progressive, hasSOF            bool
	hasEXIF, hasXMP, hasPS, hasCOM bool
	adobe                          bool
	adobeTransform                 int
	orient                         int
	exifCount                      int
	exifStart, exifEnd             int
	dropped, kept                  int
	keepOff                        int
	sosStart                       int
	icc                            []iccChunk
}

// analyze walks the marker segments up to the first SOS.
func analyze(raw []byte, opts StripOptions, wantICC bool) (scan, error) {
	s := scan{adobeTransform: -1, orient: 1, keepOff: -1}
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != markerSOI {
		return s, errNotJPEG
	}
	pos := 2
	for {
		seg, err := nextSegment(raw, pos)
		if err != nil {
			return s, err
		}
		pos = seg.end
		size := seg.end - seg.start
		switch {
		case seg.marker == markerEOI:
			return s, errTruncated
		case seg.marker == markerSOS:
			if !s.hasSOF {
				return s, errNoSOF
			}
			s.sosStart = seg.start
			if s.exifCount == 1 && opts.KeepOrientation && s.orient > 1 {
				m := minimalEXIF(s.orient)
				if bytes.Equal(raw[s.exifStart:s.exifEnd], m[:]) {
					s.keepOff = s.exifStart
					s.dropped -= s.exifEnd - s.exifStart
					s.kept += s.exifEnd - s.exifStart
				}
			}
			return s, nil
		case isSOF(seg.marker):
			if !s.hasSOF {
				if err := s.parseSOF(&seg); err != nil {
					return s, err
				}
			}
		}
		s.noteMetadata(&seg, wantICC)
		switch classify(&seg, opts) {
		case actKeep:
			s.kept += size
		case actDrop:
			s.dropped += size
		case actEXIF:
			s.dropped += size
			if s.exifCount == 0 {
				s.exifStart, s.exifEnd = seg.start, seg.end
			}
			s.exifCount++
			if s.orient == 1 {
				s.orient = ParseOrientation(seg.data)
			}
		}
	}
}

func (s *scan) parseSOF(seg *segment) error {
	d := seg.data
	if len(d) < 6 {
		return errBadSOF
	}
	nf := int(d[5])
	if nf == 0 || len(d) < 6+3*nf {
		return errBadSOF
	}
	s.hasSOF = true
	s.height = int(binary.BigEndian.Uint16(d[1:]))
	s.width = int(binary.BigEndian.Uint16(d[3:]))
	s.comps = nf
	switch seg.marker {
	case 0xC2, 0xC6, 0xCA, 0xCE:
		s.progressive = true
	}
	return nil
}

func (s *scan) noteMetadata(seg *segment, wantICC bool) {
	d := seg.data
	switch seg.marker {
	case markerCOM:
		s.hasCOM = true
	case markerAPP + 1:
		switch {
		case bytes.HasPrefix(d, exifPrefix):
			s.hasEXIF = true
		case bytes.HasPrefix(d, xmpPrefix), bytes.HasPrefix(d, xmpExtPrefx):
			s.hasXMP = true
		}
	case markerAPP + 2:
		if wantICC && len(d) >= len(iccPrefix)+2 && bytes.HasPrefix(d, iccPrefix) {
			s.icc = append(s.icc, iccChunk{seq: d[12], count: d[13], data: d[14:]})
		}
	case markerAPP + 13:
		if bytes.HasPrefix(d, psPrefix) {
			s.hasPS = true
		}
	case markerAPP + 14:
		if len(d) >= 12 && bytes.HasPrefix(d, adobePrefix) && !s.adobe {
			s.adobe = true
			s.adobeTransform = int(d[11])
		}
	}
}

// replacesEXIF reports whether the stripped output carries a freshly written
// minimal EXIF segment.
func (s *scan) replacesEXIF(opts StripOptions) bool {
	return opts.KeepOrientation && s.orient > 1 && s.exifCount > 0 && s.keepOff < 0
}

// ParseJPEG reads the marker segments of a JPEG up to the first scan. It
// returns an error for data that is not a JPEG or whose headers are truncated.
// Arithmetic-coded, lossless and extended variants are reported, not rejected.
func ParseJPEG(raw []byte) (*JPEG, error) {
	opts := DefaultStripOptions()
	s, err := analyze(raw, opts, true)
	if err != nil {
		return nil, err
	}
	meta := s.dropped
	if s.replacesEXIF(opts) {
		meta -= minimalEXIFSize
	}
	if meta < 0 {
		meta = 0
	}
	return &JPEG{
		Width:          s.width,
		Height:         s.height,
		Components:     s.comps,
		Progressive:    s.progressive,
		Orientation:    s.orient,
		ICC:            assembleICC(s.icc),
		HasEXIF:        s.hasEXIF,
		HasXMP:         s.hasXMP,
		HasPhotoshop:   s.hasPS,
		HasComment:     s.hasCOM,
		Adobe:          s.adobe,
		AdobeTransform: s.adobeTransform,
		MetadataBytes:  meta,
	}, nil
}

// assembleICC joins ICC_PROFILE chunks. Any inconsistency (count mismatch,
// duplicate or missing sequence number) yields nil.
func assembleICC(chunks []iccChunk) []byte {
	n := len(chunks)
	if n == 0 || n > 255 {
		return nil
	}
	total := 0
	inOrder := true
	for i := range chunks {
		c := &chunks[i]
		if int(c.count) != n || c.seq < 1 || int(c.seq) > n {
			return nil
		}
		if int(c.seq) != i+1 {
			inOrder = false
		}
		total += len(c.data)
	}
	out := make([]byte, 0, total)
	if inOrder {
		for i := range chunks {
			out = append(out, chunks[i].data...)
		}
		return out
	}
	byseq := make([][]byte, n)
	var seen [256]bool
	for i := range chunks {
		c := &chunks[i]
		if seen[c.seq] {
			return nil
		}
		seen[c.seq] = true
		byseq[c.seq-1] = c.data
	}
	for _, d := range byseq {
		out = append(out, d...)
	}
	return out
}

// StripJPEG removes EXIF, XMP, Photoshop, comments, thumbnails and other APPn
// segments at the marker level. Entropy-coded data and every segment needed to
// decode are copied untouched, so the decoded pixels are bit-identical. The
// input slice itself is returned (no copy) when there is nothing to remove.
func StripJPEG(raw []byte, opts StripOptions) ([]byte, error) {
	s, err := analyze(raw, opts, false)
	if err != nil {
		return nil, err
	}
	if s.dropped == 0 {
		return raw, nil
	}
	insert := s.replacesEXIF(opts)
	size := 2 + s.kept + len(raw) - s.sosStart
	if insert {
		size += minimalEXIFSize
	}
	out := make([]byte, 0, size)
	out = append(out, 0xFF, markerSOI)
	pending := insert
	pos := 2
	for {
		seg, err := nextSegment(raw, pos)
		if err != nil {
			return nil, err
		}
		if seg.marker == markerSOS {
			break
		}
		pos = seg.end
		act := classify(&seg, opts)
		if act == actEXIF && seg.start == s.keepOff {
			act = actKeep
		}
		if act != actKeep {
			continue
		}
		isJFIF := seg.marker == markerAPP && bytes.HasPrefix(seg.data, jfifPrefix)
		if pending && !isJFIF {
			m := minimalEXIF(s.orient)
			out = append(out, m[:]...)
			pending = false
		}
		out = append(out, raw[seg.start:seg.end]...)
	}
	if pending {
		m := minimalEXIF(s.orient)
		out = append(out, m[:]...)
	}
	return append(out, raw[s.sosStart:]...), nil
}
