package control

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

func TestServerAuthenticateAndHeartbeat(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:               1,
					Name:             "group-a",
					Enabled:          true,
					ClientAccessMode: "disabled",
					TokenHash:        tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    7,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: 0,
								RemoteStart: 20000,
								RemoteEnd:   20000,
								LocalHost:   host,
								LocalStart:  22,
								LocalEnd:    22,
							},
						},
					},
				},
			},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      5 * time.Second,
			HeartbeatInterval: 2 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientRaw, serverRaw := net.Pipe()
	clientConn := &connWithRemoteAddr{
		Conn:   clientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001},
	}
	serverConn := &connWithRemoteAddr{
		Conn:   serverRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20001},
	}
	defer clientConn.Close()

	done := make(chan struct{})
	server.registerConn(serverConn)
	server.connWG.Add(1)
	go func() {
		defer close(done)
		server.handleConnection(serverConn)
	}()

	authBeginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		TokenID:       tokenID,
		ClientVersion: "test-client",
		Hostname:      "node-1",
		OS:            protocol.OSLinux,
		Arch:          protocol.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("marshal auth.begin: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      authBeginBody,
	})

	challengeFrame := readMessage(t, clientConn)
	if challengeFrame.Type != protocol.TypeAuthChallenge {
		t.Fatalf("expected auth.challenge, got %s", challengeFrame.Type.String())
	}
	challenge, err := protocol.UnmarshalAuthChallenge(challengeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.challenge: %v", err)
	}

	response := challengeResponse(tokenHash, challenge.Nonce)
	authFinishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    response,
	})
	if err != nil {
		t.Fatalf("marshal auth.finish: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 2,
		Body:      authFinishBody,
	})

	helloFrame := readMessage(t, clientConn)
	if helloFrame.Type != protocol.TypeServerHello || helloFrame.RequestID != 2 {
		t.Fatalf("unexpected server.hello frame: %#v", helloFrame)
	}

	configFrame := readMessage(t, clientConn)
	if configFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected config.push, got %s", configFrame.Type.String())
	}
	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}
	if configPush.ConfigVersion != 99 {
		t.Fatalf("unexpected config version: %d", configPush.ConfigVersion)
	}

	configAckBody, err := protocol.MarshalConfigAck(protocol.ConfigAck{
		ConfigVersion: configPush.ConfigVersion,
		AppliedAtMs:   uint64(time.Now().UTC().UnixMilli()),
		Status:        protocol.StatusOK,
	})
	if err != nil {
		t.Fatalf("marshal config.ack: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: configFrame.RequestID,
		Body:      configAckBody,
	})

	heartbeatBody, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{
		ClientUnixMs:           123,
		ActiveStreams:          0,
		ActiveUDPSessions:      0,
		LastAckedConfigVersion: configPush.ConfigVersion,
	})
	if err != nil {
		t.Fatalf("marshal heartbeat.ping: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeHeartbeatPing,
		RequestID: 3,
		Body:      heartbeatBody,
	})

	pongFrame := readMessage(t, clientConn)
	if pongFrame.Type != protocol.TypeHeartbeatPong || pongFrame.RequestID != 3 {
		t.Fatalf("unexpected heartbeat.pong frame: %#v", pongFrame)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerForwardsTCPStream(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeTCPPort(t)
	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:               1,
					Name:             "group-a",
					Enabled:          true,
					ClientAccessMode: "disabled",
					TokenHash:        tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    7,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: uint16(remotePort),
								RemoteEnd:   uint16(remotePort),
								LocalHost:   host,
								LocalStart:  22,
								LocalEnd:    22,
							},
						},
					},
				},
			},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      5 * time.Second,
			HeartbeatInterval: 2 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientRaw, serverRaw := net.Pipe()
	clientConn := &connWithRemoteAddr{
		Conn:   clientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001},
	}
	serverConn := &connWithRemoteAddr{
		Conn:   serverRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20001},
	}
	defer clientConn.Close()

	done := make(chan struct{})
	server.registerConn(serverConn)
	server.connWG.Add(1)
	go func() {
		defer close(done)
		server.handleConnection(serverConn)
	}()

	authBeginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		TokenID:       tokenID,
		ClientVersion: "test-client",
		Hostname:      "node-1",
		OS:            protocol.OSLinux,
		Arch:          protocol.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("marshal auth.begin: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      authBeginBody,
	})

	challengeFrame := readMessage(t, clientConn)
	challenge, err := protocol.UnmarshalAuthChallenge(challengeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.challenge: %v", err)
	}

	authFinishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    challengeResponse(tokenHash, challenge.Nonce),
	})
	if err != nil {
		t.Fatalf("marshal auth.finish: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 2,
		Body:      authFinishBody,
	})

	helloFrame := readMessage(t, clientConn)
	if helloFrame.Type != protocol.TypeServerHello {
		t.Fatalf("expected server.hello, got %s", helloFrame.Type.String())
	}

	configFrame := readMessage(t, clientConn)
	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}

	configAckBody, err := protocol.MarshalConfigAck(protocol.ConfigAck{
		ConfigVersion: configPush.ConfigVersion,
		AppliedAtMs:   uint64(time.Now().UTC().UnixMilli()),
		Status:        protocol.StatusOK,
	})
	if err != nil {
		t.Fatalf("marshal config.ack: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: configFrame.RequestID,
		Body:      configAckBody,
	})

	publicConn := waitForTCPDial(t, remotePort)
	defer publicConn.Close()

	streamOpenFrame := readMessage(t, clientConn)
	if streamOpenFrame.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected stream.open, got %s", streamOpenFrame.Type.String())
	}
	streamOpen, err := protocol.UnmarshalStreamOpen(streamOpenFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal stream.open: %v", err)
	}
	if streamOpen.TunnelID != 7 {
		t.Fatalf("unexpected tunnel id: %d", streamOpen.TunnelID)
	}

	streamOpenedBody, err := protocol.MarshalStreamOpened(protocol.StreamOpened{Status: protocol.StatusOK})
	if err != nil {
		t.Fatalf("marshal stream.opened: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: streamOpenFrame.RequestID,
		StreamID:  streamOpenFrame.StreamID,
		Body:      streamOpenedBody,
	})

	if _, err := publicConn.Write([]byte("hello")); err != nil {
		t.Fatalf("write public conn: %v", err)
	}

	streamDataFrame := readMessage(t, clientConn)
	if streamDataFrame.Type != protocol.TypeStreamData {
		t.Fatalf("expected stream.data, got %s", streamDataFrame.Type.String())
	}
	if string(streamDataFrame.Body) != "hello" {
		t.Fatalf("unexpected forwarded payload: %q", string(streamDataFrame.Body))
	}

	writeMessage(t, clientConn, protocol.Frame{
		Type:     protocol.TypeStreamData,
		StreamID: streamDataFrame.StreamID,
		Body:     []byte("world"),
	})

	var response [5]byte
	if _, err := io.ReadFull(publicConn, response[:]); err != nil {
		t.Fatalf("read public conn: %v", err)
	}
	if string(response[:]) != "world" {
		t.Fatalf("unexpected public response: %q", string(response[:]))
	}

	_ = publicConn.Close()
	streamCloseFrame := readMessage(t, clientConn)
	if streamCloseFrame.Type != protocol.TypeStreamClose {
		t.Fatalf("expected stream.close, got %s", streamCloseFrame.Type.String())
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerStartsTunnelListenerOnlyAfterConfigAckAndStopsOnShutdown(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeTCPPort(t)
	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:               1,
					Name:             "group-a",
					Enabled:          true,
					ClientAccessMode: "disabled",
					TokenHash:        tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    7,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: uint16(remotePort),
								RemoteEnd:   uint16(remotePort),
								LocalHost:   host,
								LocalStart:  22,
								LocalEnd:    22,
							},
						},
					},
				},
			},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      5 * time.Second,
			HeartbeatInterval: 2 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}

	assertTCPDialFails(t, remotePort)

	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	publicConn := waitForTCPDial(t, remotePort)
	streamOpenFrame := readMessage(t, clientConn)
	if streamOpenFrame.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected stream.open, got %s", streamOpenFrame.Type.String())
	}

	streamOpenedBody, err := protocol.MarshalStreamOpened(protocol.StreamOpened{
		Status:    protocol.StatusError,
		ErrorCode: protocol.ErrorCodeStreamLocalDialFailed,
		Message:   "reject for test cleanup",
	})
	if err != nil {
		t.Fatalf("marshal stream.opened: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: streamOpenFrame.RequestID,
		StreamID:  streamOpenFrame.StreamID,
		Body:      streamOpenedBody,
	})
	_ = publicConn.Close()

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}

	assertTCPDialFails(t, remotePort)
}

