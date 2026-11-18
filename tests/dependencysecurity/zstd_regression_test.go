// Regression fixture adapted from klauspost/compress v1.20.2 zstd/decoder_test.go
// Upstream license for the adapted fixture is in zstd_regression.LICENSE.
// https://github.com/klauspost/compress/security/advisories/GHSA-fpmh-frq8-7rj8
package dependencysecurity_test

import (
	"bytes"
	"github.com/klauspost/compress/zstd"
	"io"
	"slices"
	"testing"
)

// TestRegressionZstdRejectsInvalidHuffmanWeights decodes two blocks with Huffman weights at
// FSE tableLog 15. The second table's only count, 1<<15, overflowed an int16
// and decoded with the first table's state, panicking in fse. That state is
// kept between blocks, and comes back from huffDecoderPool in the next frame.
func TestRegressionZstdRejectsInvalidHuffmanWeights(t *testing.T) {
	oneFrame := slices.Concat(
		[]byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x38, 0x74, 0x06, 0x00, 0x42, 0xb8, 0x32, 0x0d, 0x1a, 0x00, 0x00, 0x00, 0x90, 0xaf, 0xfa, 0xff, 0x03, 0x00, 0x80, 0xff, 0x7f},
		bytes.Repeat([]byte{0xa5, 0x94, 0x52, 0x4a, 0x29}, 37),
		[]byte{0xa5, 0x94, 0x12, 0x00, 0xad, 0x56, 0x03, 0xee, 0xff, 0xdf, 0xb3, 0x1a, 0x1c, 0x1a, 0x00, 0xe0, 0xff, 0x1f, 0x0f, 0xb1, 0x9c, 0x5a, 0xcf, 0x4d, 0xe4, 0x1d, 0x7b, 0xab, 0x99, 0x5e, 0x01, 0x51, 0xa4, 0x73, 0xd8, 0xb1, 0xf3, 0x39, 0x4b, 0x67, 0xf6, 0xab, 0x1a, 0xab, 0x1a, 0xab, 0x1a},
		bytes.Repeat([]byte{0x8c, 0x31, 0xc6, 0x18, 0x63}, 1365),
		[]byte{0x8c, 0x31},
		bytes.Repeat([]byte{0x63, 0x8c, 0x31, 0xc6, 0x18}, 1365),
		[]byte{0x63, 0x2c},
		bytes.Repeat([]byte{0x31, 0xc6, 0x18, 0x63, 0x8c}, 1365),
		[]byte{0x31, 0x46},
		bytes.Repeat([]byte{0x31, 0xc6, 0x18, 0x63, 0x8c}, 1365),
		[]byte{0x31, 0x06, 0x00},
	)
	// The same blocks, the first marked last, each in its own frame.
	twoFrames := slices.Concat(oneFrame[:6], []byte{0x75, 0x06, 0x00}, oneFrame[9:215], oneFrame[:6], oneFrame[215:])
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	for _, in := range [][]byte{oneFrame, twoFrames} {
		if _, err := dec.DecodeAll(in, nil); err == nil {
			t.Error("DecodeAll: want error")
		}
		if err := dec.Reset(bytes.NewReader(in)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(dec); err == nil {
			t.Error("Reader: want error")
		}
	}
}

func TestRegressionZstdValidRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("go-fluentd compression compatibility control\n"), 64)
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	compressed := enc.EncodeAll(payload, nil)
	for i := 0; i < 3; i++ {
		got, err := dec.DecodeAll(compressed, nil)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("roundtrip: %v", err)
		}
	}
}
