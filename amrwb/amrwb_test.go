package amrwb

import (
	"os"
	"os/exec"
	"testing"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/res"
	"github.com/livekit/media-sdk/res/testdata"
)

func TestAMRWB(t *testing.T) {
	const rate = 16000
	frames := res.ReadOggAudioFile(testdata.TestAudioOgg16K, rate, 1)

	blocks := make([]Sample, 0, len(frames))
	fw := media.NewFrameWriter(&blocks, rate)

	enc := Encode(fw)
	t.Cleanup(func() {
		enc.Close()
	})
	for i, frame := range frames {
		err := enc.WriteSample(frame)
		if err != nil {
			t.Errorf("encoding frame %d/%d: %v", i+1, len(frames), err)
		}
	}
	famr, err := os.Create("testdata.amrwb")
	if err != nil {
		t.Fatal(err)
	}
	defer famr.Close()

	famr.WriteString("#!AMR-WB\n")
	for _, block := range blocks {
		famr.Write(block)
	}

	var out []media.PCM16Sample
	pw := media.NewPCM16FrameWriter(&out, rate)

	dec := Decode(pw)
	t.Cleanup(func() {
		dec.Close()
	})
	for _, block := range blocks {
		err := dec.WriteSample(block)
		if err != nil {
			t.Error(err)
		}
	}

	f, err := os.Create("testdata.s16le")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = media.DumpFramesPCM16(f, rate, out)
	if err != nil {
		t.Fatal(err)
	}

	err = exec.Command("ffmpeg",
		"-i", "testdata.amrwb",
		"testdata.amrwb.ogg",
	).Run()
	if err != nil {
		t.Error(err)
	}
	err = exec.Command("ffmpeg",
		"-f", "s16le", "-ar", "16000", "-ac", "1",
		"-i", "testdata.s16le",
		"testdata.s16le.ogg",
	).Run()
	if err != nil {
		t.Error(err)
	}
}