func TestServerStartsListenersOnlyForEnabledSinglePortTCPTunnels(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	allowedPort := freeTCPPort(t)
	disabledPort := freeTCPPort(t)
	rangePort := freeTCPPort(t)
	udpPort := freeTCPPort(t)

	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:               1,
					Name:             "group-a",
					Enabled:          true,
					ClientAccessMode: "disabled",
					TokenHash:        tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    7,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: uint16(allowedPort),
								RemoteEnd:   uint16(allowedPort),
								LocalHost:   host,
								LocalStart:  22,
								LocalEnd:    22,
							},
							{
								TunnelID:    8,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: 0,
								RemoteStart: uint16(disabledPort),
								RemoteEnd:   uint16(disabledPort),
								LocalHost:   host,
								LocalStart:  23,
								LocalEnd:    23,
							},
							{
								TunnelID:    9,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: protocol.TunnelFlagEnabled | protocol.TunnelFlagRange,
								RemoteStart: uint16(rangePort),
								RemoteEnd:   uint16(rangePort + 1),
								LocalHost:   host,
								LocalStart:  24,
								LocalEnd:    25,
							},
							{
								TunnelID:    10,
								Protocol:    protocol.ProtocolUDP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: uint16(udpPort),
								RemoteEnd:   uint16(udpPort),
								LocalHost:   host,
								LocalStart:  26,
								LocalEnd:    26,
							},
						},
					},
				},
			},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      5 * time.Second,
			HeartbeatInterval: 2 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	assertTCPDialFails(t, disabledPort)
	assertTCPDialFails(t, rangePort)
	assertTCPDialFails(t, udpPort)

	publicConn := waitForTCPDial(t, allowedPort)
	streamOpenFrame := readMessage(t, clientConn)
	if streamOpenFrame.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected stream.open, got %s", streamOpenFrame.Type.String())
	}

	streamOpenedBody, err := protocol.MarshalStreamOpened(protocol.StreamOpened{
		Status:    protocol.StatusError,
		ErrorCode: protocol.ErrorCodeStreamLocalDialFailed,
		Message:   "reject for test cleanup",
	})
	if err != nil {
		t.Fatalf("marshal stream.opened: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: streamOpenFrame.RequestID,
		StreamID:  streamOpenFrame.StreamID,
		Body:      streamOpenedBody,
	})
	_ = publicConn.Close()

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerRejectsInvalidToken(t *testing.T) {
	server := NewServer(
		Options{
			Repository:   stubRepository{err: ErrGroupNotFound},
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientRaw, serverRaw := net.Pipe()
	clientConn := &connWithRemoteAddr{
		Conn:   clientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001},
	}
	serverConn := &connWithRemoteAddr{
		Conn:   serverRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20001},
	}
	defer clientConn.Close()

	done := make(chan struct{})
	server.registerConn(serverConn)
	server.connWG.Add(1)
	go func() {
		defer close(done)
		server.handleConnection(serverConn)
	}()

	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	authBeginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{TokenID: tokenID})
	if err != nil {
		t.Fatalf("marshal auth.begin: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      authBeginBody,
	})

	errorFrame := readMessage(t, clientConn)
	if errorFrame.Type != protocol.TypeError {
		t.Fatalf("expected error frame, got %s", errorFrame.Type.String())
	}
	errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if errorBody.ErrorCode != protocol.ErrorCodeAuthInvalidToken {
		t.Fatalf("unexpected error code: %d", errorBody.ErrorCode)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

type stubRepository struct {
	group GroupRuntime
	err   error
}

func (r stubRepository) LoadGroupRuntime(_ context.Context, _ [16]byte) (GroupRuntime, error) {
	if r.err != nil {
		return GroupRuntime{}, r.err
	}
	return r.group, nil
}

type connWithRemoteAddr struct {
	net.Conn
	remote net.Addr
}

func (c *connWithRemoteAddr) RemoteAddr() net.Addr {
	return c.remote
}

func writeMessage(t *testing.T, conn net.Conn, frame protocol.Frame) {
	t.Helper()

	frameBytes, err := frame.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	if err := transport.WriteFrame(conn, frameBytes, time.Second); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func readMessage(t *testing.T, conn net.Conn) protocol.Frame {
	t.Helper()

	frameBytes, err := transport.ReadFrame(conn, time.Second)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	frame, err := protocol.ParseFrame(frameBytes)
	if err != nil {
		t.Fatalf("parse frame: %v", err)
	}
	return frame
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForTCPDial(t *testing.T, port int) net.Conn {
	t.Helper()

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", address, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertTCPDialFails(t *testing.T, port int) {
	t.Helper()

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected dial %s to fail", address)
	}
}

func authenticateServerSession(t *testing.T, server *Server, tokenID [16]byte, tokenHash [32]byte) (*connWithRemoteAddr, chan struct{}, protocol.Frame) {
	t.Helper()

	clientRaw, serverRaw := net.Pipe()
	clientConn := &connWithRemoteAddr{
		Conn:   clientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001},
	}
	serverConn := &connWithRemoteAddr{
		Conn:   serverRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20001},
	}

	done := make(chan struct{})
	server.registerConn(serverConn)
	server.connWG.Add(1)
	go func() {
		defer close(done)
		server.handleConnection(serverConn)
	}()

	authBeginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		TokenID:       tokenID,
		ClientVersion: "test-client",
		Hostname:      "node-1",
		OS:            protocol.OSLinux,
		Arch:          protocol.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("marshal auth.begin: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      authBeginBody,
	})

	challengeFrame := readMessage(t, clientConn)
	if challengeFrame.Type != protocol.TypeAuthChallenge {
		t.Fatalf("expected auth.challenge, got %s", challengeFrame.Type.String())
	}
	challenge, err := protocol.UnmarshalAuthChallenge(challengeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.challenge: %v", err)
	}

	authFinishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    challengeResponse(tokenHash, challenge.Nonce),
	})
	if err != nil {
		t.Fatalf("marshal auth.finish: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 2,
		Body:      authFinishBody,
	})

	helloFrame := readMessage(t, clientConn)
	if helloFrame.Type != protocol.TypeServerHello {
		t.Fatalf("expected server.hello, got %s", helloFrame.Type.String())
	}

	configFrame := readMessage(t, clientConn)
	if configFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected config.push, got %s", configFrame.Type.String())
	}

	return clientConn, done, configFrame
}

func writeConfigAck(t *testing.T, conn net.Conn, requestID uint32, version uint64) {
	t.Helper()

	configAckBody, err := protocol.MarshalConfigAck(protocol.ConfigAck{
		ConfigVersion: version,
		AppliedAtMs:   uint64(time.Now().UTC().UnixMilli()),
		Status:        protocol.StatusOK,
	})
	if err != nil {
		t.Fatalf("marshal config.ack: %v", err)
	}
	writeMessage(t, conn, protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: requestID,
		Body:      configAckBody,
	})
}
