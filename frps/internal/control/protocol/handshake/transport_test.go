package handshake

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type staticTLSProvider struct {
	certificate *tls.Certificate
}

func (p staticTLSProvider) CurrentCertificate() (*tls.Certificate, bool) {
	if p.certificate == nil {
		return nil, false
	}
	return p.certificate, true
}

type staticRepository struct {
	group controldomainruntime.GroupRuntime
	err   error
}

func (r staticRepository) LoadGroupRuntimeByClientID(context.Context, [16]byte) (controldomainruntime.GroupRuntime, error) {
	return r.group, r.err
}

type staticFrameReader struct {
	frame protocol.Frame
	err   error
}

func (r staticFrameReader) ReadFrame(net.Conn) (protocol.Frame, error) {
	return r.frame, r.err
}

type recordingFrameWriter struct {
	frames []protocol.Frame
}

func (w *recordingFrameWriter) WriteFrame(frame protocol.Frame) error {
	w.frames = append(w.frames, frame)
	return nil
}

func TestSelectTransportSecurityModeTLSRequiredUnavailable(t *testing.T) {
	group := controldomainruntime.GroupRuntime{
		ControlTransportSecurity: proxygroups.ControlTransportSecurityTLSRequired,
	}

	_, err := SelectTransportSecurityMode(group, protocol.TransportSecurityModeTLS, nil)
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeTransportTLSUnavailable {
		t.Fatalf("unexpected error code: got %d want %d", code, protocol.ErrorCodeTransportTLSUnavailable)
	}
}

func TestSelectTransportSecurityModeTLSRequiredUnsupported(t *testing.T) {
	group := controldomainruntime.GroupRuntime{
		ControlTransportSecurity: proxygroups.ControlTransportSecurityTLSRequired,
	}

	_, err := SelectTransportSecurityMode(group, protocol.TransportSecurityModePlain, staticTLSProvider{
		certificate: &tls.Certificate{},
	})
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeTransportTLSUnsupported {
		t.Fatalf("unexpected error code: got %d want %d", code, protocol.ErrorCodeTransportTLSUnsupported)
	}
}

func TestSelectTransportSecurityModeTLSRequired(t *testing.T) {
	group := controldomainruntime.GroupRuntime{
		ControlTransportSecurity: proxygroups.ControlTransportSecurityTLSRequired,
	}

	mode, err := SelectTransportSecurityMode(group, protocol.TransportSecurityModePlain|protocol.TransportSecurityModeTLS, staticTLSProvider{
		certificate: &tls.Certificate{},
	})
	if err != nil {
		t.Fatalf("select transport security mode: %v", err)
	}
	if mode != protocol.TransportSecurityModeTLS {
		t.Fatalf("unexpected selected mode: got %d want %d", mode, protocol.TransportSecurityModeTLS)
	}
}

func TestNegotiateTransportTLSRequiredUnavailableWritesError(t *testing.T) {
	writer := &recordingFrameWriter{}

	_, _, err := NegotiateTransport(NegotiateOptions{
		Conn:   noopConn{},
		Reader: staticFrameReader{frame: transportClientHelloFrame(t, protocol.TransportSecurityModeTLS)},
		Writer: writer,
		Repository: staticRepository{group: controldomainruntime.GroupRuntime{
			Enabled:                  true,
			ControlTransportSecurity: proxygroups.ControlTransportSecurityTLSRequired,
		}},
	})
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeTransportTLSUnavailable {
		t.Fatalf("unexpected error code: got %d want %d", code, protocol.ErrorCodeTransportTLSUnavailable)
	}
	if code := writtenErrorCode(t, writer); code != protocol.ErrorCodeTransportTLSUnavailable {
		t.Fatalf("unexpected written error code: got %d want %d", code, protocol.ErrorCodeTransportTLSUnavailable)
	}
}

func TestNegotiateTransportTLSRequiredUnsupportedWritesError(t *testing.T) {
	writer := &recordingFrameWriter{}

	_, _, err := NegotiateTransport(NegotiateOptions{
		Conn:   noopConn{},
		Reader: staticFrameReader{frame: transportClientHelloFrame(t, protocol.TransportSecurityModePlain)},
		Writer: writer,
		Repository: staticRepository{group: controldomainruntime.GroupRuntime{
			Enabled:                  true,
			ControlTransportSecurity: proxygroups.ControlTransportSecurityTLSRequired,
		}},
		Certificates: staticTLSProvider{certificate: &tls.Certificate{}},
	})
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeTransportTLSUnsupported {
		t.Fatalf("unexpected error code: got %d want %d", code, protocol.ErrorCodeTransportTLSUnsupported)
	}
	if code := writtenErrorCode(t, writer); code != protocol.ErrorCodeTransportTLSUnsupported {
		t.Fatalf("unexpected written error code: got %d want %d", code, protocol.ErrorCodeTransportTLSUnsupported)
	}
}

func protocolErrorCode(t *testing.T, err error) uint16 {
	t.Helper()

	var protocolErr *protocol.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected protocol error, got %T %[1]v", err)
	}
	return protocolErr.Code
}

func transportClientHelloFrame(t *testing.T, supportedModes uint8) protocol.Frame {
	t.Helper()

	body, err := protocol.MarshalTransportClientHello(protocol.TransportClientHello{
		ClientID:               [16]byte{1, 2, 3, 4},
		SupportedSecurityModes: supportedModes,
	})
	if err != nil {
		t.Fatalf("marshal transport.client_hello: %v", err)
	}
	return protocol.Frame{
		Type:      protocol.TypeTransportClientHello,
		RequestID: 1,
		Body:      body,
	}
}

func writtenErrorCode(t *testing.T, writer *recordingFrameWriter) uint16 {
	t.Helper()

	if len(writer.frames) != 1 {
		t.Fatalf("expected one written frame, got %d", len(writer.frames))
	}
	frame := writer.frames[0]
	if frame.Type != protocol.TypeError {
		t.Fatalf("expected error frame, got %s", frame.Type.String())
	}
	body, err := protocol.UnmarshalErrorBody(frame.Body)
	if err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	return body.ErrorCode
}

type noopConn struct{}

func (noopConn) Read([]byte) (int, error)         { return 0, nil }
func (noopConn) Write([]byte) (int, error)        { return 0, nil }
func (noopConn) Close() error                     { return nil }
func (noopConn) LocalAddr() net.Addr              { return noopAddr("local") }
func (noopConn) RemoteAddr() net.Addr             { return noopAddr("remote") }
func (noopConn) SetDeadline(time.Time) error      { return nil }
func (noopConn) SetReadDeadline(time.Time) error  { return nil }
func (noopConn) SetWriteDeadline(time.Time) error { return nil }

type noopAddr string

func (a noopAddr) Network() string { return string(a) }
func (a noopAddr) String() string  { return string(a) }
