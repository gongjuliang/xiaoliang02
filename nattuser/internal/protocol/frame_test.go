package protocol

import (
	"bytes"
	"testing"
)

func TestReadWriteMessage(t *testing.T) {
	message, err := NewMessage(TypeHeartbeat, 12, 0, "", Heartbeat{ClientTime: 100})
	if err != nil {
		t.Fatalf("new message: %v", err)
	}

	var buf bytes.Buffer
	if err := WriteMessage(&buf, message); err != nil {
		t.Fatalf("write message: %v", err)
	}
	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if got.Type != TypeHeartbeat || got.ClientID != 12 || got.RequestID == "" {
		t.Fatalf("unexpected message: %+v", got)
	}
	payload, err := DecodePayload[Heartbeat](got)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.ClientTime != 100 {
		t.Fatalf("payload client_time=%d want=100", payload.ClientTime)
	}
}
