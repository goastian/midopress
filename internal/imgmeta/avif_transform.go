package imgmeta

import "encoding/binary"

// AVIFTransform is an ordered primary-item presentation property.
type AVIFTransform struct {
	Kind     string
	Value    uint8
	Aperture [8]int64
}

// primaryAVIFProperties resolves properties in the primary item's association order.
func primaryAVIFProperties(raw []byte) ([]isoBox, bool, error) {
	var primary uint32
	var sawPrimary bool
	var properties []isoBox
	var associations []byte
	for _, top := range isoBoxes(raw) {
		if top.typ != "meta" || len(top.body) < 4 {
			continue
		}
		for _, box := range isoBoxes(top.body[4:]) {
			if box.typ == "pitm" {
				if sawPrimary {
					return nil, false, errBadAVIF
				}
				sawPrimary = true
				if len(box.body) >= 6 && box.body[0] == 0 {
					primary = uint32(binary.BigEndian.Uint16(box.body[4:]))
				} else if len(box.body) >= 8 && box.body[0] == 1 {
					primary = binary.BigEndian.Uint32(box.body[4:])
				}
			}
			if box.typ == "iprp" {
				for _, p := range isoBoxes(box.body) {
					if p.typ == "ipco" {
						if properties != nil {
							return nil, false, errBadAVIF
						}
						properties = isoBoxes(p.body)
					}
					if p.typ == "ipma" {
						if associations != nil {
							return nil, false, errBadAVIF
						}
						associations = p.body
					}
				}
			}
		}
	}
	if !sawPrimary && associations == nil {
		return nil, false, nil
	}
	if primary == 0 || len(associations) < 8 {
		return nil, false, errBadAVIF
	}
	version, large := associations[0], associations[3]&1 != 0
	if version > 1 {
		return nil, false, errBadAVIF
	}
	count := binary.BigEndian.Uint32(associations[4:])
	b := associations[8:]
	var out []isoBox
	found := false
	for i := uint32(0); i < count; i++ {
		idBytes := 2
		if version == 1 {
			idBytes = 4
		}
		if len(b) < idBytes+1 {
			return nil, false, errBadAVIF
		}
		id := uint32(binary.BigEndian.Uint16(b))
		if idBytes == 4 {
			id = binary.BigEndian.Uint32(b)
		}
		n := int(b[idBytes])
		b = b[idBytes+1:]
		if id == primary {
			if found {
				return nil, false, errBadAVIF
			}
			found = true
		}
		for j := 0; j < n; j++ {
			if len(b) < 1 {
				return nil, false, errBadAVIF
			}
			index := int(b[0] & 0x7f)
			b = b[1:]
			if large {
				if len(b) < 1 {
					return nil, false, errBadAVIF
				}
				index = index<<8 | int(b[0])
				b = b[1:]
			}
			if id != primary || index == 0 {
				continue
			}
			if index > len(properties) {
				return nil, false, errBadAVIF
			}
			out = append(out, properties[index-1])
		}
	}
	if !found || len(b) != 0 {
		return nil, false, errBadAVIF
	}
	return out, true, nil
}

// AVIFPresentation reads presentation properties in primary-item association
// order. It rejects containers whose transformations cannot be associated.
func AVIFPresentation(raw []byte) ([]AVIFTransform, error) {
	props, associated, err := primaryAVIFProperties(raw)
	if err != nil {
		return nil, err
	}
	if !associated {
		meta, err := ParseAVIF(raw)
		if err != nil || meta.Transformed {
			return nil, errBadAVIF
		}
		return nil, nil
	}
	var out []AVIFTransform
	seen := make(map[string]bool)
	for _, p := range props {
		t := AVIFTransform{Kind: p.typ}
		switch p.typ {
		case "irot":
			if len(p.body) != 1 || p.body[0] > 3 {
				return nil, errBadAVIF
			}
			t.Value = p.body[0]
		case "imir":
			if len(p.body) != 1 || p.body[0] > 1 {
				return nil, errBadAVIF
			}
			t.Value = p.body[0]
		case "clap":
			if len(p.body) != 32 {
				return nil, errBadAVIF
			}
			for k := 0; k < 8; k++ {
				t.Aperture[k] = int64(binary.BigEndian.Uint32(p.body[k*4:]))
			}
			t.Aperture[4] = int64(int32(t.Aperture[4]))
			t.Aperture[6] = int64(int32(t.Aperture[6]))
		default:
			continue
		}
		if seen[p.typ] {
			return nil, errBadAVIF
		}
		seen[p.typ] = true
		out = append(out, t)
	}
	return out, nil
}
