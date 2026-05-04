package wiring

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) serveTunnelListenerOverTCPWork(serve tunnelRuntimeServeContext, listener net.Listener) {
	for {
		publicConn, err := listener.Accept()
		if err != nil {
			if err == net.ErrClosed {
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

	workConn, ok := serve.session.AcquireTCPWorkConn()
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
	var cleanup sync.Once
	cleanupAll := func() {
		cleanup.Do(func() {
			session.ClosePublicStream(streamID)
			session.RetireTCPWorkConn(workConn)
		})
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- copyTCPRawConn(workConn, stream.Conn, stream)
	}()
	go func() {
		errCh <- copyTCPRawConn(stream.Conn, workConn, stream)
	}()

	firstErr := <-errCh
	if firstErr != nil {
		cleanupAll()
	}
	<-errCh
	cleanupAll()
}

func copyTCPRawConn(dst net.Conn, src net.Conn, stream *publicStream) error {
	_, err := io.Copy(touchConnWriter{conn: dst, stream: stream}, src)
	if closeWriter, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = closeWriter.CloseWrite()
	} else {
		_ = dst.Close()
	}
	return err
}

type touchConnWriter struct {
	conn   net.Conn
	stream *publicStream
}

func (w touchConnWriter) Write(payload []byte) (int, error) {
	n, err := w.conn.Write(payload)
	if n > 0 && w.stream != nil {
		w.stream.Touch(time.Now().UTC())
	}
	return n, err
}
