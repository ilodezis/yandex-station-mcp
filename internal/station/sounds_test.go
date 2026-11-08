package station

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The vendor "wav" files were mp3 inside and played as white noise. Every signal must be a
// real PCM wav, short, and quiet enough in level.
func TestSoundsAreValidQuietPCM(t *testing.T) {
	full := 32767.0
	maxPeak := int(0.4 * full)
	for name := range Sounds {
		raw := soundWav(name)
		if len(raw) < 44 || !bytes.HasPrefix(raw, []byte("RIFF")) || string(raw[8:16]) != "WAVEfmt " {
			t.Fatalf("%s: not a RIFF/WAVE file", name)
		}
		if binary.LittleEndian.Uint16(raw[20:]) != 1 || binary.LittleEndian.Uint16(raw[22:]) != 1 ||
			binary.LittleEndian.Uint32(raw[24:]) != soundRate || binary.LittleEndian.Uint16(raw[34:]) != 16 {
			t.Fatalf("%s: want PCM mono %d Hz 16-bit", name, soundRate)
		}
		dataLen := int(binary.LittleEndian.Uint32(raw[40:]))
		if dataLen != len(raw)-44 {
			t.Fatalf("%s: data length %d, body %d", name, dataLen, len(raw)-44)
		}
		if secs := float64(dataLen/2) / soundRate; secs > 1.2 || secs < 0.1 {
			t.Errorf("%s: %.2f s long", name, secs)
		}
		var peak int
		for i := 44; i+1 < len(raw); i += 2 {
			v := int(int16(binary.LittleEndian.Uint16(raw[i:])))
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
		if peak == 0 || peak > maxPeak {
			t.Errorf("%s: peak %d, want 1..%d", name, peak, maxPeak)
		}
		// first and last sample start and end near zero: no clicks
		if first := int(int16(binary.LittleEndian.Uint16(raw[44:]))); first > 200 || first < -200 {
			t.Errorf("%s: starts at %d", name, first)
		}
	}
}
