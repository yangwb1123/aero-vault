package thumbnail

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

const pngIHDRPrefixLen = 33

var pngSignature = [...]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// pngIHDRDimensions returns the raw unsigned dimensions only when prefix is a
// complete, valid PNG signature plus IHDR chunk. Keeping this check independent
// of image/png avoids its native-int pixel-count overflow on 32-bit systems.
func pngIHDRDimensions(prefix []byte) (uint32, uint32, bool) {
	if len(prefix) < pngIHDRPrefixLen || !bytes.Equal(prefix[:8], pngSignature[:]) {
		return 0, 0, false
	}
	if binary.BigEndian.Uint32(prefix[8:12]) != 13 ||
		!bytes.Equal(prefix[12:16], []byte("IHDR")) {
		return 0, 0, false
	}
	if !validPNGHeaderFields(prefix[16:29]) ||
		crc32.Checksum(prefix[12:29], crc32.IEEETable) != binary.BigEndian.Uint32(prefix[29:33]) {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(prefix[16:20]), binary.BigEndian.Uint32(prefix[20:24]), true
}

func validPNGHeaderFields(data []byte) bool {
	if len(data) != 13 || binary.BigEndian.Uint32(data[:4]) == 0 || binary.BigEndian.Uint32(data[4:8]) == 0 {
		return false
	}
	depth, colorType := data[8], data[9]
	if data[10] != 0 || data[11] != 0 || (data[12] != 0 && data[12] != 1) {
		return false
	}
	switch depth {
	case 1, 2, 4:
		return colorType == 0 || colorType == 3
	case 8:
		return colorType == 0 || colorType == 2 || colorType == 3 || colorType == 4 || colorType == 6
	case 16:
		return colorType == 0 || colorType == 2 || colorType == 4 || colorType == 6
	default:
		return false
	}
}
