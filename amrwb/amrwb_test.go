package amrwb

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/livekit/amrwb-cgo"
	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/res"
	"github.com/livekit/media-sdk/res/testdata"
)

const rate = 16000

func TestAMRWB(t *testing.T) {
	frames := readOGG()

	modes := [9]amrwb.Mode{
		amrwb.Mode6Kb,
		amrwb.Mode8Kb,
		amrwb.Mode12Kb,
		amrwb.Mode14Kb,
		amrwb.Mode16Kb,
		amrwb.Mode18Kb,
		amrwb.Mode20Kb,
		amrwb.Mode23Kb,
		amrwb.Mode24Kb,
	}

	const selFrame = 41

	pcmHash := [9][]string{
		0: {
			"3e1459dd8f153700478c28574d503c089af19057", // mac
			"3127946be110e6ebc8d64ed18a3334415a61ac4a", // linux
		},
		1: {
			"4d3e177ac9e6b71e0f74443aa137ffc4e4de15f9", // mac
			"3d0f014fcdf88ad8e5e5ff7617470442db6d661c", // linux
		},
		2: {
			"6565057924a517608b265b7dc4d1cdc1363c82a8", // mac
			"9bb552a97849e1fd572d1df657ab0bb9998013e2", // linux
		},
		3: {
			"47523e7b0defa53a6e631982e06bbbdf221c4ec6", // mac
			"98d374a1c27b7d2a682df7d2c5b12abd330105f9", // linux
		},
		4: {
			"1a8c286d32abc7695c34da729bcc6681407153d5", // mac
			"ad82c55e9a5df28c7bf254c136a2d1bcc575ba87", // linux
		},
		5: {
			"64172da7990f652ae993f70b2bcdb26753e16bfa", // mac
			"8d7543be36a002b9de5fa035d5667545defd7bc0", // linux
		},
		6: {
			"6880cd543b5d707b69fc73a46d23c1cd1ad8134e", // mac
			"37692d0cff60b592f99b85bbb883469696785035", // linux
		},
		7: {
			"251335ded3c16b58aaac409908e7dee0ff458333", // mac
			"c67f2fbb32b3d9825a211cbc91ed39d88fc8dba4", // linux
		},
		8: {
			"2315d3ac38e9b4523bd380c3719d1d8477c5fd00", // mac
			"06f13780fe3b87a68c78ebe2e7f5d51c31adfc50", // linux
		},
	}

	t.Run("storage", func(t *testing.T) {
		const format = Storage
		hashes := [9][]string{
			0: {
				"0236b84c931bcf121da1a0587420d30d1aac0301", // mac
				"d9151ae183596869c845bb2e3bbfc6f05a7138ba", // linux
			},
			1: {
				"dabd16afd6fcacefc26d221e3fa06a2bc6644076", // mac
				"cdb173454b66f61bf3e783285d10d598f5686824", // linux
			},
			2: {
				"61d20f4e120dd13e2fbaaf74dd3c08863ed5a557", // mac
				"029223c3b4a263c7e472020954b9560b23d3b46f", // linux
			},
			3: {
				"bece54885e71b89dfcd86c8cbe7ce9fd2fefb4f7", // mac
				"4e43af5ec9e6de64828276d31b85d3f153b2e17a", // linux
			},
			4: {
				"67fa8975e5757d3b83aba349a7cc1f4e5d06ff66", // mac
				"a6cba7433d37e36718c8d4a31109e53a87ab699b", // linux
			},
			5: {
				"6f9c014385c8e7dbde780ae2e4a89b03b5e03dcb", // mac
				"484fb0173d291785a914876e9dab6c302117e635", // linux
			},
			6: {
				"039be8ddd5d2de4bb148d166552e6d59eebb3e40", // mac
				"39d075885e5cdfcff597e0ee64d599793446c9ab", // linux
			},
			7: {
				"2d0346353617d23bb59a6519167707115be2245e", // mac
				"f410a08e3ca0644663efbd92e047da64554abd3e", // linux
			},
			8: {
				"266c17405ad9cb6057bfe1a3e0cf764c448a10a0", // mac
				"b3d9349b01e93c523311e587d639142d0c79546b", // linux
			},
		}
		for mi, mode := range modes {
			t.Run(fmt.Sprintf("mode %d", mi), func(t *testing.T) {
				hashes := hashes[mi]

				blocks := encodeAMR(t, format, mode, frames)
				t.Logf("block: %x", blocks[selFrame])

				name := fmt.Sprintf("testdata_mode_%d", mode)

				hashAMR := dumpAMR(t, name+".amrwb", blocks)
				ffmpegAMRFileToOGG(t, name+".amrwb")
				if !slices.Contains(hashes, hashAMR) {
					t.Errorf("unexpected amrwb hash %s", hashAMR)
				}

				out := decodeAMR(t, format, blocks)

				hashOut := dumpPCM(t, name+".s16le", out)
				ffmpegPCMFileToOGG(t, name+".s16le")
				if !slices.Contains(pcmHash[mi], hashOut) {
					t.Errorf("unexpected output hash %s", hashOut)
				}
			})
		}
	})
	t.Run("rtp", func(t *testing.T) {
		const format = RTPBandwidthEfficient
		hashes := [9][]string{
			0: {
				"a8e453a08c894e2c3d0f7bf62142d6eb63bfe5c8", // mac
				"c202d8b71545a3886991712857e82948efbb8037", // linux
			},
			1: {
				"270a13237d862e9e20e725a0a9484edddc3b4f00", // mac
				"a4819af7e4a41ffd916671e632db55e85151995b", // linux
			},
			2: {
				"607966a93720aaa1c8425e08b6471f58282c0884", // mac
				"837c6def6dbd34aeb466bfc255c084cbc5af4ca9", // linux
			},
			3: {
				"8a620e9c3ce1656ea07804be210befe5c878f3f3", // mac
				"a64abd663ade6a5ebb2c9f6e539a669b935ca23f", // linux
			},
			4: {
				"9121d8781a397f4784174f15309f6f48d86db338", // mac
				"8b2ffe416d0a0f7e7d06baf44369f9ed4bb51388", // linux
			},
			5: {
				"a6e005daf0e8040530157be090934376610beeb7", // mac
				"d4aba015c82e362fe1582cba7ee94579abed9aa9", // linux
			},
			6: {
				"4497873be7c5574468e68c0718a5e57d3c8ac099", // mac
				"e4cc30e3a4e6e0f65a08d7764ba208ab432ab9aa", // linux
			},
			7: {
				"b73f2049b96be4793f611339e4d3435ec19a8760", // mac
				"31e80f7eb50f12a552e86b547eda421defa7bd16", // linux
			},
			8: {
				"7aa1a489f7f951ab421f0e462abb18c7d4c00c68", // mac
				"cbe884fc78d580078ccdb3ed7acec77928cffa52", // linux
			},
		}
		for mi, mode := range modes {
			t.Run(fmt.Sprintf("mode %d", mi), func(t *testing.T) {
				hashes := hashes[mi]

				blocks := encodeAMR(t, format, mode, frames)
				t.Logf("block: %x", blocks[selFrame])

				name := fmt.Sprintf("testdata_rtp_mode_%d", mode)

				hashAMR := dumpAMR(t, name+".amrwb", blocks)
				if !slices.Contains(hashes, hashAMR) {
					t.Errorf("unexpected amrwb hash %s", hashAMR)
				}

				out := decodeAMR(t, format, blocks)

				hashOut := dumpPCM(t, name+".s16le", out)
				ffmpegPCMFileToOGG(t, name+".s16le")
				if !slices.Contains(pcmHash[mi], hashOut) {
					t.Errorf("unexpected output hash %s", hashOut)
				}
			})
		}
	})
}

