package production

import (
	"strings"
	"unicode"
)

func NormalizeSpeechText(text string) string {
	var out strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out.WriteRune(unicode.ToLower(r))
		}
	}
	return out.String()
}

type AudioWord struct {
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
	Text    string `json:"text"`
}

type AudioPhrase struct {
	StartMs int64       `json:"startMs"`
	EndMs   int64       `json:"endMs"`
	Text    string      `json:"text"`
	Words   []AudioWord `json:"words,omitempty"`
}

type AudioAlignment struct {
	ExpectedText        string        `json:"expectedText"`
	RecognizedText      string        `json:"recognizedText"`
	TextMatches         bool          `json:"textMatches"`
	TimingValid         bool          `json:"timingValid"`
	Status              string        `json:"status"`
	WordAlignmentStatus string        `json:"wordAlignmentStatus"`
	Phrases             []AudioPhrase `json:"phrases"`
}

// Timestamps are measured on the trimmed excerpt, then projected into the segment timeline.
// Missing word timestamps stay unavailable; phrase boundaries must never be presented as word alignment.
func AlignAudioText(expected string, phrases []AudioPhrase, startMs, endMs int64) AudioAlignment {
	result := AudioAlignment{ExpectedText: expected, Status: "unknown", TimingValid: true, WordAlignmentStatus: "unavailable", Phrases: []AudioPhrase{}}
	var text strings.Builder
	lastEnd := int64(0)
	allWords := len(phrases) > 0
	invalidWords := false
	for _, phrase := range phrases {
		phrase.Words = append([]AudioWord(nil), phrase.Words...)
		if phrase.StartMs < lastEnd || phrase.EndMs <= phrase.StartMs || phrase.EndMs > endMs-startMs {
			result.TimingValid = false
		}
		lastEnd = phrase.EndMs
		text.WriteString(phrase.Text)
		wordsText := ""
		wordEnd := phrase.StartMs
		for i := range phrase.Words {
			word := &phrase.Words[i]
			if word.StartMs < wordEnd || word.EndMs <= word.StartMs || word.StartMs < phrase.StartMs || word.EndMs > phrase.EndMs {
				result.TimingValid = false
				invalidWords = true
			}
			wordEnd = word.EndMs
			wordsText += word.Text
			word.StartMs += startMs
			word.EndMs += startMs
		}
		if len(phrase.Words) == 0 || NormalizeSpeechText(wordsText) != NormalizeSpeechText(phrase.Text) {
			allWords = false
			if len(phrase.Words) > 0 {
				invalidWords = true
			}
		}
		phrase.StartMs += startMs
		phrase.EndMs += startMs
		result.Phrases = append(result.Phrases, phrase)
	}
	result.RecognizedText = text.String()
	result.TextMatches = NormalizeSpeechText(expected) != "" && NormalizeSpeechText(expected) == NormalizeSpeechText(result.RecognizedText)
	if allWords && result.TimingValid {
		result.WordAlignmentStatus = "measured"
	}
	if invalidWords {
		result.WordAlignmentStatus = "invalid"
	}
	if len(phrases) > 0 {
		result.Status = "mismatch"
		if result.TextMatches && result.TimingValid && !invalidWords {
			result.Status = "matched"
		}
	}
	return result
}
