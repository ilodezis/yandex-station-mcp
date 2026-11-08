package station

import (
	"bytes"
	"encoding/binary"
	"math"
)

// The speaker's own "wav" files are mp3 inside, and aplay plays them as white noise, so the
// service makes its own signals: plain sine beeps as a real PCM wav, sent inline with the
// command and decoded on the speaker. Loudness is set by the Master volume, the level here
// stays moderate on purpose.
const (
	soundRate  = 16000 // ALSA (dmix_spk_plug) converts the rate
	soundLevel = 0.35  // share of full scale
	soundFade  = 0.01  // seconds of fade in and out, no clicks
)

type tone struct {
	hz  float64 // 0 is a pause
	sec float64
}

// Sounds is the allowlist for play_sound: short name -> sequence of beeps.
var Sounds = map[string][]tone{
	"timer":   {{880, 0.18}, {0, 0.1}, {880, 0.18}, {0, 0.1}, {880, 0.18}},
	"success": {{660, 0.12}, {880, 0.22}},
	"notice":  {{740, 0.25}},
	"error":   {{440, 0.2}, {330, 0.3}},
}

// wavBytes renders tones as a 16 kHz mono 16-bit PCM wav.
func wavBytes(tones []tone) []byte {
	var pcm []int16
	for _, t := range tones {
		n := int(t.sec * soundRate)
		for i := 0; i < n; i++ {
			if t.hz == 0 {
				pcm = append(pcm, 0)
				continue
			}
			at := float64(i) / soundRate
			env := 1.0
			if at < soundFade {
				env = at / soundFade
			} else if left := t.sec - at; left < soundFade {
				env = left / soundFade
			}
			v := math.Sin(2*math.Pi*t.hz*at) * soundLevel * env
			pcm = append(pcm, int16(v*32767))
		}
	}
	var b bytes.Buffer
	dataLen := uint32(len(pcm) * 2)
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, 36+dataLen)
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	_ = binary.Write(&b, binary.LittleEndian, uint32(soundRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(soundRate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, dataLen)
	_ = binary.Write(&b, binary.LittleEndian, pcm)
	return b.Bytes()
}

// soundWav is the sound as wav bytes for aplay on stdin.
func soundWav(name string) []byte { return wavBytes(Sounds[name]) }
