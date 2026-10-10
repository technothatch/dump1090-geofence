package beast

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// wireFrame builds a Beast frame: type byte plus data, escaping 0x1a in
// the data exactly as dump1090's writeBeastMessage does.
func wireFrame(typ byte, data []byte) []byte {
	out := []byte{Esc, typ}
	for _, b := range data {
		out = append(out, b)
		if b == Esc {
			out = append(out, Esc)
		}
	}
	return out
}

// mlatFrame builds the data section for types '1'/'2'/'3': 6-byte
// big-endian MLAT, signal byte, payload.
func mlatFrame(typ byte, mlat uint64, signal byte, payload []byte) []byte {
	data := make([]byte, 0, 7+len(payload))
	data = binary.BigEndian.AppendUint64(data, mlat) // truncated to 6 bytes below
	data = data[2:]
	data = append(data, signal)
	data = append(data, payload...)
	return wireFrame(typ, data)
}

func TestReaderDecodesModeSFrame(t *testing.T) {
	payload := []byte{0x8D, 0x48, 0x40, 0xD6, 0x20, 0x2C, 0xC3, 0x71, 0xC3, 0x2C, 0xE0, 0x57, 0x60, 0x98}
	raw := mlatFrame(TypeLong, 0x123456789ABC, 0x80, payload)

	rd := NewReader(bytes.NewReader(raw))
	f, err := rd.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if f.Type != TypeLong {
		t.Errorf("Type = %q, want '3'", f.Type)
	}
	if f.MLAT != 0x123456789ABC {
		t.Errorf("MLAT = %#x, want 0x123456789abc", f.MLAT)
	}
	if f.Signal != 0x80 {
		t.Errorf("Signal = %#x", f.Signal)
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Errorf("Payload = %x, want %x", f.Payload, payload)
	}
	if !bytes.Equal(f.Raw, raw) {
		t.Errorf("Raw = %x, want %x", f.Raw, raw)
	}
	if _, err := rd.Next(); err != io.EOF {
		t.Errorf("second Next = %v, want io.EOF", err)
	}
}

func TestReaderUnescapesData(t *testing.T) {
	// MLAT timestamp and payload both contain 0x1a and must survive
	// escaping:0x1a 0x1a on the wire is one 0x1a in the frame.
	payload := []byte{0x1a, 0x00, 0x1a, 0x1a, 0x03, 0x04, 0x05}
	raw := mlatFrame(TypeShort, 0x1A001A1A1A1A, 0x1a, payload)

	rd := NewReader(bytes.NewReader(raw))
	f, err := rd.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if f.MLAT != 0x1A001A1A1A1A {
		t.Errorf("MLAT = %#x", f.MLAT)
	}
	if f.Signal != 0x1a {
		t.Errorf("Signal = %#x", f.Signal)
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Errorf("Payload = %x, want %x", f.Payload, payload)
	}
	// Wire form is longer than the decoded form because of the escapes.
	if len(f.Raw) <= len(f.Payload)+7 {
		t.Errorf("Raw (%d bytes) does not contain escape sequences", len(f.Raw))
	}
}

func TestReaderHeartbeat(t *testing.T) {
	raw := []byte{Esc, '1', 0, 0, 0, 0, 0, 0, 0, 0, 0}
	rd := NewReader(bytes.NewReader(raw))
	f, err := rd.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !f.IsHeartbeat() {
		t.Error("all-zero Mode A/C frame not detected as heartbeat")
	}
	if f.IsModeAC() {
		t.Error("heartbeat classified as Mode A/C data")
	}

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "short frame", raw: mlatFrame(TypeShort, 0, 0, []byte{0x8D, 0, 0, 0, 0, 0, 0})},
		{name: "nonzero MLAT", raw: mlatFrame(TypeModeAC, 1, 0, []byte{0, 0})},
		{name: "nonzero signal", raw: mlatFrame(TypeModeAC, 0, 0x80, []byte{0, 0})},
		{name: "nonzero payload", raw: mlatFrame(TypeModeAC, 0, 0, []byte{0x0F, 0xFF})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := NewReader(bytes.NewReader(tt.raw)).Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if f.IsHeartbeat() {
				t.Error("frame wrongly detected as heartbeat")
			}
		})
	}
}

