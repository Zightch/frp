package wiring

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/ratepolicy"
)

const maxTCPWorkAcquireWait = 200 * time.Millisecond

func (s *Server) serveTunnelListenerOverTCPWork(serve tunnelRuntimeServeContext, listener net.Listener) {
	for {
		publicConn, err := listener.Accept()
		if err != nil {
			if isTCPListenerClosed(err) {
				return
			}
			if serve.logger != nil {
				serve.logger.Warn("tcp tunnel accept failed", "tunnel_id", serve.tunnel.TunnelID, "error", err)
			}
			continue
		}

		go s.handlePublicTCPWorkConnection(serve, publicConn)
	}
}

func isTCPListenerClosed(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

func (s *Server) handlePublicTCPWorkConnection(serve tunnelRuntimeServeContext, publicConn net.Conn) {
	openOp, err := serve.session.preparePublicStreamOpen(serve.runtimeIO.configVersion, serve.tunnel, serve.remotePort, publicConn, s.clock.Now().UTC())
	if err != nil {
		_ = publicConn.Close()
		return
	}
	if openOp.blocked {
		_ = publicConn.Close()
		return
	}

	workConn, ok := serve.session.AcquireTCPWorkConnWait(s.tcpWorkAcquireWaitTimeout())
	if !ok || workConn == nil {
		serve.session.ClosePublicStream(openOp.streamID)
		return
	}

	if err := s.openTCPWorkStream(workConn, serve.session, openOp); err != nil {
		if serve.logger != nil {
			serve.logger.Warn("tcp work stream open failed", "stream_id", openOp.streamID, "tunnel_id", serve.tunnel.TunnelID, "error", err)
		}
		serve.session.ClosePublicStream(openOp.streamID)
		serve.session.RetireTCPWorkConn(workConn)
		return
	}

	go s.relayPublicTCPOverWorkConn(serve.session, openOp.streamID, openOp.stream, workConn)
}

func (s *Server) openTCPWorkStream(workConn net.Conn, session *sessionState, openOp sessionStreamOpenOperation) error {
	if err := s.writeFrameWithSession(workConn, session, openOp.openFrame); err != nil {
		return err
	}

	frame, err := s.readFrameWithSessionTimeout(workConn, session, s.options.WriteTimeout)
	if err != nil {
		return err
	}
	if frame.Type == protocol.TypeError {
		errorBody, unmarshalErr := protocol.UnmarshalErrorBody(frame.Body)
		if unmarshalErr != nil {
			return unmarshalErr
		}
		return fmt.Errorf("%s", errorBody.Message)
	}
	if err := s.handleStreamOpened(workConn, session, frame); err != nil {
		return err
	}

	select {
	case openErr := <-openOp.stream.Ready:
		return openErr
	case <-time.After(s.options.WriteTimeout):
		return fmt.Errorf("stream open timeout")
	}
}

func (s *Server) relayPublicTCPOverWorkConn(session *sessionState, streamID uint32, stream *publicStream, workConn net.Conn) {
	limitCtx, limiters := streamRateLimitForTCPWork(session, streamID)

	var cleanup sync.Once
	cleanupAll := func() {
		cleanup.Do(func() {
			session.ClosePublicStream(streamID)
			session.RetireTCPWorkConn(workConn)
		})
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- copyTCPRawConn(workConn, stream.Conn, limitCtx, limiters.Downlink, stream)
	}()
	go func() {
		errCh <- copyTCPRawConn(stream.Conn, workConn, limitCtx, limiters.Uplink, stream)
	}()

	firstErr := <-errCh
	if firstErr != nil {
		cleanupAll()
	}
	<-errCh
	cleanupAll()
}

func streamRateLimitForTCPWork(session *sessionState, streamID uint32) (context.Context, ratepolicy.TunnelLimiters) {
	if session == nil {
		return context.Background(), ratepolicy.TunnelLimiters{}
	}

	limitCtx, limiters, ok := session.StreamRateLimit(streamID)
	if !ok || limitCtx == nil {
		return context.Background(), ratepolicy.TunnelLimiters{}
	}
	return limitCtx, limiters
}

func copyTCPRawConn(dst net.Conn, src net.Conn, limitCtx context.Context, limiter ratepolicy.Limiter, stream *publicStream) error {
	if limiter == nil {
		return copyTCPRawConnFast(dst, src, stream)
	}

	if limitCtx == nil {
		limitCtx = context.Background()
	}

	buffer := make([]byte, ratepolicy.ChunkSize(limiter, protocol.MaxDataBodyLen))
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			payload := buffer[:n]
			writeErr := ratepolicy.WritePayload(limitCtx, limiter, protocol.MaxDataBodyLen, payload, func(chunk []byte) error {
				return writeConnFullTouch(dst, chunk, stream)
			})
			if writeErr != nil {
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

func copyTCPRawConnFast(dst net.Conn, src net.Conn, stream *publicStream) error {
	written, err := copyTCPFast(dst, src)
	if written > 0 && stream != nil {
		stream.Touch(time.Now().UTC())
	}
	if err != nil {
		return err
	}

	closeErr := closeTCPWrite(dst)
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		return closeErr
	}
	return nil
}

func copyTCPFast(dst net.Conn, src net.Conn) (int64, error) {
	if readerFrom, ok := dst.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(dst, src)
}

func writeConnFullTouch(conn net.Conn, payload []byte, stream *publicStream) error {
	for len(payload) > 0 {
		n, err := conn.Write(payload)
		if n > 0 && stream != nil {
			stream.Touch(time.Now().UTC())
		}
		if err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

func closeTCPWrite(conn net.Conn) error {
	if closeWriter, ok := conn.(interface{ CloseWrite() error }); ok {
		return closeWriter.CloseWrite()
	}
	return conn.Close()
}

func (s *Server) tcpWorkAcquireWaitTimeout() time.Duration {
	timeout := s.options.WriteTimeout
	if timeout <= 0 || timeout > maxTCPWorkAcquireWait {
		return maxTCPWorkAcquireWait
	}
	return timeout
}
