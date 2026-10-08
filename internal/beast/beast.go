// Package beast implements the Beast binary format that dump1090 speaks on
// port 30005, just enough of it to filter a live stream:
//
//	frame   := 0x1a type data*
//	type    := '1' (Mode A/C, 9 data bytes) | '2' (short Mode S, 14)
//	         | '3' (long Mode S, 21) | '5' (Radarcape position, 21)
//	data    := any byte, except 0x1a which appears doubled ("escaping")
//	data    := mlat[6] signal[1] payload[2|7|14]   (types '1','2','3')
//	         | position block[21]                    (type '5')
//
// The 48-bit MLAT timestamp and signal byte are big-endian. A heartbeat is
// an all-zero Mode A/C frame — dump1090 writes exactly {0x1a,'1',0×9} —
// and is distinguished from a real frame by having every byte zero.
// Clients send commands as the three bytes {0x1a,'1',cmd}.
//
// Frames are read with their original wire bytes attached so a filtered
// stream can be forwarded verbatim without re-encoding.
package beast

import (
	"bufio"
	"fmt"
	"io"
)

// Esc is the Beast escape byte that starts every frame.
const Esc = 0x1a

// Frame type bytes.
const (
	TypeModeAC = '1'
	TypeShort  = '2'
	TypeLong   = '3'
	TypeRadar  = '5'
)

// dataLen is the number of data bytes following the type byte for each
// frame type. Type '5' carries 21 bytes with no MLAT/signal split (the
// Radarcape position message reads its 21 bytes straight after the type).
var dataLen = map[byte]int{
	TypeModeAC: 9,
	TypeShort:  14,
	TypeLong:   21,
	TypeRadar:  21,
}

// Frame is one decoded Beast frame.
type Frame struct {
	// Raw is the frame exactly as it appeared on the wire, escape
	// sequences included, ready for verbatim forwarding.
	Raw []byte
	// Type is the frame type byte.
	Type byte
	// MLAT is the 48-bit big-endian MLAT timestamp (types '1','2','3').
	MLAT uint64
	// Signal is the raw signal level byte (types '1','2','3').
	Signal byte
	// Payload is the unescaped message content: 2 bytes for Mode A/C,
	// 7 for short Mode S, 14 for long Mode S, 21 for a Radarcape position.
	Payload []byte
}

// IsHeartbeat reports whether the frame is the all-zero Mode A/C heartbeat
// dump1090 sends to keep idle connections alive. Heartbeats carry no
// aircraft data and are always forwarded.
func (f Frame) IsHeartbeat() bool {
	if f.Type != TypeModeAC || f.MLAT != 0 || f.Signal != 0 {
		return false
	}
	for _, b := range f.Payload {
		if b != 0 {
			return false
		}
	}
	return true
}

// IsModeAC reports whether the frame is a real Mode A/C message (no ICAO
// address, no position — dropped by the fence).
func (f Frame) IsModeAC() bool { return f.Type == TypeModeAC && !f.IsHeartbeat() }

// Reader decodes a Beast stream in the server-to-client direction:
// data frames and heartbeats. It never mutates its input; each Frame keeps
// the raw bytes that produced it.
type Reader struct {
	br *bufio.Reader
}

// NewReader wraps r in a Beast frame reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReader(r)}
}

// Next returns the next frame. The stream is expected to contain only
// well-formed frames, so any byte where a frame should start, or any
// escape not followed by a duplicate, is an error. io.EOF signals a clean
// end of stream.
func (rd *Reader) Next() (Frame, error) {
	start, err := rd.br.ReadByte()
	if err != nil {
		return Frame{}, err
	}
	if start != Esc {
		return Frame{}, fmt.Errorf("beast: expected frame start 0x1a, got 0x%02x", start)
	}
	typ, err := rd.br.ReadByte()
	if err != nil {
		if err == io.EOF {
			return Frame{}, io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	n, ok := dataLen[typ]
	if !ok {
		return Frame{}, fmt.Errorf("beast: unknown frame type 0x%02x", typ)
	}

	raw := make([]byte, 0, 2+2*n)
	raw = append(raw, Esc, typ)
	data := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		b, err := rd.br.ReadByte()
		if err != nil {
			if err == io.EOF {
				return Frame{}, io.ErrUnexpectedEOF
			}
			return Frame{}, err
		}
		raw = append(raw, b)
		if b == Esc {
			nb, err := rd.br.ReadByte()
			if err != nil {
				if err == io.EOF {
					return Frame{}, io.ErrUnexpectedEOF
				}
				return Frame{}, err
			}
			raw = append(raw, nb)
			if nb != Esc {
				return Frame{}, fmt.Errorf("beast: escape 0x1a not followed by 0x1a (got 0x%02x)", nb)
			}
		}
		data = append(data, b)
	}

	f := Frame{Raw: raw, Type: typ}
	switch typ {
	case TypeModeAC, TypeShort, TypeLong:
		for _, b := range data[:6] {
			f.MLAT = f.MLAT<<8 | uint64(b)
		}
		f.Signal = data[6]
		f.Payload = data[7:]
	case TypeRadar:
		f.Payload = data
	}
	return f, nil
}

// CommandReader decodes the client-to-server direction of a Beast
// connection: three-byte {0x1a,'1',cmd} command frames, as sent by
// dump1090 clients (e.g. 'V' verbatim on, 'J' Mode A/C on).
type CommandReader struct {
	br *bufio.Reader
}

// NewCommandReader wraps r for command parsing.
func NewCommandReader(r io.Reader) *CommandReader {
	return &CommandReader{br: bufio.NewReader(r)}
}

// Next returns the next command byte. Everything else on the wire (a data
// frame, stray bytes) is rejected so misbehaving clients cannot desync us
// silently.
func (cr *CommandReader) Next() (byte, error) {
	start, err := cr.br.ReadByte()
	if err != nil {
		return 0, err
	}
	if start != Esc {
		return 0, fmt.Errorf("beast: expected command start 0x1a, got 0x%02x", start)
	}
	typ, err := cr.br.ReadByte()
	if err != nil {
		if err == io.EOF {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	if typ != TypeModeAC {
		return 0, fmt.Errorf("beast: expected command frame type '1', got 0x%02x", typ)
	}
	cmd, err := cr.br.ReadByte()
	if err != nil {
		if err == io.EOF {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	return cmd, nil
}

// IsCommand reports whether raw is a complete {0x1a,'1',cmd} command.
func IsCommand(raw []byte) bool {
	return len(raw) == 3 && raw[0] == Esc && raw[1] == TypeModeAC
}