func TestReaderModeACData(t *testing.T) {
	f, err := NewReader(bytes.NewReader(mlatFrame(TypeModeAC, 7, 0x10, []byte{0x0F, 0xFF}))).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !f.IsModeAC() {
		t.Error("real Mode A/C frame not detected")
	}
}

func TestReaderRadarcapePosition(t *testing.T) {
	data := make([]byte, 21)
	data[3] = 0x42 // some content
	raw := wireFrame(TypeRadar, data)
	f, err := NewReader(bytes.NewReader(raw)).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if f.Type != TypeRadar {
		t.Errorf("Type = %q", f.Type)
	}
	if len(f.Payload) != 21 {
		t.Errorf("Payload len = %d, want 21", len(f.Payload))
	}
	if f.MLAT != 0 || f.Signal != 0 {
		t.Error("Radarcape frame must not invent MLAT/signal")
	}
	if !bytes.Equal(f.Raw, raw) {
		t.Error("Raw mismatch")
	}
}

func TestReaderErrors(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		wantSub string
		wantEOF bool
	}{
		{
			name:    "not a frame start",
			raw:     []byte{0x41, '3', 0, 0},
			wantSub: "expected frame start 0x1a",
		},
		{
			name:    "unknown type",
			raw:     []byte{Esc, '9', 0},
			wantSub: "unknown frame type 0x39",
		},
		{
			name:    "type is escape",
			raw:     []byte{Esc, Esc, 0},
			wantSub: "unknown frame type 0x1a",
		},
		{
			name:    "escape without duplicate",
			raw:     append([]byte{Esc, '3'}, 0x1a, 0x41),
			wantSub: "not followed by 0x1a",
		},
		{
			name:    "truncated frame",
			raw:     append([]byte{Esc, '2'}, make([]byte, 13)...),
			wantSub: "unexpected EOF",
			wantEOF: true,
		},
		{
			name:    "truncated after type",
			raw:     []byte{Esc, '3'},
			wantSub: "unexpected EOF",
			wantEOF: true,
		},
		{
			name:    "truncated escape pair",
			raw:     append([]byte{Esc, '3'}, append(make([]byte, 18), Esc)...),
			wantSub: "unexpected EOF",
			wantEOF: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewReader(bytes.NewReader(tt.raw)).Next()
			if err == nil {
				t.Fatalf("Next succeeded, want error containing %q", tt.wantSub)
			}
			if tt.wantEOF && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("error = %v, want io.ErrUnexpectedEOF", err)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestReaderEmptyStream(t *testing.T) {
	if _, err := NewReader(bytes.NewReader(nil)).Next(); err != io.EOF {
		t.Errorf("Next on empty stream = %v, want io.EOF", err)
	}
}

func TestCommandReader(t *testing.T) {
	// dump1090 sends settings as back-to-back {0x1a,'1',cmd} triples.
	raw := []byte{Esc, '1', 'V', Esc, '1', 'j'}
	cr := NewCommandReader(bytes.NewReader(raw))
	for _, want := range []byte{'V', 'j'} {
		got, err := cr.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got != want {
			t.Errorf("command = %q, want %q", got, want)
		}
	}
	if _, err := cr.Next(); err != io.EOF {
		t.Errorf("third Next = %v, want io.EOF", err)
	}
}

func TestCommandReaderErrors(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		wantSub string
	}{
		{name: "data frame instead of command", raw: []byte{Esc, '3', 1}, wantSub: "expected command frame type"},
		{name: "no escape", raw: []byte{'V'}, wantSub: "expected command start"},
		{name: "truncated after type", raw: []byte{Esc, '1'}, wantSub: "unexpected EOF"},
		{name: "truncated after escape", raw: []byte{Esc}, wantSub: "unexpected EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCommandReader(bytes.NewReader(tt.raw)).Next()
			if err == nil {
				t.Fatalf("Next succeeded, want error containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestIsCommand(t *testing.T) {
	tests := []struct {
		raw  []byte
		want bool
	}{
		{raw: []byte{Esc, '1', 'V'}, want: true},
		{raw: []byte{Esc, '1', 'V', 0}, want: false},
		{raw: []byte{Esc, '3', 'V'}, want: false},
		{raw: []byte{Esc, '1'}, want: false},
	}
	for _, tt := range tests {
		if got := IsCommand(tt.raw); got != tt.want {
			t.Errorf("IsCommand(%x) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

// The DF17 parity vector: bytes 0..10 are the message, bytes 11..13 the
// parity. A valid message must satisfy CRC(message) == parity, and
// appending the parity must yield an overall checksum of zero — which is
// also what pins down the generator polynomial.
func TestCRC24Vector(t *testing.T) {
	const vector = "8D4840D6202CC371C32CE0576098"
	msg, err := hex.DecodeString(vector)
	if err != nil {
		t.Fatal(err)
	}
	ap := uint32(msg[11])<<16 | uint32(msg[12])<<8 | uint32(msg[13])
	if got := crc24(msg[:11]); got != ap {
		t.Errorf("crc24(message) = %#06x, want parity %#06x", got, ap)
	}
	if got := crc24(msg); got != 0 {
		t.Errorf("crc24(full message) = %#06x, want 0", got)
	}
	// Same vector through the public extractor (DF17: address in clear).
	icao, ok := ExtractICAO(msg)
	if !ok || icao != "4840d6" {
		t.Errorf("ExtractICAO = %q, %v; want 4840d6, true", icao, ok)
	}
}

func TestExtractICAODirect(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "DF17 ADS-B identity",
			payload: "8D40621D58C382D690C8AC2863A7",
			want:    "40621d",
		},
		{
			name:    "DF11 all-call reply",
			payload: "5D4840D6202CC3",
			want:    "4840d6",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := hex.DecodeString(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := ExtractICAO(p)
			if !ok || got != tt.want {
				t.Errorf("ExtractICAO = %q, %v; want %q, true", got, ok, tt.want)
			}
		})
	}
}

func TestExtractICAOAddressRecovery(t *testing.T) {
	const want = 0xABC123
	for _, df := range []byte{0, 4, 5, 16, 20, 21} {
		t.Run(string('A'+rune(df)), func(t *testing.T) {
			p := make([]byte, 7)
			p[0] = df << 3
			p[1], p[2], p[3] = 0x11, 0x22, 0x33
			ap := crc24(p[:4]) ^ want
			p[4], p[5], p[6] = byte(ap>>16), byte(ap>>8), byte(ap)
			got, ok := ExtractICAO(p)
			if !ok {
				t.Fatalf("ExtractICAO not ok for DF%d", df)
			}
			if got != "abc123" {
				t.Errorf("DF%d address = %q, want abc123", df, got)
			}
		})
	}
}

func TestExtractICAOLongAddressRecovery(t *testing.T) {
	const want = 0xC0FFEE
	p := make([]byte, 14)
	p[0] = 4 << 3 // DF4, long payload variant
	ap := crc24(p[:11]) ^ want
	p[11], p[12], p[13] = byte(ap>>16), byte(ap>>8), byte(ap)
	got, ok := ExtractICAO(p)
	if !ok || got != "c0ffee" {
		t.Errorf("ExtractICAO = %q, %v; want c0ffee, true", got, ok)
	}
}

func TestExtractICAONoAddress(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "DF19 ACAS", payload: "98000000000000"},
		{name: "DF24 checkout", payload: "C000000000000000000000000000"},
		{name: "mode A/C is not Mode S", payload: "0FFF"},
		{name: "wrong length", payload: "8D4840D6202CC371C32CE05760988D"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := hex.DecodeString(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			if icao, ok := ExtractICAO(p); ok {
				t.Errorf("ExtractICAO = %q, want !ok", icao)
			}
		})
	}
}
