package handshake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type Repository interface {
	LoadGroupRuntimeByClientID(ctx context.Context, clientID [16]byte) (controldomainruntime.GroupRuntime, error)
}

type FrameReader interface {
	ReadFrame(conn net.Conn) (protocol.Frame, error)
}

type FrameReaderFunc func(conn net.Conn) (protocol.Frame, error)

func (fn FrameReaderFunc) ReadFrame(conn net.Conn) (protocol.Frame, error) {
	return fn(conn)
}

type NegotiateOptions struct {
	Conn         net.Conn
	InitialFrame *protocol.Frame
	Reader       FrameReader
	Writer       controlprotocolerrors.FrameWriter
	Repository   Repository
	ReadTimeout  time.Duration
	Clock        Clock
	Certificates TLSCertificateProvider
}

func NegotiateTransport(options NegotiateOptions) (net.Conn, [16]byte, error) {
	if options.Conn == nil {
		return nil, [16]byte{}, fmt.Errorf("control connection is nil")
	}
	if options.InitialFrame == nil && options.Reader == nil {
		return nil, [16]byte{}, fmt.Errorf("transport frame reader is nil")
	}
	if options.Writer == nil {
		return nil, [16]byte{}, fmt.Errorf("transport frame writer is nil")
	}
	if options.Repository == nil {
		return nil, [16]byte{}, fmt.Errorf("transport repository is nil")
	}

	frame := protocol.Frame{}
	var err error
	if options.InitialFrame != nil {
		frame = *options.InitialFrame
	} else {
		frame, err = options.Reader.ReadFrame(options.Conn)
		if err != nil {
			return nil, [16]byte{}, controlprotocolerrors.ReplyProtocolError(options.Writer, frame, err)
		}
	}
	if frame.Type != protocol.TypeTransportClientHello {
		return nil, [16]byte{}, controlprotocolerrors.ReplyError(
			options.Writer,
			frame.RequestID,
			frame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected transport.client_hello, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return nil, [16]byte{}, controlprotocolerrors.ReplyError(options.Writer, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "transport.client_hello requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return nil, [16]byte{}, controlprotocolerrors.ReplyError(options.Writer, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "transport.client_hello streamId must be zero")
	}

	hello, err := protocol.UnmarshalTransportClientHello(frame.Body)
	if err != nil {
		return nil, [16]byte{}, controlprotocolerrors.ReplyProtocolError(options.Writer, frame, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), options.ReadTimeout)
	group, err := options.Repository.LoadGroupRuntimeByClientID(ctx, hello.ClientID)
	cancel()
	if err != nil {
		switch {
		case errors.Is(err, controlrepo.ErrGroupNotFound):
			return nil, [16]byte{}, controlprotocolerrors.ReplyError(options.Writer, frame.RequestID, 0, protocol.ErrorCodeAuthInvalidClient, "client_id not found")
		default:
			return nil, [16]byte{}, err
		}
	}
	if !group.Enabled {
		return nil, [16]byte{}, controlprotocolerrors.ReplyError(options.Writer, frame.RequestID, 0, protocol.ErrorCodeAuthGroupDisabled, "proxy group is disabled")
	}

	selectedMode, err := SelectTransportSecurityMode(group, hello.SupportedSecurityModes, options.Certificates)
	if err != nil {
		return nil, [16]byte{}, controlprotocolerrors.ReplyProtocolError(options.Writer, frame, err)
	}
	serverHelloBody, err := protocol.MarshalTransportServerHello(protocol.TransportServerHello{
		SelectedSecurityMode: selectedMode,
		CapabilityBits:       0,
	})
	if err != nil {
		return nil, [16]byte{}, err
	}
	if err := options.Writer.WriteFrame(protocol.Frame{
		Type:      protocol.TypeTransportServerHello,
		RequestID: frame.RequestID,
		Body:      serverHelloBody,
	}); err != nil {
		return nil, [16]byte{}, err
	}

	conn := options.Conn
	if selectedMode == protocol.TransportSecurityModeTLS {
		conn, err = UpgradeControlConnToTLS(options.Conn, options.Clock, options.ReadTimeout, options.Certificates)
		if err != nil {
			return nil, [16]byte{}, err
		}
	}
	return conn, hello.ClientID, nil
}

func SelectTransportSecurityMode(group controldomainruntime.GroupRuntime, supportedModes uint8, certificates TLSCertificateProvider) (uint8, error) {
	security := group.ControlTransportSecurity
	if security == "" {
		security = proxygroups.DefaultControlTransportSecurity()
	}

	switch security {
	case proxygroups.ControlTransportSecurityPlain:
		if supportedModes&protocol.TransportSecurityModePlain == 0 {
			return 0, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "client does not support plain transport")
		}
		return protocol.TransportSecurityModePlain, nil
	case proxygroups.ControlTransportSecurityTLSRequired:
		if certificates == nil {
			return 0, protocol.NewError(protocol.ErrorCodeTransportTLSUnavailable, "control listener tls certificate is unavailable")
		}
		if _, ok := certificates.CurrentCertificate(); !ok {
			return 0, protocol.NewError(protocol.ErrorCodeTransportTLSUnavailable, "control listener tls certificate is unavailable")
		}
		if supportedModes&protocol.TransportSecurityModeTLS == 0 {
			return 0, protocol.NewError(protocol.ErrorCodeTransportTLSUnsupported, "client does not support tls transport")
		}
		return protocol.TransportSecurityModeTLS, nil
	default:
		return 0, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "unsupported control transport security %q", security)
	}
}
