package db

import (
	"bytes"

	"github.com/caio/go-tdigest/v4"
)

// SerializeTDigest serializes the T-Digest to bytes for storage.
func SerializeTDigest(td *tdigest.TDigest) ([]byte, error) {
	return td.AsBytes()
}

// DeserializeTDigest deserializes bytes to a T-Digest.
func DeserializeTDigest(data []byte) (*tdigest.TDigest, error) {
	// If empty data, return new empty digest
	if len(data) == 0 {
		return tdigest.New(tdigest.Compression(100))
	}
	return tdigest.FromBytes(bytes.NewReader(data))
}
