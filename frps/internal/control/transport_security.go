package control

import (
	"errors"
	"net"

	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) negotiateTransport(conn net.Conn) (net.Conn, [16]byte, error) {
	frame, err := s.readFrame(conn)
	if err != nil {
		return nil, [16]byte{}, s.replyProtocolError(conn, frame, err)
	}
	if frame.Type != protocol.TypeTransportClientHello {
		return nil, [16]byte{}, s.replyError(
			conn,
			frame.RequestID,
			frame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected transport.client_hello, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return nil, [16]byte{}, s.replyError(conn, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "transport.client_hello requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return nil, [16]byte{}, s.replyError(conn, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "transport.client_hello streamId must be zero")
	}

	hello, err := protocol.UnmarshalTransportClientHello(frame.Body)
	if err != nil {
		return nil, [16]byte{}, s.replyProtocolError(conn, frame, err)
	}

	group, err := s.loadGroupRuntimeByClientID(hello.ClientID)
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			return nil, [16]byte{}, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthInvalidClient, "client_id not found")
		default:
			return nil, [16]byte{}, err
		}
	}
	if !group.Enabled {
		return nil, [16]byte{}, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthGroupDisabled, "proxy group is disabled")
	}

	selectedMode, err := s.selectTransportSecurityMode(group, hello.SupportedSecurityModes)
	if err != nil {
		return nil, [16]byte{}, s.replyProtocolError(conn, frame, err)
	}
	serverHelloBody, err := protocol.MarshalTransportServerHello(protocol.TransportServerHello{
		SelectedSecurityMode: selectedMode,
		CapabilityBits:       0,
	})
	if err != nil {
		return nil, [16]byte{}, err
	}
	if err := s.writeFrame(conn, protocol.Frame{
		Type:      protocol.TypeTransportServerHello,
		RequestID: frame.RequestID,
		Body:      serverHelloBody,
	}); err != nil {
		return nil, [16]byte{}, err
	}

	if selectedMode == protocol.TransportSecurityModeTLS {
		conn, err = s.upgradeControlConnToTLS(conn)
		if err != nil {
			return nil, [16]byte{}, err
		}
	}
	return conn, hello.ClientID, nil
}

func (s *Server) selectTransportSecurityMode(group GroupRuntime, supportedModes uint8) (uint8, error) {
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
		if _, ok := s.currentControlTLSCertificate(); !ok {
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
