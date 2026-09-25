package butako

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// testWAV builds a 16 kHz mono PCM16 WAV of the given sample count and amplitude.
func testWAV(samples int, amplitude float64) []byte {
	data := make([]byte, 44+samples*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 16000)
	binary.LittleEndian.PutUint32(data[28:], 32000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(samples*2))
	for i := 0; i < samples; i++ {
		value := int16(amplitude * 32767 * math.Sin(float64(i)*2*math.Pi*440/16000))
		binary.LittleEndian.PutUint16(data[44+i*2:], uint16(value))
	}
	return data
}

func TestValidateWAV(t *testing.T) {
	stereo := testWAV(16000, 0.3)
	binary.LittleEndian.PutUint16(stereo[22:], 2)
	truncated := testWAV(16000, 0.3)
	binary.LittleEndian.PutUint32(truncated[40:], 1<<30)

	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"voice", testWAV(16000, 0.3), nil},
		{"silent", testWAV(16000, 0), errSilence},
		{"under half a second", testWAV(7999, 0.3), errSilence},
		{"not wav", []byte("hello hello hello hello hello hello hello hello"), errInvalidAudio},
		{"stereo", stereo, errInvalidAudio},
		{"chunk beyond data", truncated, errInvalidAudio},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateWAV(test.data); !errors.Is(err, test.want) {
				t.Fatalf("validateWAV() = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEmptyOrHallucinated(t *testing.T) {
	for text, want := range map[string]bool{
		"":     true,
		"  。 ": true,
		"ご視聴ありがとうございました。":         true,
		"「字幕をご覧いただきありがとうございました」":  true,
		"Thank you for watching!": true,
		"ブタコ、元気？":                 false,
	} {
		if got := emptyOrHallucinated(text); got != want {
			t.Errorf("emptyOrHallucinated(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestSpeechText(t *testing.T) {
	for display, want := range map[string]string{
		"**フゴー**、元気だよ！🐷":        "フゴー、元気だよ！",
		"Manchester United が好き": "マンチェスター・ユナイテッド が好き",
		"# Butako\nです":          "ブタコ です",
		"🐷✨":                    "",
	} {
		if got := speechText(display); got != want {
			t.Errorf("speechText(%q) = %q, want %q", display, got, want)
		}
	}
}
