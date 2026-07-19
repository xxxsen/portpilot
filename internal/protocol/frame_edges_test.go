package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestJSONFrameRejectsZeroAndTruncatedFrames(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{
		"zero":             {0, 0, 0, 0},
		"truncated header": {0, 0},
		"truncated body":   {0, 0, 0, 5, '{'},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var target map[string]any
			if err := ReadJSONFrame(bytes.NewReader(data), DefaultMaxJSONFrame, &target); err == nil {
				t.Fatal("invalid JSON frame was accepted")
			}
		})
	}
}

func TestUDPFrameRejectsUnknownVersionTypeAndTruncation(t *testing.T) {
	t.Parallel()
	valid := make([]byte, UDPHeaderSize)
	valid[0] = Version
	valid[1] = UDPTypeDataToServer
	binary.BigEndian.PutUint32(valid[14:18], 1)
	for name, mutate := range map[string]func([]byte) []byte{
		"unknown version": func(data []byte) []byte {
			data[0] = Version + 1
			return data
		},
		"unknown type": func(data []byte) []byte {
			data[1] = 99
			return data
		},
		"truncated payload": func(data []byte) []byte {
			return data
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := mutate(append([]byte(nil), valid...))
			if _, err := ReadUDPFrame(bytes.NewReader(data), 65507); err == nil {
				t.Fatal("invalid UDP frame was accepted")
			}
		})
	}
}

func TestUDPFrameAcceptsZeroAndBoundaryDatagrams(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 65507} {
		var encoded bytes.Buffer
		input := UDPFrame{
			Type: UDPTypeDataToServer, FlowID: 7,
			Data: bytes.Repeat([]byte{0x5a}, size),
		}
		if err := WriteUDPFrame(&encoded, input, 65507); err != nil {
			t.Fatalf("write %d-byte datagram: %v", size, err)
		}
		output, err := ReadUDPFrame(&encoded, 65507)
		if err != nil {
			t.Fatalf("read %d-byte datagram: %v", size, err)
		}
		if !bytes.Equal(output.Data, input.Data) {
			t.Fatalf("%d-byte datagram changed", size)
		}
	}
}
