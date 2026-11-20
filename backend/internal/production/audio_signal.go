package production

import (
	"encoding/binary"
	"errors"
	"math"
)

type AudioSignal struct {
	Method         string  `json:"method"`
	RMSDBFS        float64 `json:"rmsDbfs"`
	PeakDBFS       float64 `json:"peakDbfs"`
	DurationMs     int64   `json:"durationMs"`
	DigitalSilence bool    `json:"digitalSilence"`
	LowEnergy      bool    `json:"lowEnergy"`
	SpeechPresence string  `json:"speechPresence"`
}

// Energy can exclude digital silence; nonzero sound never proves speech or a speaker's identity.
func MeasurePCM16Wav(data []byte) (AudioSignal, error) {
	result := AudioSignal{Method: "pcm_energy_not_vad", SpeechPresence: "unverified", RMSDBFS: -120, PeakDBFS: -120}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return result, errors.New("expected PCM WAV")
	}
	var pcm []byte
	validFormat := false
	for offset := 12; offset+8 <= len(data); {
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		if size > len(data)-start {
			return result, errors.New("truncated WAV")
		}
		switch string(data[offset : offset+4]) {
		case "fmt ":
			validFormat = size >= 16 && binary.LittleEndian.Uint16(data[start:start+2]) == 1 && binary.LittleEndian.Uint16(data[start+2:start+4]) == 1 && binary.LittleEndian.Uint32(data[start+4:start+8]) == 16000 && binary.LittleEndian.Uint16(data[start+14:start+16]) == 16
		case "data":
			pcm = data[start : start+size]
		}
		offset = start + size + (size % 2)
	}
	if !validFormat || len(pcm) == 0 || len(pcm)%2 != 0 {
		return result, errors.New("expected nonempty mono 16k PCM16")
	}
	var squares, peak float64
	for i := 0; i < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:i+2]))) / 32768
		squares += v * v
		peak = math.Max(peak, math.Abs(v))
	}
	result.DurationMs = int64(len(pcm)/2) * 1000 / 16000
	result.DigitalSilence = peak == 0
	if peak > 0 {
		result.PeakDBFS = 20 * math.Log10(peak)
		result.RMSDBFS = 10 * math.Log10(squares/float64(len(pcm)/2))
	}
	result.LowEnergy = result.RMSDBFS <= -60 && result.PeakDBFS <= -45
	if result.DigitalSilence {
		result.SpeechPresence = "absent"
	}
	return result, nil
}
