package production

import "testing"

func TestAudioAlignmentKeepsUnknownWordsAndRejectsBadTiming(t *testing.T) {
	phrase := AudioPhrase{StartMs: 100, EndMs: 500, Text: "这次，我不退。"}
	result := AlignAudioText("这次我不退", []AudioPhrase{phrase}, 6100, 7600)
	if result.Status != "matched" || result.WordAlignmentStatus != "unavailable" || result.Phrases[0].StartMs != 6200 {
		t.Fatalf("phrase alignment %+v", result)
	}
	for _, bad := range []AudioPhrase{{StartMs: -1, EndMs: 500, Text: phrase.Text}, {StartMs: 0, EndMs: 1600, Text: phrase.Text}, {StartMs: 500, EndMs: 400, Text: phrase.Text}, {StartMs: 0, EndMs: 500, Text: "这次我退了"}} {
		if got := AlignAudioText("这次我不退", []AudioPhrase{bad}, 6100, 7600); got.Status == "matched" {
			t.Fatalf("bad alignment accepted %+v", got)
		}
	}
	if got := AlignAudioText("这次我不退", nil, 0, 1000); got.Status != "unknown" {
		t.Fatal("empty ASR implies acceptance")
	}
}

func TestAudioAlignmentWordProjectionDoesNotMutateEvidence(t *testing.T) {
	phrases := []AudioPhrase{{StartMs: 0, EndMs: 1000, Text: "你好", Words: []AudioWord{{StartMs: 0, EndMs: 400, Text: "你"}, {StartMs: 400, EndMs: 1000, Text: "好"}}}}
	got := AlignAudioText("你好。", phrases, 5000, 6500)
	if got.Status != "matched" || got.WordAlignmentStatus != "measured" || got.Phrases[0].Words[0].StartMs != 5000 || phrases[0].Words[0].StartMs != 0 {
		t.Fatalf("word projection %+v", got)
	}
	phrases[0].Words[1].EndMs = 1200
	if got := AlignAudioText("你好", phrases, 0, 1500); got.Status == "matched" || got.WordAlignmentStatus == "measured" {
		t.Fatal("invalid word timestamps accepted")
	}
}
