// Package transcribe — hallucination filter.
//
// Whisper-large-v3 hallucinates a small set of common short phrases on
// silence or background noise (transcribing nothing as "thank you",
// "subtitles by the amara.org community", etc. — these come from the model's
// YouTube subtitle training data). When the provider reports a high
// no_speech_prob on the first segment AND the transcript text matches one of
// these known stock phrases, we replace the transcript with the empty string
// so the rest of the pipeline treats the recording as silent.
//
// The phrase list and threshold (0.1) are ported verbatim from the upstream reference's
// TranscriptionService.isHallucination. Both were tuned against ~500 samples
// of real and empty audio; the threshold is intentionally conservative to
// minimize filtering real user speech.
package transcribe

import (
	"strings"
	"unicode"
)

// hallucinationPhrases lists the exact (lowercased, punctuation-stripped)
// transcript strings that the filter recognizes as model hallucinations.
// Order doesn't matter; lookup is by exact equality after normalization.
var hallucinationPhrases = map[string]struct{}{
	"thank you":                            {},
	"thank you for watching":               {},
	"thank you very much":                  {},
	"thank you so much":                    {},
	"thanks for watching":                  {},
	"please subscribe":                     {},
	"like and subscribe":                   {},
	"subtitles by":                         {},
	"subtitles by the amara.org community": {},
	"you":                                  {},
}

// hallucinationNoSpeechThreshold is the minimum first-segment no_speech_prob
// (Whisper's own confidence that the audio was silent) at which the filter
// will drop a matching transcript. Below this threshold the model is fairly
// confident there was speech, so we trust the text even when it happens to
// equal a stock phrase.
const hallucinationNoSpeechThreshold = 0.1

// isHallucination returns true when text should be filtered out as a known
// Whisper stock-phrase hallucination on silence. It mirrors the upstream reference's
// isHallucination(text:json:) exactly:
//
//  1. Lowercase the text and strip surrounding whitespace and punctuation.
//  2. If the normalized form isn't in hallucinationPhrases, return false.
//  3. If hasNoSpeechProb is false (the provider didn't include segments or
//     no_speech_prob in the response), DO NOT filter — without metadata we
//     can't safely tell speech from silence, so we pass the text through.
//  4. Otherwise filter when noSpeechProb >= hallucinationNoSpeechThreshold.
//
// hasNoSpeechProb is the explicit "did the response actually carry this
// number?" signal; passing 0.0 with hasNoSpeechProb=true is a real Whisper
// reading and means "high confidence there was speech" (so we pass through),
// whereas passing 0.0 with hasNoSpeechProb=false means "the field was
// missing" (also pass through, but for a different reason).
func isHallucination(text string, noSpeechProb float64, hasNoSpeechProb bool) bool {
	normalized := normalizeForHallucinationMatch(text)
	if _, ok := hallucinationPhrases[normalized]; !ok {
		return false
	}
	if !hasNoSpeechProb {
		return false
	}
	return noSpeechProb >= hallucinationNoSpeechThreshold
}

// normalizeForHallucinationMatch lowercases s and trims surrounding
// whitespace and punctuation characters. It deliberately does NOT remove
// internal punctuation — "thank you." normalizes to "thank you", but
// "thank, you" stays "thank, you" and won't match. This matches the Swift
// behavior of trimming a CharacterSet that contains only punctuation and
// whitespace.
func normalizeForHallucinationMatch(s string) string {
	lowered := strings.ToLower(s)
	return strings.TrimFunc(lowered, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	})
}
