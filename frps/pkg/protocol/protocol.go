package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
)

const (
	Version   uint8 = 1
	HeaderLen       = 12
)

type Type uint8

const (
	TypeAuthBegin     Type = 0x01
	TypeAuthChallenge Type = 0x02
	TypeAuthFinish    Type = 0x03
	TypeServerHello   Type = 0x04
	TypeHeartbeatPing Type = 0x05
	TypeHeartbeatPong Type = 0x06
	TypeConfigPush    Type = 0x10
	TypeConfigAck     Type = 0x11
	TypeStreamOpen    Type = 0x20
	TypeStreamOpened  Type = 0x21
	TypeStreamData    Type = 0x22
	TypeStreamClose   Type = 0x23
	TypeUDPOpen       Type = 0x30
	TypeUDPData       Type = 0x31
	TypeUDPClose      Type = 0x32
	TypeEventReport   Type = 0x40
	TypeError         Type = 0x41
)

const (
	ProtocolTCP uint8 = 1
	ProtocolUDP uint8 = 2
)

const (
	MaxDataBodyLen = 64 * 1024
)

const (
	StatusOK    uint8 = 1
	StatusError uint8 = 2
)

const (
	InitiatorFRPS uint8 = 1
	InitiatorFRPC uint8 = 2
)

const (
	OSWindows uint8 = 1
	OSLinux   uint8 = 2
	OSDarwin  uint8 = 3
	OSUnknown uint8 = 255
)

const (
	ArchAMD64   uint8 = 1
	ArchARM64   uint8 = 2
	Arch386     uint8 = 3
	ArchARM     uint8 = 4
	ArchUnknown uint8 = 255
)

const (
	HostTypeIPv4 uint8 = 1
	HostTypeIPv6 uint8 = 2
	HostTypeDNS  uint8 = 3
)

const (
	TunnelFlagEnabled uint8 = 1 << 0
	TunnelFlagRange   uint8 = 1 << 1
)

const (
	ErrorCodeProtocolInvalidLength  uint16 = 1001
	ErrorCodeProtocolUnknownType    uint16 = 1002
	ErrorCodeProtocolInvalidVersion uint16 = 1003
	ErrorCodeProtocolInvalidFlags   uint16 = 1004
	ErrorCodeProtocolBadBody        uint16 = 1005
	ErrorCodeAuthInvalidToken       uint16 = 1101
	ErrorCodeAuthDeniedByIP         uint16 = 1102
	ErrorCodeAuthGroupDisabled      uint16 = 1103
	ErrorCodeAuthChallengeExpired   uint16 = 1104
	ErrorCodeAuthChallengeReplayed  uint16 = 1105
	ErrorCodeAuthVersionUnsupported uint16 = 1106
	ErrorCodeConfigApplyFailed      uint16 = 1201
	ErrorCodeStreamTunnelNotFound   uint16 = 1301
	ErrorCodeStreamLocalDialFailed  uint16 = 1302
	ErrorCodeStreamNotFound         uint16 = 1303
	ErrorCodeUDPSessionNotFound     uint16 = 1401
)

const (
	CloseReasonEOF             uint16 = 1
	CloseReasonLocalDialFailed uint16 = 2
	CloseReasonReadError       uint16 = 3
	CloseReasonWriteError      uint16 = 4
	CloseReasonAdminTerminated uint16 = 5
	CloseReasonClientOffline   uint16 = 6
	CloseReasonServerShutdown  uint16 = 7
	CloseReasonIdleTimeout     uint16 = 8
	CloseReasonProtocolError   uint16 = 9
)

type ProtocolError struct {
	Code    uint16
	Message string
}

func NewError(code uint16, format string, args ...any) *ProtocolError {
	return &ProtocolError{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
	}
}

func AsProtocolError(err error) *ProtocolError {
	var protocolErr *ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr
	}
	return nil
}

func (e *ProtocolError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("protocol error %d", e.Code)
}

type Frame struct {
	Version   uint8
	Type      Type
	Flags     uint16
	RequestID uint32
	StreamID  uint32
	Body      []byte
}

