package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	LengthPrefixSize = 4
	MinFrameSize     = 12
	MaxFrameSize     = 4 * 1024 * 1024
)

var (
	ErrFrameTooSmall = errors.New("frame length is below minimum")
	ErrFrameTooLarge = errors.New("frame length exceeds maximum")
)

func ReadFrame(conn net.Conn, timeout time.Duration) ([]byte, error) {
	if conn == nil {
		return nil, fmt.Errorf("connection is nil")
	}

	var lengthPrefix [LengthPrefixSize]byte
	if err := setReadDeadline(conn, timeout); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, lengthPrefix[:]); err != nil {
		clearReadDeadline(conn, timeout)
		return nil, err
	}

	length := binary.LittleEndian.Uint32(lengthPrefix[:])
	if length < MinFrameSize {
		clearReadDeadline(conn, timeout)
		return nil, ErrFrameTooSmall
	}
	if length > MaxFrameSize {
		clearReadDeadline(conn, timeout)
		return nil, ErrFrameTooLarge
	}

	frame := make([]byte, length)
	if err := setReadDeadline(conn, timeout); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, frame); err != nil {
		clearReadDeadline(conn, timeout)
		return nil, err
	}

	clearReadDeadline(conn, timeout)
	return frame, nil
}

func WriteFrame(conn net.Conn, frame []byte, timeout time.Duration) error {
	if conn == nil {
		return fmt.Errorf("connection is nil")
	}
	if len(frame) < MinFrameSize {
		return ErrFrameTooSmall
	}
	if len(frame) > MaxFrameSize {
		return ErrFrameTooLarge
	}

	var lengthPrefix [LengthPrefixSize]byte
	binary.LittleEndian.PutUint32(lengthPrefix[:], uint32(len(frame)))

	if err := setWriteDeadline(conn, timeout); err != nil {
		return err
	}
	if err := writeFull(conn, lengthPrefix[:]); err != nil {
		clearWriteDeadline(conn, timeout)
		return err
	}
	if err := writeFull(conn, frame); err != nil {
		clearWriteDeadline(conn, timeout)
		return err
	}

	clearWriteDeadline(conn, timeout)
	return nil
}

func writeFull(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		n, err := conn.Write(payload)
		if err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

func setReadDeadline(conn net.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	return conn.SetReadDeadline(time.Now().Add(timeout))
}

func clearReadDeadline(conn net.Conn, timeout time.Duration) {
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Time{})
	}
}

func setWriteDeadline(conn net.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	return conn.SetWriteDeadline(time.Now().Add(timeout))
}

func clearWriteDeadline(conn net.Conn, timeout time.Duration) {
	if timeout > 0 {
		_ = conn.SetWriteDeadline(time.Time{})
	}
}
