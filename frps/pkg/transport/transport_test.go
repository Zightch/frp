package transport

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	frame := []byte{
		1, 2, 0, 0,
		0, 0, 0, 1,
		0, 0, 0, 0,
		'b', 'o', 'd', 'y',
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- WriteFrame(client, frame, time.Second)
	}()

	got, err := ReadFrame(server, time.Second)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("write frame: %v", err)
	}

	if string(got) != string(frame) {
		t.Fatalf("unexpected frame: got %v want %v", got, frame)
	}
}

func TestReadFrameRejectsTooSmallLength(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errCh := make(chan error, 1)
	go func() {
		var prefix [LengthPrefixSize]byte
		binary.LittleEndian.PutUint32(prefix[:], MinFrameSize-1)
		_, err := client.Write(prefix[:])
		errCh <- err
	}()

	_, err := ReadFrame(server, time.Second)
	if !errors.Is(err, ErrFrameTooSmall) {
		t.Fatalf("expected ErrFrameTooSmall, got %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("write length prefix: %v", err)
	}
}

func TestReadFrameRejectsTooLargeLength(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errCh := make(chan error, 1)
	go func() {
		var prefix [LengthPrefixSize]byte
		binary.LittleEndian.PutUint32(prefix[:], MaxFrameSize+1)
		_, err := client.Write(prefix[:])
		errCh <- err
	}()

	_, err := ReadFrame(server, time.Second)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("write length prefix: %v", err)
	}
}

func TestWriteFrameRejectsTooSmallFrame(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	err := WriteFrame(client, make([]byte, MinFrameSize-1), time.Second)
	if !errors.Is(err, ErrFrameTooSmall) {
		t.Fatalf("expected ErrFrameTooSmall, got %v", err)
	}
}