func ParseFrame(data []byte) (Frame, error) {
	if len(data) < HeaderLen {
		return Frame{}, NewError(
			ErrorCodeProtocolBadBody,
			"business frame shorter than header: %d",
			len(data),
		)
	}

	frame := Frame{
		Version:   data[0],
		Type:      Type(data[1]),
		Flags:     binary.BigEndian.Uint16(data[2:4]),
		RequestID: binary.BigEndian.Uint32(data[4:8]),
		StreamID:  binary.BigEndian.Uint32(data[8:12]),
		Body:      append([]byte(nil), data[HeaderLen:]...),
	}

	if frame.Version != Version {
		return frame, NewError(
			ErrorCodeProtocolInvalidVersion,
			"unsupported protocol version %d",
			frame.Version,
		)
	}
	if frame.Flags != 0 {
		return frame, NewError(
			ErrorCodeProtocolInvalidFlags,
			"unsupported flags %d",
			frame.Flags,
		)
	}
	if !frame.Type.Known() {
		return frame, NewError(
			ErrorCodeProtocolUnknownType,
			"unknown message type 0x%02x",
			uint8(frame.Type),
		)
	}

	return frame, nil
}

func (f Frame) MarshalBinary() ([]byte, error) {
	version := f.Version
	if version == 0 {
		version = Version
	}
	if version != Version {
		return nil, NewError(ErrorCodeProtocolInvalidVersion, "unsupported protocol version %d", version)
	}
	if f.Flags != 0 {
		return nil, NewError(ErrorCodeProtocolInvalidFlags, "unsupported flags %d", f.Flags)
	}
	if !f.Type.Known() {
		return nil, NewError(ErrorCodeProtocolUnknownType, "unknown message type 0x%02x", uint8(f.Type))
	}

	frame := make([]byte, HeaderLen+len(f.Body))
	frame[0] = version
	frame[1] = byte(f.Type)
	binary.BigEndian.PutUint16(frame[2:4], f.Flags)
	binary.BigEndian.PutUint32(frame[4:8], f.RequestID)
	binary.BigEndian.PutUint32(frame[8:12], f.StreamID)
	copy(frame[HeaderLen:], f.Body)
	return frame, nil
}

func (t Type) Known() bool {
	switch t {
	case TypeAuthBegin,
		TypeAuthChallenge,
		TypeAuthFinish,
		TypeServerHello,
		TypeHeartbeatPing,
		TypeHeartbeatPong,
		TypeConfigPush,
		TypeConfigAck,
		TypeStreamOpen,
		TypeStreamOpened,
		TypeStreamData,
		TypeStreamClose,
		TypeUDPOpen,
		TypeUDPData,
		TypeUDPClose,
		TypeEventReport,
		TypeError:
		return true
	default:
		return false
	}
}

func (t Type) String() string {
	switch t {
	case TypeAuthBegin:
		return "auth.begin"
	case TypeAuthChallenge:
		return "auth.challenge"
	case TypeAuthFinish:
		return "auth.finish"
	case TypeServerHello:
		return "server.hello"
	case TypeHeartbeatPing:
		return "heartbeat.ping"
	case TypeHeartbeatPong:
		return "heartbeat.pong"
	case TypeConfigPush:
		return "config.push"
	case TypeConfigAck:
		return "config.ack"
	case TypeStreamOpen:
		return "stream.open"
	case TypeStreamOpened:
		return "stream.opened"
	case TypeStreamData:
		return "stream.data"
	case TypeStreamClose:
		return "stream.close"
	case TypeUDPOpen:
		return "udp.open"
	case TypeUDPData:
		return "udp.data"
	case TypeUDPClose:
		return "udp.close"
	case TypeEventReport:
		return "event.report"
	case TypeError:
		return "error"
	default:
		return fmt.Sprintf("unknown(0x%02x)", uint8(t))
	}
}

type AuthBegin struct {
	TokenID        [16]byte
	ClientVersion  string
	Hostname       string
	OS             uint8
	Arch           uint8
	CapabilityBits uint32
}

type AuthChallenge struct {
	ChallengeID uint32
	Nonce       [16]byte
	ExpiresInMs uint32
}

type AuthFinish struct {
	ChallengeID uint32
	Response    [32]byte
}

