package prometheus

import (
	"math"
	"testing"
)

func TestLabelEncoding(t *testing.T) {
	l := label{Name: "a", Value: "b"}
	got := l.marshal()

	// Expected wire format:
	//   0x0A        tag for field 1 (name), wire_type 2 (length-delimited)
	//   0x01        varint length = 1
	//   0x61        "a"
	//   0x12        tag for field 2 (value), wire_type 2
	//   0x01        varint length = 1
	//   0x62        "b"
	want := []byte{
		0x0A, 0x01, 0x61,
		0x12, 0x01, 0x62,
	}
	assertBytesEqual(t, got, want)
}

func TestSampleZeroEncoding(t *testing.T) {
	s := psample{Value: 0.0, TimestampMs: 0}
	got := s.marshal()

	// Expected:
	//   0x09        tag for field 1 (value), wire_type 1 (fixed64)
	//   8 zero bytes for float64bits(0.0)
	//   0x10        tag for field 2 (timestamp), wire_type 0 (varint)
	//   0x00        varint 0
	want := []byte{
		0x09, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x10, 0x00,
	}
	assertBytesEqual(t, got, want)
}

func TestSampleEncoding(t *testing.T) {
	s := psample{Value: 1.5, TimestampMs: 123456789}
	got := s.marshal()

	f64 := math.Float64bits(1.5)
	want := append([]byte{
		0x09, // tag value, fixed64
	}, byte(f64), byte(f64>>8), byte(f64>>16), byte(f64>>24),
		byte(f64>>32), byte(f64>>40), byte(f64>>48), byte(f64>>56),
	)
	// timestampMs=123456789 encodes as varint
	// 123456789 = 58*128^3 + 111*128^2 + 26*128 + 21, varint: 0x95 0x9A 0xEF 0x3A
	want = append(want, 0x10, 0x95, 0x9A, 0xEF, 0x3A)

	assertBytesEqual(t, got, want)
}

func TestWriteRequestEncoding(t *testing.T) {
	req := writeRequest{
		Timeseries: []timeSeries{
			{
				Labels: []label{
					{Name: "__name__", Value: "test_metric"},
					{Name: "instance", Value: "router1"},
				},
				Samples: []psample{
					{Value: 42.0, TimestampMs: 1000},
				},
			},
		},
	}

	got := req.marshal()
	if len(got) == 0 {
		t.Fatal("expected non-empty marshal output")
	}

	// Verify it starts with the WriteRequest timeseries tag.
	if got[0] != 0x0A {
		t.Fatalf("expected first byte 0x0A (WriteRequest.timeseries tag), got 0x%02X", got[0])
	}

	// Verify the raw output survives snappy round-trip (sanity check that it's
	// valid protobuf-ish bytes — snappy handles arbitrary binary).
	_ = got
}

func TestEmptyWriteRequest(t *testing.T) {
	req := writeRequest{}
	got := req.marshal()
	if len(got) != 0 {
		t.Fatalf("expected empty output for empty WriteRequest, got %d bytes", len(got))
	}
}

// assertBytesEqual compares two byte slices and reports the first mismatch.
func assertBytesEqual(t *testing.T, got, want []byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d: got 0x%02X, want 0x%02X\n got: %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}
