package handler

import "testing"

func TestParseExtractedVoice(t *testing.T) {
	t.Parallel()
	got, err := parseExtractedVoice(`{"persona":"你是靠谱的同事，先把事实说清楚。","reply_tone":"短句、不客套。"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Persona != "你是靠谱的同事，先把事实说清楚。" || got.ReplyTone != "短句、不客套。" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseExtractedVoiceStripsFenceAndRejectsEmpty(t *testing.T) {
	t.Parallel()
	got, err := parseExtractedVoice("```json\n{\"persona\":\"A calm coordinator.\",\"reply_tone\":\"Direct.\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if got.Persona != "A calm coordinator." || got.ReplyTone != "Direct." {
		t.Fatalf("got %+v", got)
	}
	if _, err := parseExtractedVoice(`{"persona":"","reply_tone":""}`); err == nil {
		t.Fatal("expected empty voice to fail")
	}
}