type ServerHello struct {
	HeartbeatIntervalMs uint32
	SessionID           uint64
	CapabilityBits      uint32
	ServerVersion       string
	MinSupportedVersion string
}

type ConfigPush struct {
	ConfigVersion uint64
	GeneratedAtMs uint64
	Tunnels       []TunnelEntry
}

type TunnelEntry struct {
	TunnelID    uint32
	Protocol    uint8
	TunnelFlags uint8
	RemoteStart uint16
	RemoteEnd   uint16
	LocalHost   Host
	LocalStart  uint16
	LocalEnd    uint16
}

type ConfigAck struct {
	ConfigVersion uint64
	AppliedAtMs   uint64
	Status        uint8
	ErrorCode     uint16
	Message       string
}

type HeartbeatPing struct {
	ClientUnixMs           uint64
	ActiveStreams          uint32
	ActiveUDPSessions      uint32
	LastAckedConfigVersion uint64
}

type HeartbeatPong struct {
	ClientUnixMs uint64
	ServerUnixMs uint64
}

type StreamOpen struct {
	TunnelID   uint32
	RemotePort uint16
	ClientAddr SockAddr
	OpenedAtMs uint64
}

type StreamOpened struct {
	Status    uint8
	ErrorCode uint16
	Message   string
}

type StreamClose struct {
	ReasonCode uint16
	Initiator  uint8
	Message    string
}

type ErrorBody struct {
	ErrorCode uint16
	Retryable bool
	Message   string
}

type SockAddr struct {
	IP   net.IP
	Port uint16
}

type Host struct {
	Type uint8
	IP   net.IP
	Name string
}

func ParseHost(value string) (Host, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Host{}, NewError(ErrorCodeProtocolBadBody, "host is required")
	}

	if ip := net.ParseIP(value); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return Host{Type: HostTypeIPv4, IP: append(net.IP(nil), ip4...)}, nil
		}
		ip16 := ip.To16()
		if ip16 == nil {
			return Host{}, NewError(ErrorCodeProtocolBadBody, "invalid IP host %q", value)
		}
		return Host{Type: HostTypeIPv6, IP: append(net.IP(nil), ip16...)}, nil
	}

	return Host{Type: HostTypeDNS, Name: value}, nil
}

func (h Host) String() string {
	switch h.Type {
	case HostTypeIPv4, HostTypeIPv6:
		return h.IP.String()
	case HostTypeDNS:
		return h.Name
	default:
		return ""
	}
}

func MarshalAuthBegin(message AuthBegin) ([]byte, error) {
	var enc bodyEncoder
	enc.bytes(message.TokenID[:])
	if err := enc.shortstr(message.ClientVersion); err != nil {
		return nil, err
	}
	if err := enc.shortstr(message.Hostname); err != nil {
		return nil, err
	}
	enc.u8(message.OS)
	enc.u8(message.Arch)
	enc.u32(message.CapabilityBits)
	return enc.bytesValue(), nil
}

