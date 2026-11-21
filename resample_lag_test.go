package media_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
)

// Reproduces the residual slip from https://github.com/livekit/media-sdk/issues/109:
// frameBuffer.flush only wrote whole chunks, so when soxr came back a few samples short
// after warm-up (8k->48k at tick 4: 880 of 960; 16k->8k at tick 4: 108 of 160; 44.1k->8k
// at tick 28: 158 of 160), that write emitted nothing and the stream stayed one frame
// behind for the rest of the call. After warm-up every write must emit a chunk even
// when soxr's return comes up short; the reserve pays for the shortfall.
func TestResampleWriterNoPermanentLag(t *testing.T) {
	cases := []struct {
		srcRate, dstRate int
	}{
		{8000, 48000},
		{16000, 8000},
		{44100, 8000},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d-%d", c.srcRate, c.dstRate), func(t *testing.T) {
			writeFrames := c.srcRate / 50 // 20 ms
			var chunks []media.PCM16Sample
			w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, c.dstRate), c.srcRate)
			for i := 1; i <= 200; i++ {
				before := len(chunks)
				require.NoError(t, w.WriteSample(make(media.PCM16Sample, writeFrames)))
				if i <= media.DefFramesPerSec {
					continue // warm-up: soxr holds its filter delay
				}
				require.Equal(t, before+1, len(chunks), "write %d skipped a send slot", i)
			}
			require.NoError(t, w.Close())
			want := 200 * writeFrames * c.dstRate / c.srcRate
			require.InDelta(t, want, totalSamples(chunks), 2, "total output after Close")
		})
	}
}

func totalSamples(chunks []media.PCM16Sample) int {
	n := 0
	for _, c := range chunks {
		n += len(c)
	}
	return n
}
