package pebblestore

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// A wav declared audio/wav (as the web client and the app send it) is
// staged, though Go detects it as audio/wave; it keeps the one spelling.
func TestMediaStagingWav(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	binary.Write(&wav, binary.LittleEndian, uint32(36+8))
	wav.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(8000), uint32(16000), uint16(2), uint16(16)} {
		binary.Write(&wav, binary.LittleEndian, v)
	}
	wav.WriteString("data")
	binary.Write(&wav, binary.LittleEndian, uint32(8))
	wav.Write(make([]byte, 8))
	for i, declared := range []string{"audio/wav", "audio/x-wav", "audio/wave"} {
		record, _, err := NewMediaStagingStore(db).Put(PutMediaStagingInput{AccountScopeID: "account", IdempotencyKey: "wav-" + declared, DeclaredMIMEType: declared, FileName: "take.wav", TTL: time.Hour, NowUnixMs: int64(1000 + i), Reader: bytes.NewReader(wav.Bytes())})
		if err != nil {
			t.Fatalf("%s: %v", declared, err)
		}
		if record.DeclaredMIMEType != "audio/wav" || record.DetectedMIMEType != "audio/wav" {
			t.Fatalf("%s staged as %q / %q", declared, record.DeclaredMIMEType, record.DetectedMIMEType)
		}
	}
}
