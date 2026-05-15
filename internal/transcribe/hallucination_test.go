package transcribe

import (
	"testing"
)

// TestIsHallucination_PerPhrase confirms each documented stock phrase is
// caught, with case- and punctuation-insensitivity verified by varying the
// surface form per phrase. Threshold is the spec's 0.5 (well above 0.1).
func TestIsHallucination_PerPhrase(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"thank_you_lower", "thank you"},
		{"thank_you_period", "Thank you."},
		{"thank_you_upper", "THANK YOU"},
		{"thank_you_excl", "Thank you!"},
		{"thank_you_for_watching", "Thank you for watching"},
		{"thank_you_for_watching_period", "Thank you for watching."},
		{"thank_you_very_much", "Thank you very much."},
		{"thank_you_so_much", "Thank you so much"},
		{"thanks_for_watching", "Thanks for watching."},
		{"please_subscribe", "Please subscribe."},
		{"like_and_subscribe", "Like and subscribe!"},
		{"subtitles_by", "Subtitles by"},
		{"subtitles_by_amara", "Subtitles by the Amara.org community."},
		{"you", "You"},
		{"you_with_punct", "You."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isHallucination(tc.text, 0.5, true) {
				t.Errorf("isHallucination(%q, 0.5, true) = false; want true", tc.text)
			}
		})
	}
}

// TestIsHallucination_BelowThreshold confirms that for any of the stock
// phrases, a low no_speech_prob (well below 0.1) means we trust the model
// and pass the text through.
func TestIsHallucination_BelowThreshold(t *testing.T) {
	phrases := []string{
		"thank you",
		"thank you for watching",
		"thank you very much",
		"thank you so much",
		"thanks for watching",
		"please subscribe",
		"like and subscribe",
		"subtitles by",
		"subtitles by the amara.org community",
		"you",
	}
	for _, p := range phrases {
		t.Run(p, func(t *testing.T) {
			if isHallucination(p, 0.05, true) {
				t.Errorf("isHallucination(%q, 0.05, true) = true; want false (below threshold)", p)
			}
		})
	}
}

// TestIsHallucination_NonMatchingText confirms that text not on the list is
// passed through even with a high no_speech_prob.
func TestIsHallucination_NonMatchingText(t *testing.T) {
	if isHallucination("thank you for the demo", 0.9, true) {
		t.Errorf("isHallucination(%q, 0.9, true) = true; want false (not on list)",
			"thank you for the demo")
	}
}

// TestIsHallucination_NoSegments confirms that when the provider didn't
// send no_speech_prob (hasNoSpeechProb=false), we don't filter even if the
// text matches — the filter requires metadata to fire.
func TestIsHallucination_NoSegments(t *testing.T) {
	if isHallucination("thank you", 0, false) {
		t.Errorf("isHallucination(%q, 0, false) = true; want false (no metadata)", "thank you")
	}
}

// TestIsHallucination_ExactThreshold confirms the boundary condition: 0.1
// is at the threshold and should filter (>=, not >).
func TestIsHallucination_ExactThreshold(t *testing.T) {
	if !isHallucination("thank you", 0.1, true) {
		t.Errorf("isHallucination(%q, 0.1, true) = false; want true (>= threshold)", "thank you")
	}
}

// TestNormalizeForHallucinationMatch confirms the normalization helper does
// what the rest of the filter relies on. Internal punctuation must be
// preserved (only surrounding whitespace/punctuation is stripped).
func TestNormalizeForHallucinationMatch(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Thank you.", "thank you"},
		{"  THANK YOU  ", "thank you"},
		{"Subtitles by the Amara.org community", "subtitles by the amara.org community"},
		{"Thank, you", "thank, you"}, // internal comma kept
		{"!!You!!", "you"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalizeForHallucinationMatch(tc.in)
			if got != tc.want {
				t.Errorf("normalize(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
