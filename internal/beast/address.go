package beast

import "fmt"

// crc24 computes the Mode S parity checksum: the standard CRC-24 with
// generator x^24+x^23+...+x^12+x^10+x^3+1 (0x1FFF409), initial value 0,
// processed most-significant-bit first. No final XOR — the remainder is
// used directly, either compared against the parity bytes or XORed with
// them to recover the address.
func crc24(data []byte) uint32 {
	const poly = 0x1FFF409
	var reg uint32
	for _, b := range data {
		reg ^= uint32(b) << 16
		for i := 0; i < 8; i++ {
			if reg&0x800000 != 0 {
				reg = (reg << 1) ^ poly
			} else {
				reg <<= 1
			}
			reg &= 0xFFFFFF
		}
	}
	return reg
}

// ExtractICAO returns the ICAO address of a Mode S payload (7 or 14
// unescaped bytes) in lowercase hex.
//
//   - DF 11/17/18 carry the address in bytes 1–3 directly.
//   - DF 0/4/5/16/20/21 have parity (AP) in the last three bytes instead:
//     address = CRC(first len-24 bits) XOR AP.
//   - Every other format (19, 24, Mode A/C, ...) has no address we can
//     trust, so ok is false and the message must be dropped.
func ExtractICAO(payload []byte) (string, bool) {
	if len(payload) != 7 && len(payload) != 14 {
		return "", false
	}
	df := payload[0] >> 3 // downlink format: top five bits
	switch df {
	case 11, 17, 18:
		return fmt.Sprintf("%02x%02x%02x", payload[1], payload[2], payload[3]), true
	case 0, 4, 5, 16, 20, 21:
		n := len(payload)
		ap := uint32(payload[n-3])<<16 | uint32(payload[n-2])<<8 | uint32(payload[n-1])
		addr := crc24(payload[:n-3]) ^ ap
		return fmt.Sprintf("%06x", addr), true
	default:
		return "", false
	}
}
