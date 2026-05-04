package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func (c *Client) handleWorkStreamOpen(conn net.Conn, state *sessionState, frame protocol.Frame) error {
	if frame.Type != protocol.TypeStreamOpen {
		return fmt.Errorf("unsupported work message type %s", frame.Type.String())
	}
	if frame.RequestID == 0 {
		return fmt.Errorf("stream.open requestId must be non-zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("stream.open streamId must be non-zero")
	}

	open, err := protocol.UnmarshalStreamOpen(frame.Body)
	if err != nil {
		return err
	}

	_, stream, errorCode, err := c.prepareLocalTCPStream(state, open)
	if err != nil {
		_ = c.replyStreamOpened(conn, state, frame, protocol.StreamOpened{
			Status:    protocol.StatusError,
			ErrorCode: errorCode,
			Message:   err.Error(),
		})
		return err
	}

	if !state.addStream(frame.StreamID, stream) {
		_ = stream.conn.Close()
		_ = c.replyStreamOpened(conn, state, frame, protocol.StreamOpened{
			Status:    protocol.StatusError,
			ErrorCode: protocol.ErrorCodeProtocolBadBody,
			Message:   fmt.Sprintf("stream %d already exists", frame.StreamID),
		})
		return fmt.Errorf("stream %d already exists", frame.StreamID)
	}

	if err := c.replyStreamOpened(conn, state, frame, protocol.StreamOpened{Status: protocol.StatusOK}); err != nil {
		state.closeStream(frame.StreamID)
		return err
	}

	c.relayWorkConnToLocal(conn, state, frame.StreamID, stream)
	return nil
}

func (c *Client) relayWorkConnToLocal(workConn net.Conn, state *sessionState, streamID uint32, stream *localStream) {
	var cleanup sync.Once
	cleanupAll := func() {
		cleanup.Do(func() {
			state.closeStream(streamID)
			_ = workConn.Close()
		})
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- copyTCPRaw(workConn, stream.conn)
	}()
	go func() {
		errCh <- copyTCPRaw(stream.conn, workConn)
	}()

	firstErr := <-errCh
	if firstErr != nil {
		cleanupAll()
	}
	<-errCh
	cleanupAll()
}

func copyTCPRaw(dst net.Conn, src net.Conn) error {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			if writeErr := writeConnFull(dst, buffer[:n]); writeErr != nil {
				return writeErr
			}
		}

		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			closeErr := closeTCPWrite(dst)
			if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
				return closeErr
			}
			return nil
		}
		return err
	}
}

func closeTCPWrite(conn net.Conn) error {
	if closeWriter, ok := conn.(interface{ CloseWrite() error }); ok {
		return closeWriter.CloseWrite()
	}
	return conn.Close()
}
