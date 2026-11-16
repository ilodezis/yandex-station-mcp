package station

import (
	"fmt"
	"sort"
	"strings"
)

// Voices are the TTS voices the speaker's local synthesis accepts through a
// <speaker voice="..."> tag. An unknown name is not an error on the speaker: the
// phrase is silently dropped, so the list is an allowlist.
var Voices = map[string]bool{
	"alyss":  true, // Alice's own voice
	"jane":   true,
	"oksana": true,
	"omazh":  true,
	"zahar":  true,
	"ermil":  true,
}

// Effects are the TTS effects accepted through <speaker effect="...">.
var Effects = map[string]bool{
	"megaphone": true,
	"hamster":   true,
}

// VoiceNames lists the allowed voices, sorted.
func VoiceNames() []string { return sortedKeys(Voices) }

// EffectNames lists the allowed effects, sorted.
func EffectNames() []string { return sortedKeys(Effects) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Styled prefixes text with a speaker tag that picks the voice and effect. Empty
// voice and effect return text unchanged (Alice's default voice). Names are
// case-insensitive and must be in Voices and Effects.
func Styled(text, voice, effect string) (string, error) {
	voice = strings.ToLower(strings.TrimSpace(voice))
	effect = strings.ToLower(strings.TrimSpace(effect))
	if voice == "" && effect == "" {
		return text, nil
	}
	var attrs []string
	if voice != "" {
		if !Voices[voice] {
			return "", fmt.Errorf("unknown voice %q, use one of: %s", voice, strings.Join(VoiceNames(), ", "))
		}
		attrs = append(attrs, `voice="`+voice+`"`)
	}
	if effect != "" {
		if !Effects[effect] {
			return "", fmt.Errorf("unknown effect %q, use one of: %s", effect, strings.Join(EffectNames(), ", "))
		}
		attrs = append(attrs, `effect="`+effect+`"`)
	}
	return "<speaker " + strings.Join(attrs, " ") + ">" + text, nil
}
