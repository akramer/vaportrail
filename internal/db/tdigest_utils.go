package db

import (
	"bytes"
	"encoding/gob"

	"github.com/influxdata/tdigest"
)

// SerializeTDigest serializes the T-Digest to bytes for storage.
func SerializeTDigest(td *tdigest.TDigest) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(td.Centroids())
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DeserializeTDigest deserializes bytes to a T-Digest.
func DeserializeTDigest(data []byte) (*tdigest.TDigest, error) {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	var centroids tdigest.CentroidList
	if err := dec.Decode(&centroids); err != nil {
		return nil, err
	}
	td := tdigest.New()
	td.AddCentroidList(centroids)
	return td, nil
}
