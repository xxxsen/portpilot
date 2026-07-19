package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

type oneByteReader struct {
	reader io.Reader
}

func (r oneByteReader) Read(data []byte) (int, error) {
	if len(data) > 1 {
		data = data[:1]
	}
	return r.reader.Read(data)
}

func TestJSONFrameHandlesFragmentedReads(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	input := StreamOpen{
		Version: Version, Role: "tcp-stream", ClientID: "client",
		SessionID: "session", SessionEpoch: 7, ConnectionID: "connection",
	}
	if err := WriteJSONFrame(&buffer, input); err != nil {
		t.Fatal(err)
	}
	var output StreamOpen
	if err := ReadJSONFrame(oneByteReader{reader: &buffer}, DefaultMaxJSONFrame, &output); err != nil {
		t.Fatal(err)
	}
	if output != input {
		t.Fatalf("frame mismatch: %#v != %#v", output, input)
	}
}

func TestJSONFrameRejectsOversizedLengthBeforeAllocation(t *testing.T) {
	t.Parallel()
	var data [4]byte
	binary.BigEndian.PutUint32(data[:], DefaultMaxJSONFrame+1)
	var output StreamOpen
	if err := ReadJSONFrame(bytes.NewReader(data[:]), DefaultMaxJSONFrame, &output); err == nil {
		t.Fatal("oversized frame was accepted")
	}
}

func TestUDPFrameRoundTripWithFragmentation(t *testing.T) {
	t.Parallel()
	tests := []*net.UDPAddr{
		{IP: net.ParseIP("192.0.2.10"), Port: 5353},
		{IP: net.ParseIP("2001:db8::10"), Port: 5353},
	}
	for _, address := range tests {
		var buffer bytes.Buffer
		input := UDPFrame{
			Type: UDPTypeDataToClient, Flags: 5, FlowID: 42,
			Addr: address, Data: []byte("datagram payload"),
		}
		if err := WriteUDPFrame(&buffer, input, 65507); err != nil {
			t.Fatal(err)
		}
		output, err := ReadUDPFrame(oneByteReader{reader: &buffer}, 65507)
		if err != nil {
			t.Fatal(err)
		}
		if output.Type != input.Type || output.Flags != input.Flags || output.FlowID != input.FlowID ||
			output.Addr.String() != input.Addr.String() || !bytes.Equal(output.Data, input.Data) {
			t.Fatalf("UDP frame mismatch: %#v != %#v", output, input)
		}
	}
}

func TestUDPFrameRejectsInvalidLengths(t *testing.T) {
	t.Parallel()
	header := make([]byte, UDPHeaderSize)
	header[0] = Version
	header[1] = UDPTypeDataToServer
	binary.BigEndian.PutUint32(header[14:18], 65508)
	if _, err := ReadUDPFrame(bytes.NewReader(header), 65507); err == nil {
		t.Fatal("oversized UDP frame was accepted")
	}
}
