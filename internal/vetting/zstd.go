package vetting

import "github.com/klauspost/compress/zstd"

func zstdBytes(b []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return enc.EncodeAll(b, nil), nil
}

func unzstd(b []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(b, nil)
}
