package voice

import (
	"errors"
	"io"
	"os"
	"time"
)

// MPEG audio header tables (MPEG-1/2/2.5, layer III only: edge-tts output).
var (
	bitrates = map[int][16]int{ // kbit/s by version: 1 = MPEG-1, 2 = MPEG-2/2.5
		1: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
		2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	}
	sampleRates = map[int][4]int{ // by version bits: 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5
		3: {44100, 48000, 32000, 0},
		2: {22050, 24000, 16000, 0},
		0: {11025, 12000, 8000, 0},
	}
)

// MP3Duration walks the frames of a layer III mp3 and sums their length.
func MP3Duration(path string) (time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil {
		return 0, err
	}
	i := 0
	if len(b) >= 10 && string(b[:3]) == "ID3" { // skip an ID3v2 tag
		i = 10 + int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f)
	}
	var total float64
	frames := 0
	for i+4 <= len(b) {
		if b[i] != 0xFF || b[i+1]&0xE0 != 0xE0 {
			i++
			continue
		}
		ver := int(b[i+1]>>3) & 3
		layer := int(b[i+1]>>1) & 3
		bi := int(b[i+2] >> 4)
		si := int(b[i+2]>>2) & 3
		pad := int(b[i+2]>>1) & 1
		rates, ok := sampleRates[ver]
		if ver == 1 || layer != 1 || !ok || rates[si] == 0 || bi == 0 || bi == 15 {
			i++
			continue
		}
		table, samples := 2, 576
		if ver == 3 {
			table, samples = 1, 1152
		}
		sr := rates[si]
		size := (samples/8)*bitrates[table][bi]*1000/sr + pad
		if size < 4 {
			i++
			continue
		}
		total += float64(samples) / float64(sr)
		frames++
		i += size
	}
	if frames == 0 {
		return 0, errors.New("voice: no mp3 frames")
	}
	return time.Duration(total * float64(time.Second)), nil
}
