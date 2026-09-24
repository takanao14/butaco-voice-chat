package butako

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
)

var errInvalidAudio = errors.New("invalid_audio")
var errSilence = errors.New("silence")

func validateWAV(data []byte) error {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return errInvalidAudio
	}
	var format, channels, bits uint16
	var sampleRate uint32
	var samples []byte
	for offset := 12; offset+8 <= len(data); {
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		end := start + size
		if size < 0 || end < start || end > len(data) {
			return errInvalidAudio
		}
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if size < 16 {
				return errInvalidAudio
			}
			format = binary.LittleEndian.Uint16(data[start : start+2])
			channels = binary.LittleEndian.Uint16(data[start+2 : start+4])
			sampleRate = binary.LittleEndian.Uint32(data[start+4 : start+8])
			bits = binary.LittleEndian.Uint16(data[start+14 : start+16])
		case "data":
			samples = data[start:end]
		}
		offset = end + size%2
	}
	if format != 1 || channels != 1 || sampleRate != 16000 || bits != 16 || len(samples)%2 != 0 {
		return errInvalidAudio
	}
	if len(samples) < 16000 {
		return errSilence
	}
	var sum float64
	for offset := 0; offset < len(samples); offset += 2 {
		value := float64(int16(binary.LittleEndian.Uint16(samples[offset:offset+2]))) / 32768
		sum += value * value
	}
	rms := math.Sqrt(sum / float64(len(samples)/2))
	if rms < 0.003 {
		return errSilence
	}
	return nil
}

func emptyOrHallucinated(text string) bool {
	clean := strings.Trim(strings.TrimSpace(text), " \n\r\t。、.!！?？\"'「」")
	switch clean {
	case "", "ご視聴ありがとうございました", "字幕をご覧いただきありがとうございました", "Thank you for watching":
		return true
	}
	return false
}

func speechText(display string) string {
	text := strings.NewReplacer(
		"Manchester United", "マンチェスター・ユナイテッド",
		"Butako", "ブタコ",
		"**", "", "__", "", "`", "", "#", "", "*", "",
	).Replace(display)
	var clean strings.Builder
	for _, r := range text {
		if (r >= 0x1f000 && r <= 0x1faff) || (r >= 0x2600 && r <= 0x27bf) {
			continue
		}
		clean.WriteRune(r)
	}
	return strings.Join(strings.Fields(clean.String()), " ")
}