func UnmarshalAuthBegin(data []byte) (AuthBegin, error) {
	var message AuthBegin
	dec := newBodyDecoder(data)
	tokenID, err := dec.fixedBytes(16)
	if err != nil {
		return message, err
	}
	copy(message.TokenID[:], tokenID)
	if message.ClientVersion, err = dec.shortstr(); err != nil {
		return message, err
	}
	if message.Hostname, err = dec.shortstr(); err != nil {
		return message, err
	}
	if message.OS, err = dec.u8(); err != nil {
		return message, err
	}
	if message.Arch, err = dec.u8(); err != nil {
		return message, err
	}
	if message.CapabilityBits, err = dec.u32(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalAuthChallenge(message AuthChallenge) ([]byte, error) {
	var enc bodyEncoder
	enc.u32(message.ChallengeID)
	enc.bytes(message.Nonce[:])
	enc.u32(message.ExpiresInMs)
	return enc.bytesValue(), nil
}

func UnmarshalAuthChallenge(data []byte) (AuthChallenge, error) {
	var message AuthChallenge
	dec := newBodyDecoder(data)
	var err error
	if message.ChallengeID, err = dec.u32(); err != nil {
		return message, err
	}
	nonce, err := dec.fixedBytes(16)
	if err != nil {
		return message, err
	}
	copy(message.Nonce[:], nonce)
	if message.ExpiresInMs, err = dec.u32(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalAuthFinish(message AuthFinish) ([]byte, error) {
	var enc bodyEncoder
	enc.u32(message.ChallengeID)
	enc.bytes(message.Response[:])
	return enc.bytesValue(), nil
}

func UnmarshalAuthFinish(data []byte) (AuthFinish, error) {
	var message AuthFinish
	dec := newBodyDecoder(data)
	var err error
	if message.ChallengeID, err = dec.u32(); err != nil {
		return message, err
	}
	response, err := dec.fixedBytes(32)
	if err != nil {
		return message, err
	}
	copy(message.Response[:], response)
	return message, dec.done()
}

func MarshalServerHello(message ServerHello) ([]byte, error) {
	var enc bodyEncoder
	enc.u32(message.HeartbeatIntervalMs)
	enc.u64(message.SessionID)
	enc.u32(message.CapabilityBits)
	if err := enc.shortstr(message.ServerVersion); err != nil {
		return nil, err
	}
	if err := enc.shortstr(message.MinSupportedVersion); err != nil {
		return nil, err
	}
	return enc.bytesValue(), nil
}

func UnmarshalServerHello(data []byte) (ServerHello, error) {
	var message ServerHello
	dec := newBodyDecoder(data)
	var err error
	if message.HeartbeatIntervalMs, err = dec.u32(); err != nil {
		return message, err
	}
	if message.SessionID, err = dec.u64(); err != nil {
		return message, err
	}
	if message.CapabilityBits, err = dec.u32(); err != nil {
		return message, err
	}
	if message.ServerVersion, err = dec.shortstr(); err != nil {
		return message, err
	}
	if message.MinSupportedVersion, err = dec.shortstr(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalConfigPush(message ConfigPush) ([]byte, error) {
	if len(message.Tunnels) > math.MaxUint16 {
		return nil, NewError(ErrorCodeProtocolBadBody, "too many tunnels: %d", len(message.Tunnels))
	}

	var enc bodyEncoder
	enc.u64(message.ConfigVersion)
	enc.u64(message.GeneratedAtMs)
	enc.u16(uint16(len(message.Tunnels)))
	for _, tunnel := range message.Tunnels {
		if tunnel.TunnelID == 0 {
			return nil, NewError(ErrorCodeProtocolBadBody, "tunnel id must be non-zero")
		}
		if err := encodeTunnelEntry(&enc, tunnel); err != nil {
			return nil, err
		}
	}
	return enc.bytesValue(), nil
}

func UnmarshalConfigPush(data []byte) (ConfigPush, error) {
	var message ConfigPush
	dec := newBodyDecoder(data)
	var err error
	if message.ConfigVersion, err = dec.u64(); err != nil {
		return message, err
	}
	if message.GeneratedAtMs, err = dec.u64(); err != nil {
		return message, err
	}
	count, err := dec.u16()
	if err != nil {
		return message, err
	}
	message.Tunnels = make([]TunnelEntry, 0, count)
	for range int(count) {
		tunnel, err := decodeTunnelEntry(dec)
		if err != nil {
			return message, err
		}
		message.Tunnels = append(message.Tunnels, tunnel)
	}
	return message, dec.done()
}

func MarshalConfigAck(message ConfigAck) ([]byte, error) {
	var enc bodyEncoder
	enc.u64(message.ConfigVersion)
	enc.u64(message.AppliedAtMs)
	enc.u8(message.Status)
	enc.u16(message.ErrorCode)
	if err := enc.shortstr(message.Message); err != nil {
		return nil, err
	}
	return enc.bytesValue(), nil
}

func UnmarshalConfigAck(data []byte) (ConfigAck, error) {
	var message ConfigAck
	dec := newBodyDecoder(data)
	var err error
	if message.ConfigVersion, err = dec.u64(); err != nil {
		return message, err
	}
	if message.AppliedAtMs, err = dec.u64(); err != nil {
		return message, err
	}
	if message.Status, err = dec.u8(); err != nil {
		return message, err
	}
	if message.ErrorCode, err = dec.u16(); err != nil {
		return message, err
	}
	if message.Message, err = dec.shortstr(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalHeartbeatPing(message HeartbeatPing) ([]byte, error) {
	var enc bodyEncoder
	enc.u64(message.ClientUnixMs)
	enc.u32(message.ActiveStreams)
	enc.u32(message.ActiveUDPSessions)
	enc.u64(message.LastAckedConfigVersion)
	return enc.bytesValue(), nil
}

func UnmarshalHeartbeatPing(data []byte) (HeartbeatPing, error) {
	var message HeartbeatPing
	dec := newBodyDecoder(data)
	var err error
	if message.ClientUnixMs, err = dec.u64(); err != nil {
		return message, err
	}
	if message.ActiveStreams, err = dec.u32(); err != nil {
		return message, err
	}
	if message.ActiveUDPSessions, err = dec.u32(); err != nil {
		return message, err
	}
	if message.LastAckedConfigVersion, err = dec.u64(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalHeartbeatPong(message HeartbeatPong) ([]byte, error) {
	var enc bodyEncoder
	enc.u64(message.ClientUnixMs)
	enc.u64(message.ServerUnixMs)
	return enc.bytesValue(), nil
}

func UnmarshalHeartbeatPong(data []byte) (HeartbeatPong, error) {
	var message HeartbeatPong
	dec := newBodyDecoder(data)
	var err error
	if message.ClientUnixMs, err = dec.u64(); err != nil {
		return message, err
	}
	if message.ServerUnixMs, err = dec.u64(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalStreamOpen(message StreamOpen) ([]byte, error) {
	var enc bodyEncoder
	enc.u32(message.TunnelID)
	enc.u16(message.RemotePort)
	if err := encodeSockAddr(&enc, message.ClientAddr); err != nil {
		return nil, err
	}
	enc.u64(message.OpenedAtMs)
	return enc.bytesValue(), nil
}

func UnmarshalStreamOpen(data []byte) (StreamOpen, error) {
	var message StreamOpen
	dec := newBodyDecoder(data)
	var err error
	if message.TunnelID, err = dec.u32(); err != nil {
		return message, err
	}
	if message.RemotePort, err = dec.u16(); err != nil {
		return message, err
	}
	if message.ClientAddr, err = decodeSockAddr(dec); err != nil {
		return message, err
	}
	if message.OpenedAtMs, err = dec.u64(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalStreamOpened(message StreamOpened) ([]byte, error) {
	var enc bodyEncoder
	enc.u8(message.Status)
	enc.u16(message.ErrorCode)
	if err := enc.shortstr(message.Message); err != nil {
		return nil, err
	}
	return enc.bytesValue(), nil
}

func UnmarshalStreamOpened(data []byte) (StreamOpened, error) {
	var message StreamOpened
	dec := newBodyDecoder(data)
	var err error
	if message.Status, err = dec.u8(); err != nil {
		return message, err
	}
	if message.ErrorCode, err = dec.u16(); err != nil {
		return message, err
	}
	if message.Message, err = dec.shortstr(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalStreamClose(message StreamClose) ([]byte, error) {
	var enc bodyEncoder
	enc.u16(message.ReasonCode)
	enc.u8(message.Initiator)
	if err := enc.shortstr(message.Message); err != nil {
		return nil, err
	}
	return enc.bytesValue(), nil
}

func UnmarshalStreamClose(data []byte) (StreamClose, error) {
	var message StreamClose
	dec := newBodyDecoder(data)
	var err error
	if message.ReasonCode, err = dec.u16(); err != nil {
		return message, err
	}
	if message.Initiator, err = dec.u8(); err != nil {
		return message, err
	}
	if message.Message, err = dec.shortstr(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func MarshalErrorBody(message ErrorBody) ([]byte, error) {
	var enc bodyEncoder
	enc.u16(message.ErrorCode)
	enc.bool(message.Retryable)
	if err := enc.shortstr(message.Message); err != nil {
		return nil, err
	}
	return enc.bytesValue(), nil
}

func UnmarshalErrorBody(data []byte) (ErrorBody, error) {
	var message ErrorBody
	dec := newBodyDecoder(data)
	var err error
	if message.ErrorCode, err = dec.u16(); err != nil {
		return message, err
	}
	if message.Retryable, err = dec.bool(); err != nil {
		return message, err
	}
	if message.Message, err = dec.shortstr(); err != nil {
		return message, err
	}
	return message, dec.done()
}

func encodeTunnelEntry(enc *bodyEncoder, tunnel TunnelEntry) error {
	enc.u32(tunnel.TunnelID)
	enc.u8(tunnel.Protocol)
	enc.u8(tunnel.TunnelFlags)
	enc.u16(0)
	enc.u16(tunnel.RemoteStart)
	enc.u16(tunnel.RemoteEnd)
	if err := encodeHost(enc, tunnel.LocalHost); err != nil {
		return err
	}
	enc.u16(tunnel.LocalStart)
	enc.u16(tunnel.LocalEnd)
	return nil
}

func decodeTunnelEntry(dec *bodyDecoder) (TunnelEntry, error) {
	var tunnel TunnelEntry
	var err error
	if tunnel.TunnelID, err = dec.u32(); err != nil {
		return tunnel, err
	}
	if tunnel.Protocol, err = dec.u8(); err != nil {
		return tunnel, err
	}
	if tunnel.TunnelFlags, err = dec.u8(); err != nil {
		return tunnel, err
	}
	reserved, err := dec.u16()
	if err != nil {
		return tunnel, err
	}
	if reserved != 0 {
		return tunnel, NewError(ErrorCodeProtocolBadBody, "tunnel reserved field must be zero")
	}
	if tunnel.RemoteStart, err = dec.u16(); err != nil {
		return tunnel, err
	}
	if tunnel.RemoteEnd, err = dec.u16(); err != nil {
		return tunnel, err
	}
	if tunnel.LocalHost, err = decodeHost(dec); err != nil {
		return tunnel, err
	}
	if tunnel.LocalStart, err = dec.u16(); err != nil {
		return tunnel, err
	}
	if tunnel.LocalEnd, err = dec.u16(); err != nil {
		return tunnel, err
	}
	return tunnel, nil
}

func encodeHost(enc *bodyEncoder, host Host) error {
	enc.u8(host.Type)
	switch host.Type {
	case HostTypeIPv4:
		ip4 := host.IP.To4()
		if ip4 == nil {
			return NewError(ErrorCodeProtocolBadBody, "invalid IPv4 host")
		}
		enc.bytes(ip4)
	case HostTypeIPv6:
		ip16 := host.IP.To16()
		if ip16 == nil || host.IP.To4() != nil {
			return NewError(ErrorCodeProtocolBadBody, "invalid IPv6 host")
		}
		enc.bytes(ip16)
	case HostTypeDNS:
		if err := enc.shortstr(host.Name); err != nil {
			return err
		}
	default:
		return NewError(ErrorCodeProtocolBadBody, "unknown host type %d", host.Type)
	}
	return nil
}

func decodeHost(dec *bodyDecoder) (Host, error) {
	hostType, err := dec.u8()
	if err != nil {
		return Host{}, err
	}

	switch hostType {
	case HostTypeIPv4:
		ip, err := dec.fixedBytes(4)
		if err != nil {
			return Host{}, err
		}
		return Host{Type: HostTypeIPv4, IP: append(net.IP(nil), ip...)}, nil
	case HostTypeIPv6:
		ip, err := dec.fixedBytes(16)
		if err != nil {
			return Host{}, err
		}
		return Host{Type: HostTypeIPv6, IP: append(net.IP(nil), ip...)}, nil
	case HostTypeDNS:
		name, err := dec.shortstr()
		if err != nil {
			return Host{}, err
		}
		return Host{Type: HostTypeDNS, Name: name}, nil
	default:
		return Host{}, NewError(ErrorCodeProtocolBadBody, "unknown host type %d", hostType)
	}
}

func encodeSockAddr(enc *bodyEncoder, addr SockAddr) error {
	ip4 := addr.IP.To4()
	if ip4 != nil {
		enc.u8(HostTypeIPv4)
		enc.bytes(ip4)
		enc.u16(addr.Port)
		return nil
	}

	ip16 := addr.IP.To16()
	if ip16 == nil {
		return NewError(ErrorCodeProtocolBadBody, "invalid socket IP")
	}
	enc.u8(HostTypeIPv6)
	enc.bytes(ip16)
	enc.u16(addr.Port)
	return nil
}

func decodeSockAddr(dec *bodyDecoder) (SockAddr, error) {
	hostType, err := dec.u8()
	if err != nil {
		return SockAddr{}, err
	}

	var ip net.IP
	switch hostType {
	case HostTypeIPv4:
		raw, err := dec.fixedBytes(4)
		if err != nil {
			return SockAddr{}, err
		}
		ip = append(net.IP(nil), raw...)
	case HostTypeIPv6:
		raw, err := dec.fixedBytes(16)
		if err != nil {
			return SockAddr{}, err
		}
		ip = append(net.IP(nil), raw...)
	default:
		return SockAddr{}, NewError(ErrorCodeProtocolBadBody, "unknown socket address type %d", hostType)
	}

	port, err := dec.u16()
	if err != nil {
		return SockAddr{}, err
	}
	return SockAddr{
		IP:   ip,
		Port: port,
	}, nil
}

type bodyEncoder struct {
	buf bytes.Buffer
}

func (e *bodyEncoder) bytesValue() []byte {
	return e.buf.Bytes()
}

func (e *bodyEncoder) bytes(value []byte) {
	_, _ = e.buf.Write(value)
}

func (e *bodyEncoder) u8(value uint8) {
	_ = e.buf.WriteByte(value)
}

func (e *bodyEncoder) bool(value bool) {
	if value {
		e.u8(1)
		return
	}
	e.u8(0)
}

func (e *bodyEncoder) u16(value uint16) {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], value)
	e.bytes(buf[:])
}

func (e *bodyEncoder) u32(value uint32) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], value)
	e.bytes(buf[:])
}

func (e *bodyEncoder) u64(value uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	e.bytes(buf[:])
}

func (e *bodyEncoder) shortstr(value string) error {
	if len(value) > math.MaxUint16 {
		return NewError(ErrorCodeProtocolBadBody, "shortstr too long: %d", len(value))
	}
	e.u16(uint16(len(value)))
	e.bytes([]byte(value))
	return nil
}

type bodyDecoder struct {
	data   []byte
	offset int
}

func newBodyDecoder(data []byte) *bodyDecoder {
	return &bodyDecoder{data: data}
}

func (d *bodyDecoder) done() error {
	if d.offset != len(d.data) {
		return NewError(
			ErrorCodeProtocolBadBody,
			"unexpected trailing body bytes: %d",
			len(d.data)-d.offset,
		)
	}
	return nil
}

func (d *bodyDecoder) fixedBytes(length int) ([]byte, error) {
	if length < 0 {
		return nil, NewError(ErrorCodeProtocolBadBody, "invalid byte length %d", length)
	}
	if len(d.data)-d.offset < length {
		return nil, NewError(ErrorCodeProtocolBadBody, "body truncated")
	}
	value := d.data[d.offset : d.offset+length]
	d.offset += length
	return value, nil
}

func (d *bodyDecoder) u8() (uint8, error) {
	value, err := d.fixedBytes(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (d *bodyDecoder) bool() (bool, error) {
	value, err := d.u8()
	if err != nil {
		return false, err
	}
	switch value {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, NewError(ErrorCodeProtocolBadBody, "invalid bool value %d", value)
	}
}

func (d *bodyDecoder) u16() (uint16, error) {
	value, err := d.fixedBytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(value), nil
}

func (d *bodyDecoder) u32() (uint32, error) {
	value, err := d.fixedBytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(value), nil
}

func (d *bodyDecoder) u64() (uint64, error) {
	value, err := d.fixedBytes(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(value), nil
}

func (d *bodyDecoder) shortstr() (string, error) {
	length, err := d.u16()
	if err != nil {
		return "", err
	}
	value, err := d.fixedBytes(int(length))
	if err != nil {
		return "", err
	}
	return string(value), nil
}
