package amrwb

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"io"
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

	hamr := sha1.New()

	wbamr := bufio.NewWriter(io.MultiWriter(famr, hamr))
	wbamr.WriteString("#!AMR-WB\n")
	for _, block := range blocks {
		wbamr.Write(block)
	}
	wbamr.Flush()

	hashAMR := hex.EncodeToString(hamr.Sum(nil))
	if hashAMR != "266c17405ad9cb6057bfe1a3e0cf764c448a10a0" {
		t.Errorf("unexpected amrwb hash %s", hashAMR)
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

	h := sha1.New()
	bw := bufio.NewWriter(io.MultiWriter(f, h))
	err = media.DumpFramesPCM16(f, rate, out)
	if err != nil {
		t.Fatal(err)
	}
	bw.Flush()

	hashOut := hex.EncodeToString(h.Sum(nil))
	if hashOut != "da39a3ee5e6b4b0d3255bfef95601890afd80709" {
		t.Errorf("unexpected output hash %s", hashOut)
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Log("ffmpeg not found in $PATH")
		return
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
