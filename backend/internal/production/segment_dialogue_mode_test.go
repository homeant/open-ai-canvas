package production

import (
	"strings"
	"testing"
)

func TestSegmentPromptStructuredMonologue(t *testing.T) {
	in := SegmentPromptInput{Index: 0, Count: 2, Shots: []SegmentShot{{Speaker: "陈启", Dialogue: "这次不能五五分。", DialogueMode: "inner_monologue", DurationMs: 2000}, {Dialogue: "不能播放的背景句", DialogueMode: "silent", DurationMs: 1000}}}
	prompt := SegmentVideoPrompt(in)
	for _, text := range []string{"内心独白", "不张嘴", "不对口型", "这次不能五五分。", "本镜静音", "接续基准"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("missing %s: %s", text, prompt)
		}
	}
	if strings.Contains(prompt, "不能播放的背景句") {
		t.Fatal("silent dialogue emitted as speech")
	}
	writer := SegmentWriterPrompt(in, prompt, "", nil)
	if !strings.Contains(writer, "不能插入回忆替代独白") {
		t.Fatal("writer lacks monologue rule")
	}
}