func readOGG() []media.PCM16Sample {
	return res.ReadOggAudioFile(testdata.TestAudioOgg16K, rate, 1)
}

func encodeAMR(t testing.TB, format amrwb.Format, mode amrwb.Mode, frames []media.PCM16Sample) []Sample {
	blocks := make([]Sample, 0, len(frames))
	fw := media.NewFrameWriter(&blocks, rate)

	enc := EncodeWith(fw, format, mode)
	t.Cleanup(func() {
		enc.Close()
	})
	for i, frame := range frames {
		err := enc.WriteSample(frame)
		if err != nil {
			t.Errorf("encoding frame %d/%d: %v", i+1, len(frames), err)
		}
	}
	return blocks
}

func dumpAMR(t testing.TB, name string, blocks []Sample) string {
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	h := sha1.New()
	bw := bufio.NewWriter(io.MultiWriter(f, h))
	bw.WriteString(amrwb.Magic)
	for _, block := range blocks {
		bw.Write(block)
	}
	bw.Flush()

	return hex.EncodeToString(h.Sum(nil))
}

func decodeAMR(t testing.TB, format amrwb.Format, blocks []Sample) []media.PCM16Sample {
	var out []media.PCM16Sample
	pw := media.NewPCM16FrameWriter(&out, rate)

	dec := Decode(pw, format)
	t.Cleanup(func() {
		dec.Close()
	})
	for _, block := range blocks {
		err := dec.WriteSample(block)
		if err != nil {
			t.Error(err)
		}
	}
	return out
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func dumpPCM(t testing.TB, name string, data []media.PCM16Sample) string {
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	h := sha1.New()
	bw := bufio.NewWriter(io.MultiWriter(f, h))
	err = media.DumpFramesPCM16(struct {
		io.Writer
		io.Closer
	}{
		Writer: bw,
		Closer: nopCloser{},
	}, rate, data)
	if err != nil {
		t.Fatal(err)
	}
	bw.Flush()

	return hex.EncodeToString(h.Sum(nil))
}

func ffmpegAMRFileToOGG(t testing.TB, name string) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Log("ffmpeg not found in $PATH")
		return
	}
	err := exec.Command("ffmpeg",
		"-i", name, name+".ogg",
	).Run()
	if err != nil {
		t.Error(err)
	} else {
		os.Remove(name)
	}
}

func ffmpegPCMFileToOGG(t testing.TB, name string) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Log("ffmpeg not found in $PATH")
		return
	}
	err := exec.Command("ffmpeg",
		"-f", "s16le", "-ar", "16000", "-ac", "1",
		"-i", name, name+".ogg",
	).Run()
	if err != nil {
		t.Error(err)
	} else {
		os.Remove(name)
	}
}
