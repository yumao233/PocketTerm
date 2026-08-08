package ws

import (
	"bytes"
	"reflect"
	"testing"
)

// TestParseInputFrame is a table-driven test of ParseInputFrame covering every
// edge case from the task brief: resize/close control frames, raw terminal
// data, malformed leading-'{', empty input, and unknown/missing types.
func TestParseInputFrame(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		wantKind InputKind
		// wantData is the raw bytes returned for InputData frames. For
		// InputResize/InputClose the returned data must be nil.
		wantData []byte
		// wantCtrl is non-nil only for InputResize/InputClose.
		wantCtrl *ControlFrame
	}{
		{
			name:     "resize control frame",
			input:    []byte(`{"type":"resize","cols":80,"rows":24}`),
			wantKind: InputResize,
			wantData: nil,
			wantCtrl: &ControlFrame{Type: "resize", Cols: 80, Rows: 24},
		},
		{
			name:     "close control frame",
			input:    []byte(`{"type":"close"}`),
			wantKind: InputClose,
			wantData: nil,
			wantCtrl: &ControlFrame{Type: "close"},
		},
		{
			name:     "close control frame with reason",
			input:    []byte(`{"type":"close","reason":"user exited"}`),
			wantKind: InputClose,
			wantData: nil,
			wantCtrl: &ControlFrame{Type: "close", Reason: "user exited"},
		},
		{
			name:     "raw terminal data: ping command",
			input:    []byte("ping -c 1 127.0.0.1\n"),
			wantKind: InputData,
			wantData: []byte("ping -c 1 127.0.0.1\n"),
			wantCtrl: nil,
		},
		{
			name:     "malformed leading brace treated as data",
			input:    []byte("{malformed"),
			wantKind: InputData,
			wantData: []byte("{malformed"),
			wantCtrl: nil,
		},
		{
			name:     "empty object treated as data (no type)",
			input:    []byte("{}"),
			wantKind: InputData,
			wantData: []byte("{}"),
			wantCtrl: nil,
		},
		{
			name:     "unknown type treated as data",
			input:    []byte(`{"type":"unknown"}`),
			wantKind: InputData,
			wantData: []byte(`{"type":"unknown"}`),
			wantCtrl: nil,
		},
		{
			name:     "empty input treated as data",
			input:    []byte{},
			wantKind: InputData,
			wantData: []byte{},
			wantCtrl: nil,
		},
		{
			name:     "data not starting with brace",
			input:    []byte("hello world\r\n"),
			wantKind: InputData,
			wantData: []byte("hello world\r\n"),
			wantCtrl: nil,
		},
		{
			name:     "resize with extra fields still resize",
			input:    []byte(`{"type":"resize","cols":120,"rows":50,"reason":"fit"}`),
			wantKind: InputResize,
			wantData: nil,
			wantCtrl: &ControlFrame{Type: "resize", Cols: 120, Rows: 50, Reason: "fit"},
		},
		{
			name:     "binary-safe data with non-utf8 bytes",
			input:    []byte{0x01, 0x02, 0xff, 0xfe, 'a', 'b'},
			wantKind: InputData,
			wantData: []byte{0x01, 0x02, 0xff, 0xfe, 'a', 'b'},
			wantCtrl: nil,
		},
		{
			name:     "json array treated as data (does not parse into ControlFrame)",
			input:    []byte(`["not","a","control","frame"]`),
			wantKind: InputData,
			wantData: []byte(`["not","a","control","frame"]`),
			wantCtrl: nil,
		},
		{
			name:     "json string treated as data",
			input:    []byte(`"just a string"`),
			wantKind: InputData,
			wantData: []byte(`"just a string"`),
			wantCtrl: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, data, ctrl := ParseInputFrame(tc.input)
			if kind != tc.wantKind {
				t.Fatalf("kind = %v; want %v", kind, tc.wantKind)
			}
			if tc.wantKind == InputData {
				if !bytes.Equal(data, tc.wantData) {
					t.Fatalf("data = %q; want %q", data, tc.wantData)
				}
				if ctrl != nil {
					t.Fatalf("ctrl = %+v; want nil for InputData", ctrl)
				}
			} else {
				// Resize/Close: data must be nil, ctrl must match.
				if data != nil {
					t.Fatalf("data = %q; want nil for %v", data, tc.wantKind)
				}
				if !reflect.DeepEqual(ctrl, tc.wantCtrl) {
					t.Fatalf("ctrl = %+v; want %+v", ctrl, tc.wantCtrl)
				}
			}
		})
	}
}