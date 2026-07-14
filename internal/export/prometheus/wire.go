// Package prometheus implements a remote_write v1 exporter (Sink) for
// Prometheus-compatible backends (Grafana Cloud, Mimir, VictoriaMetrics, etc.).
//
// Protobuf encoding is done with hand-rolled varint/delta helpers instead of
// github.com/prometheus/prometheus/prompb to keep the dependency tree tiny, pure
// Go, and ARM-friendly (no cgo, no huge protobuf toolchain).
package prometheus

import (
	"bytes"
	"encoding/binary"
	"math"
)

// ---- wire types for proto3 (only the ones we need) ----

const (
	wireVarint          = 0
	wireFixed64         = 1
	wireLengthDelimited = 2
)

// ---- internal message types mirroring the protobuf schema ----

type writeRequest struct {
	Timeseries []timeSeries
}

type timeSeries struct {
	Labels  []label
	Samples []psample
}

type label struct {
	Name  string
	Value string
}

type psample struct {
	Value       float64
	TimestampMs int64
}

// ---- top-level marshal entry point ----

// marshal serialises the WriteRequest to protobuf wire format.
func (w *writeRequest) marshal() []byte {
	var buf bytes.Buffer
	for _, ts := range w.Timeseries {
		data := ts.marshal()
		writeTag(&buf, 1, wireLengthDelimited) // field 1: timeseries (repeated message)
		writeVarint(&buf, uint64(len(data)))
		buf.Write(data)
	}
	return buf.Bytes()
}

func (ts *timeSeries) marshal() []byte {
	var buf bytes.Buffer
	for _, l := range ts.Labels {
		data := l.marshal()
		writeTag(&buf, 1, wireLengthDelimited) // field 1: labels (repeated message)
		writeVarint(&buf, uint64(len(data)))
		buf.Write(data)
	}
	for _, s := range ts.Samples {
		data := s.marshal()
		writeTag(&buf, 2, wireLengthDelimited) // field 2: samples (repeated message)
		writeVarint(&buf, uint64(len(data)))
		buf.Write(data)
	}
	return buf.Bytes()
}

func (l *label) marshal() []byte {
	var buf bytes.Buffer
	writeString(&buf, 1, l.Name)  // field 1: name
	writeString(&buf, 2, l.Value) // field 2: value
	return buf.Bytes()
}

func (s *psample) marshal() []byte {
	var buf bytes.Buffer
	writeDouble(&buf, 1, s.Value)      // field 1: value (double, wire type 1)
	writeInt64(&buf, 2, s.TimestampMs) // field 2: timestamp (int64, wire type 0)
	return buf.Bytes()
}

// ---- low-level wire encoding helpers ----

// writeTag writes a protobuf tag byte(s): (field_number << 3) | wire_type.
func writeTag(buf *bytes.Buffer, field, wireType int) {
	writeVarint(buf, uint64((field<<3)|wireType))
}

// writeVarint encodes v as a protobuf base-128 varint.
func writeVarint(buf *bytes.Buffer, v uint64) {
	for v >= 0x80 {
		buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
}

// writeString writes a length-delimited string field (tag + varint length + bytes).
func writeString(buf *bytes.Buffer, field int, s string) {
	writeTag(buf, field, wireLengthDelimited)
	writeVarint(buf, uint64(len(s)))
	buf.WriteString(s)
}

// writeDouble writes a fixed64 double field (tag + 8 little-endian bytes).
func writeDouble(buf *bytes.Buffer, field int, v float64) {
	writeTag(buf, field, wireFixed64)
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
	buf.Write(b[:])
}

// writeInt64 writes an int64 varint field. Proto3 uses plain varint (not zigzag)
// for the int64 type.
func writeInt64(buf *bytes.Buffer, field int, v int64) {
	writeTag(buf, field, wireVarint)
	writeVarint(buf, uint64(v))
}
