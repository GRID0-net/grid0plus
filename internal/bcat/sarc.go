package bcat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// SARC is Nintendo's archive format, and what a decrypted Splatfest pack turns
// out to be.
//
// Note the ".pack.zs" naming: the extension says zstd, and the packs in a real
// seed are NOT compressed — decrypting one yields a SARC archive directly, magic
// and all. The name is a leftover. Anything here that assumed a compression
// layer would find a corrupt stream on the first real pack.
//
// Format: https://zeldamods.org/wiki/SARC
//
//	SARC header  0x14   magic, header size, BOM, file size, data offset, version
//	SFAT header  0x0C   magic, header size, node count, hash key
//	SFAT nodes   0x10   name hash, name offset + flag, data start, data end
//	SFNT header  0x08   magic, header size, reserved
//	name table          NUL-terminated names, each padded to 4 bytes
//	file data           each aligned

const (
	sarcMagic = "SARC"
	sfatMagic = "SFAT"
	sfntMagic = "SFNT"

	sarcHeaderSize = 0x14
	sfatHeaderSize = 0x0C
	sfntHeaderSize = 0x08
	sfatNodeSize   = 0x10

	// sarcHashKey is the multiplier in SARC's filename hash. 0x65 is what
	// Nintendo uses and it is stored in the header, so it is read back rather
	// than assumed when parsing.
	sarcHashKey uint32 = 0x65

	sarcVersion uint16 = 0x0100
	sarcBOM     uint16 = 0xFEFF
)

// SARCFile is one member of an archive.
type SARCFile struct {
	Name string
	Data []byte
}

// sarcNameHash is the hash SFAT nodes are sorted and looked up by.
func sarcNameHash(name string, key uint32) uint32 {
	var h uint32
	for i := 0; i < len(name); i++ {
		h = h*key + uint32(name[i])
	}
	return h
}

// ParseSARC reads an archive.
func ParseSARC(raw []byte) ([]SARCFile, error) {
	if len(raw) < sarcHeaderSize+sfatHeaderSize {
		return nil, fmt.Errorf("bcat: %d bytes is too short for a SARC", len(raw))
	}
	if string(raw[0:4]) != sarcMagic {
		return nil, fmt.Errorf("bcat: not a SARC (magic %q)", raw[0:4])
	}
	// Little-endian only. The BOM permits big-endian archives, but the Switch
	// does not produce them, and silently mis-reading one would be worse than
	// refusing it.
	if bom := binary.LittleEndian.Uint16(raw[6:8]); bom != sarcBOM {
		return nil, fmt.Errorf("bcat: SARC byte-order mark is %#04x, want %#04x", bom, sarcBOM)
	}

	dataOffset := binary.LittleEndian.Uint32(raw[12:16])

	sfat := raw[sarcHeaderSize:]
	if string(sfat[0:4]) != sfatMagic {
		return nil, fmt.Errorf("bcat: SFAT magic is %q", sfat[0:4])
	}
	count := int(binary.LittleEndian.Uint16(sfat[6:8]))
	key := binary.LittleEndian.Uint32(sfat[8:12])

	nodesAt := sarcHeaderSize + sfatHeaderSize
	nodesEnd := nodesAt + count*sfatNodeSize
	if nodesEnd > len(raw) {
		return nil, fmt.Errorf("bcat: SARC claims %d files but is only %d bytes", count, len(raw))
	}

	sfnt := raw[nodesEnd:]
	if len(sfnt) < sfntHeaderSize || string(sfnt[0:4]) != sfntMagic {
		return nil, fmt.Errorf("bcat: SFNT magic missing")
	}
	nameTable := raw[nodesEnd+sfntHeaderSize:]

	out := make([]SARCFile, 0, count)
	for i := 0; i < count; i++ {
		node := raw[nodesAt+i*sfatNodeSize:]
		hash := binary.LittleEndian.Uint32(node[0:4])
		attrs := binary.LittleEndian.Uint32(node[4:8])
		start := binary.LittleEndian.Uint32(node[8:12])
		end := binary.LittleEndian.Uint32(node[12:16])

		// The low 24 bits are the name's offset into the name table, in units
		// of 4 bytes. The top byte flags that a name is present at all.
		nameOff := int(attrs&0x00FFFFFF) * 4
		if nameOff >= len(nameTable) {
			return nil, fmt.Errorf("bcat: file %d has a name offset past the name table", i)
		}
		name := readCString(nameTable[nameOff:])

		if got := sarcNameHash(name, key); got != hash {
			return nil, fmt.Errorf("bcat: file %q hashes to %#08x but its node says %#08x", name, got, hash)
		}

		from, to := int(dataOffset)+int(start), int(dataOffset)+int(end)
		if to > len(raw) || from > to {
			return nil, fmt.Errorf("bcat: file %q has data outside the archive", name)
		}
		out = append(out, SARCFile{Name: name, Data: raw[from:to]})
	}
	return out, nil
}

