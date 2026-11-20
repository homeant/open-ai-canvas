package production

import (
	"encoding/binary"
	"testing"
)

func TestAudioSignalDoesNotTurnEnergyIntoSpeech(t *testing.T) {
	pcm := make([]byte, 44+32000)
	copy(pcm, "RIFF")
	binary.LittleEndian.PutUint32(pcm[4:], uint32(len(pcm)-8))
	copy(pcm[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(pcm[16:], 16)
	binary.LittleEndian.PutUint16(pcm[20:], 1)
	binary.LittleEndian.PutUint16(pcm[22:], 1)
	binary.LittleEndian.PutUint32(pcm[24:], 16000)
	binary.LittleEndian.PutUint16(pcm[34:], 16)
	copy(pcm[36:], "data")
	binary.LittleEndian.PutUint32(pcm[40:], 32000)
	silent, err := MeasurePCM16Wav(pcm)
	if err != nil || !silent.DigitalSilence || silent.SpeechPresence != "absent" {
		t.Fatalf("silence %+v %v", silent, err)
	}
	for i := 44; i < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], 1000)
	}
	energy, err := MeasurePCM16Wav(pcm)
	if err != nil || energy.LowEnergy || energy.SpeechPresence != "unverified" {
		t.Fatalf("energy implies voice %+v %v", energy, err)
	}
	for i := 44; i < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], 1)
	}
	low, err := MeasurePCM16Wav(pcm)
	if err != nil || !low.LowEnergy || low.DigitalSilence || low.SpeechPresence != "unverified" {
		t.Fatalf("low energy %+v %v", low, err)
	}
	if _, err := MeasurePCM16Wav(pcm[:20]); err == nil {
		t.Fatal("truncated WAV accepted")
	}
}