func readCString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// BuildSARC writes an archive.
//
// Files are emitted in hash order, which is what SARC requires: the console
// binary-searches the node table by hash, so an archive in insertion order
// looks empty for most lookups rather than failing outright.
func BuildSARC(files []SARCFile) ([]byte, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("bcat: an empty archive")
	}
	if len(files) > 0xFFFF {
		return nil, fmt.Errorf("bcat: %d files, more than the node count can express", len(files))
	}

	sorted := make([]SARCFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		hi, hj := sarcNameHash(sorted[i].Name, sarcHashKey), sarcNameHash(sorted[j].Name, sarcHashKey)
		if hi == hj {
			// A hash collision would otherwise order by chance, and an archive
			// that serialises differently on each build is one whose revision
			// digest changes for no reason.
			return sorted[i].Name < sorted[j].Name
		}
		return hi < hj
	})
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Name == sorted[i-1].Name {
			return nil, fmt.Errorf("bcat: %q appears twice", sorted[i].Name)
		}
	}

	// Name table first, so node attributes can point into it.
	var names bytes.Buffer
	offsets := make([]uint32, len(sorted))
	for i, f := range sorted {
		if f.Name == "" {
			return nil, fmt.Errorf("bcat: a file with no name")
		}
		offsets[i] = uint32(names.Len() / 4)
		names.WriteString(f.Name)
		names.WriteByte(0)
		for names.Len()%4 != 0 {
			names.WriteByte(0)
		}
	}

	// The file data starts on an EIGHT-byte boundary, not four.
	//
	// Measured against three real packs: two happened to land on 8 already and
	// rebuilt byte-identically under a 4-byte assumption, while the third
	// (bh, whose name table ends at 948) needed the extra four. A 4-byte
	// assumption therefore looks correct on most archives and silently shifts
	// every file offset on the rest.
	dataOffset := align(sarcHeaderSize+sfatHeaderSize+len(sorted)*sfatNodeSize+sfntHeaderSize+names.Len(), 8)

	var data bytes.Buffer
	starts := make([]uint32, len(sorted))
	ends := make([]uint32, len(sorted))
	for i, f := range sorted {
		for data.Len()%4 != 0 {
			data.WriteByte(0)
		}
		starts[i] = uint32(data.Len())
		data.Write(f.Data)
		ends[i] = uint32(data.Len())
	}

	total := dataOffset + data.Len()
	out := make([]byte, 0, total)

	// SARC header.
	out = append(out, sarcMagic...)
	out = appendU16(out, sarcHeaderSize)
	out = appendU16(out, sarcBOM)
	out = appendU32(out, uint32(total))
	out = appendU32(out, uint32(dataOffset))
	out = appendU16(out, sarcVersion)
	out = appendU16(out, 0)

	// SFAT header and nodes.
	out = append(out, sfatMagic...)
	out = appendU16(out, sfatHeaderSize)
	out = appendU16(out, uint16(len(sorted)))
	out = appendU32(out, sarcHashKey)
	for i, f := range sorted {
		out = appendU32(out, sarcNameHash(f.Name, sarcHashKey))
		// Top byte set: this node has a name in the table.
		out = appendU32(out, 0x01000000|offsets[i])
		out = appendU32(out, starts[i])
		out = appendU32(out, ends[i])
	}

	// SFNT header and the name table.
	out = append(out, sfntMagic...)
	out = appendU16(out, sfntHeaderSize)
	out = appendU16(out, 0)
	out = append(out, names.Bytes()...)

	for len(out) < dataOffset {
		out = append(out, 0)
	}
	out = append(out, data.Bytes()...)
	return out, nil
}

func align(n, to int) int {
	if n%to == 0 {
		return n
	}
	return n + to - n%to
}

func appendU16(b []byte, v uint16) []byte {
	return binary.LittleEndian.AppendUint16(b, v)
}

func appendU32(b []byte, v uint32) []byte {
	return binary.LittleEndian.AppendUint32(b, v)
}
