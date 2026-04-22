package control

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/system"
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
					ID:          1,
					Name:        "group-a",
					Enabled:     true,
					EffectiveIP: system.AnyIPv4,
					TokenHash:   tokenHash,
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

	response := protocol.ChallengeResponse(tokenHash, challenge.Nonce)
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
					ID:          1,
					Name:        "group-a",
					Enabled:     true,
					EffectiveIP: system.AnyIPv4,
					TokenHash:   tokenHash,
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
		Response:    protocol.ChallengeResponse(tokenHash, challenge.Nonce),
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

func TestServerForwardsPublicUDPDatagramsAndReusesSession(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeUDPPort(t)
	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:          1,
					Name:        "group-a",
					Enabled:     true,
					EffectiveIP: system.AnyIPv4,
					TokenHash:   tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    10,
								Protocol:    protocol.ProtocolUDP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: uint16(remotePort),
								RemoteEnd:   uint16(remotePort),
								LocalHost:   host,
								LocalStart:  5300,
								LocalEnd:    5300,
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

	publicConn := dialUDPConn(t, remotePort)
	defer publicConn.Close()

	openFrame := writeUDPAndReadOpenFrame(t, clientConn, publicConn, []byte("hello"))
	if openFrame.Type != protocol.TypeUDPOpen {
		t.Fatalf("expected udp.open, got %s", openFrame.Type.String())
	}
	if openFrame.RequestID == 0 {
		t.Fatalf("udp.open requestId must be non-zero")
	}

	openMessage, err := protocol.UnmarshalUDPOpen(openFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal udp.open: %v", err)
	}
	localAddr := publicConn.LocalAddr().(*net.UDPAddr)
	if openMessage.TunnelID != 10 {
		t.Fatalf("unexpected udp.open tunnel id: %d", openMessage.TunnelID)
	}
	if openMessage.RemotePort != uint16(remotePort) {
		t.Fatalf("unexpected udp.open remote port: %d", openMessage.RemotePort)
	}
	if !openMessage.ClientAddr.IP.Equal(localAddr.IP.To4()) {
		t.Fatalf("unexpected udp.open client ip: %v", openMessage.ClientAddr.IP)
	}
	if openMessage.ClientAddr.Port != uint16(localAddr.Port) {
		t.Fatalf("unexpected udp.open client port: %d", openMessage.ClientAddr.Port)
	}
	if openMessage.IdleTimeoutMs != uint32(defaultUDPIdleTimeout/time.Millisecond) {
		t.Fatalf("unexpected udp.open idle timeout: %d", openMessage.IdleTimeoutMs)
	}

	firstDataFrame := readMessage(t, clientConn)
	if firstDataFrame.Type != protocol.TypeUDPData {
		t.Fatalf("expected first udp.data, got %s", firstDataFrame.Type.String())
	}
	if firstDataFrame.StreamID != openFrame.StreamID {
		t.Fatalf("unexpected first udp.data session id: got %d want %d", firstDataFrame.StreamID, openFrame.StreamID)
	}
	if string(firstDataFrame.Body) != "hello" {
		t.Fatalf("unexpected first udp.data payload: %q", string(firstDataFrame.Body))
	}

	if _, err := publicConn.Write([]byte("again")); err != nil {
		t.Fatalf("write second udp datagram: %v", err)
	}

	secondDataFrame := readMessage(t, clientConn)
	if secondDataFrame.Type != protocol.TypeUDPData {
		t.Fatalf("expected second udp.data, got %s", secondDataFrame.Type.String())
	}
	if secondDataFrame.StreamID != openFrame.StreamID {
		t.Fatalf("unexpected second udp.data session id: got %d want %d", secondDataFrame.StreamID, openFrame.StreamID)
	}
	if string(secondDataFrame.Body) != "again" {
		t.Fatalf("unexpected second udp.data payload: %q", string(secondDataFrame.Body))
	}

	writeMessage(t, clientConn, protocol.Frame{
		Type:     protocol.TypeUDPData,
		StreamID: openFrame.StreamID,
		Body:     []byte("world"),
	})

	if err := publicConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set udp read deadline: %v", err)
	}
	buffer := make([]byte, 16)
	n, err := publicConn.Read(buffer)
	if err != nil {
		t.Fatalf("read udp response: %v", err)
	}
	if string(buffer[:n]) != "world" {
		t.Fatalf("unexpected udp response: %q", string(buffer[:n]))
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerCleansUpIdleUDPSessionAndNotifiesClient(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeUDPPort(t)
	server := NewServer(
		Options{
			WriteTimeout: time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	session := &sessionState{
		ID: 1,
		Group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			EffectiveIP: system.AnyIPv4,
		},
		Snapshot: ConfigSnapshot{
			Tunnels: []protocol.TunnelEntry{
				{
					TunnelID:    10,
					Protocol:    protocol.ProtocolUDP,
					TunnelFlags: protocol.TunnelFlagEnabled,
					RemoteStart: uint16(remotePort),
					RemoteEnd:   uint16(remotePort),
					LocalHost:   host,
					LocalStart:  5300,
					LocalEnd:    5300,
				},
			},
		},
		streams:        make(map[uint32]*publicStream),
		udpSessions:    make(map[uint32]*publicUDPSession),
		udpSessionKeys: make(map[string]uint32),
		listeners:      make(map[uint32][]net.Listener),
		udpListeners:   make(map[uint32][]*net.UDPConn),
		done:           make(chan struct{}),
	}
	defer server.shutdownSession(session)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := server.ensureTunnelListeners(serverConn, logger, session); err != nil {
		t.Fatalf("ensure tunnel listeners: %v", err)
	}

	publicConn := dialUDPConn(t, remotePort)
	defer publicConn.Close()

	openFrame := writeUDPAndReadOpenFrame(t, clientConn, publicConn, []byte("hello"))
	if openFrame.Type != protocol.TypeUDPOpen {
		t.Fatalf("expected udp.open, got %s", openFrame.Type.String())
	}

	firstDataFrame := readMessage(t, clientConn)
	if firstDataFrame.Type != protocol.TypeUDPData {
		t.Fatalf("expected udp.data, got %s", firstDataFrame.Type.String())
	}

	udpSession := session.publicUDPSession(openFrame.StreamID)
	if udpSession == nil {
		t.Fatal("expected udp session to exist")
	}
	udpSession.touch(time.Now().Add(-defaultUDPIdleTimeout - 2*time.Second))

	closeFrame, err := readMessageWithin(clientConn, 2*time.Second)
	if err != nil {
		t.Fatalf("read udp.close: %v", err)
	}
	if closeFrame.Type != protocol.TypeUDPClose {
		t.Fatalf("expected udp.close, got %s", closeFrame.Type.String())
	}
	if closeFrame.StreamID != openFrame.StreamID {
		t.Fatalf("unexpected udp.close session id: got %d want %d", closeFrame.StreamID, openFrame.StreamID)
	}

	udpClose, err := protocol.UnmarshalUDPClose(closeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal udp.close: %v", err)
	}
	if udpClose.ReasonCode != protocol.CloseReasonIdleTimeout || udpClose.Message != "udp session idle timeout" {
		t.Fatalf("unexpected udp.close body: %#v", udpClose)
	}
	if session.publicUDPSession(openFrame.StreamID) != nil {
		t.Fatal("expected udp session to be removed after idle cleanup")
	}
}

func TestServerEnsureTunnelListenersUsesGroupEffectiveIP(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeTCPPort(t)
	server := NewServer(
		Options{
			WriteTimeout: time.Second,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
					},
				},
			},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	session := newTestSessionState(GroupRuntime{
		ID:          1,
		Name:        "group-a",
		EffectiveIP: "127.0.0.1",
	}, ConfigSnapshot{
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
	})
	defer server.shutdownSession(session)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := server.ensureTunnelListeners(serverConn, logger, session); err != nil {
		t.Fatalf("ensure tunnel listeners: %v", err)
	}

	listeners := session.listeners[7]
	if len(listeners) != 1 {
		t.Fatalf("unexpected listener count: %d", len(listeners))
	}

	boundHost, _, err := net.SplitHostPort(listeners[0].Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	if boundHost != "127.0.0.1" {
		t.Fatalf("unexpected listener bind host: got %q want %q", boundHost, "127.0.0.1")
	}
}

func TestServerEnsureTunnelListenersRejectsMissingLocalEffectiveIP(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeTCPPort(t)
	server := NewServer(
		Options{
			WriteTimeout: time.Second,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "10.0.0.9", Family: system.FamilyIPv4},
					},
				},
			},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	session := newTestSessionState(GroupRuntime{
		ID:          1,
		Name:        "group-a",
		EffectiveIP: "127.0.0.1",
	}, ConfigSnapshot{
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
	})

	err = server.ensureTunnelListeners(serverConn, slog.New(slog.NewTextHandler(io.Discard, nil)), session)
	if err == nil {
		t.Fatal("expected missing local effective_ip to be rejected")
	}
	if !strings.Contains(err.Error(), "not a current local IP") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServerResolveGroupEffectiveIPAllowsSpecialIPv6WithoutSnapshot(t *testing.T) {
	server := NewServer(
		Options{
			WriteTimeout: time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	bindIP, err := server.resolveGroupEffectiveIP(GroupRuntime{EffectiveIP: system.AnyIPv6})
	if err != nil {
		t.Fatalf("resolve group effective_ip: %v", err)
	}
	if bindIP != system.AnyIPv6 {
		t.Fatalf("unexpected bind ip: got %q want %q", bindIP, system.AnyIPv6)
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
					ID:          1,
					Name:        "group-a",
					Enabled:     true,
					EffectiveIP: system.AnyIPv4,
					TokenHash:   tokenHash,
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

func TestServerStartsListenersForEnabledTCPRangeTunnelAndUsesMatchedRemotePort(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	rangePort := freeTCPPortRange(t, 2)
	allowedPort := freeTCPPortExcept(t, rangePort, rangePort+1)
	disabledPort := freeTCPPortExcept(t, rangePort, rangePort+1, allowedPort)
	udpPort := freeUDPPortExcept(t, rangePort, rangePort+1, allowedPort, disabledPort)

	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:          1,
					Name:        "group-a",
					Enabled:     true,
					EffectiveIP: system.AnyIPv4,
					TokenHash:   tokenHash,
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
	assertTCPDialFails(t, udpPort)

	singleConn := waitForTCPDial(t, allowedPort)
	singleOpenFrame := readMessage(t, clientConn)
	if singleOpenFrame.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected stream.open, got %s", singleOpenFrame.Type.String())
	}
	singleOpen, err := protocol.UnmarshalStreamOpen(singleOpenFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal single stream.open: %v", err)
	}
	if singleOpen.TunnelID != 7 || singleOpen.RemotePort != uint16(allowedPort) {
		t.Fatalf("unexpected single stream.open: %#v", singleOpen)
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
		RequestID: singleOpenFrame.RequestID,
		StreamID:  singleOpenFrame.StreamID,
		Body:      streamOpenedBody,
	})
	_ = singleConn.Close()

	rangeConn := waitForTCPDial(t, rangePort+1)
	rangeOpenFrame := readMessage(t, clientConn)
	if rangeOpenFrame.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected range stream.open, got %s", rangeOpenFrame.Type.String())
	}
	rangeOpen, err := protocol.UnmarshalStreamOpen(rangeOpenFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal range stream.open: %v", err)
	}
	if rangeOpen.TunnelID != 9 {
		t.Fatalf("unexpected range tunnel id: %d", rangeOpen.TunnelID)
	}
	if rangeOpen.RemotePort != uint16(rangePort+1) {
		t.Fatalf("unexpected range remote port: got %d want %d", rangeOpen.RemotePort, rangePort+1)
	}

	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: rangeOpenFrame.RequestID,
		StreamID:  rangeOpenFrame.StreamID,
		Body:      streamOpenedBody,
	})
	_ = rangeConn.Close()

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerStartsListenersForEnabledUDPRangeTunnelAndUsesMatchedRemotePort(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	rangePort := freeUDPPortRange(t, 2)
	singlePort := freeUDPPortExcept(t, rangePort, rangePort+1)
	disabledPort := freeUDPPortExcept(t, rangePort, rangePort+1, singlePort)

	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:          1,
					Name:        "group-a",
					Enabled:     true,
					EffectiveIP: system.AnyIPv4,
					TokenHash:   tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    10,
								Protocol:    protocol.ProtocolUDP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: uint16(singlePort),
								RemoteEnd:   uint16(singlePort),
								LocalHost:   host,
								LocalStart:  5300,
								LocalEnd:    5300,
							},
							{
								TunnelID:    11,
								Protocol:    protocol.ProtocolUDP,
								TunnelFlags: 0,
								RemoteStart: uint16(disabledPort),
								RemoteEnd:   uint16(disabledPort),
								LocalHost:   host,
								LocalStart:  5301,
								LocalEnd:    5301,
							},
							{
								TunnelID:    12,
								Protocol:    protocol.ProtocolUDP,
								TunnelFlags: protocol.TunnelFlagEnabled | protocol.TunnelFlagRange,
								RemoteStart: uint16(rangePort),
								RemoteEnd:   uint16(rangePort + 1),
								LocalHost:   host,
								LocalStart:  5302,
								LocalEnd:    5303,
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

	singleConn := dialUDPConn(t, singlePort)
	defer singleConn.Close()

	singleOpenFrame := writeUDPAndReadOpenFrame(t, clientConn, singleConn, []byte("single"))
	if singleOpenFrame.Type != protocol.TypeUDPOpen {
		t.Fatalf("expected single udp.open, got %s", singleOpenFrame.Type.String())
	}
	singleOpen, err := protocol.UnmarshalUDPOpen(singleOpenFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal single udp.open: %v", err)
	}
	if singleOpen.TunnelID != 10 || singleOpen.RemotePort != uint16(singlePort) {
		t.Fatalf("unexpected single udp.open: %#v", singleOpen)
	}

	singleDataFrame := readMessage(t, clientConn)
	if singleDataFrame.Type != protocol.TypeUDPData {
		t.Fatalf("expected single udp.data, got %s", singleDataFrame.Type.String())
	}
	if singleDataFrame.StreamID != singleOpenFrame.StreamID || string(singleDataFrame.Body) != "single" {
		t.Fatalf("unexpected single udp.data: %#v", singleDataFrame)
	}

	rangeConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp client: %v", err)
	}
	defer rangeConn.Close()

	rangeClientAddr := rangeConn.LocalAddr().(*net.UDPAddr)

	firstOpenFrame := writeUDPToAndReadOpenFrame(
		t,
		clientConn,
		rangeConn,
		&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: rangePort},
		[]byte("range-a"),
	)
	if firstOpenFrame.Type != protocol.TypeUDPOpen {
		t.Fatalf("expected first range udp.open, got %s", firstOpenFrame.Type.String())
	}
	firstOpen, err := protocol.UnmarshalUDPOpen(firstOpenFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal first range udp.open: %v", err)
	}
	if firstOpen.TunnelID != 12 || firstOpen.RemotePort != uint16(rangePort) {
		t.Fatalf("unexpected first range udp.open: %#v", firstOpen)
	}
	if !firstOpen.ClientAddr.IP.Equal(rangeClientAddr.IP.To4()) || firstOpen.ClientAddr.Port != uint16(rangeClientAddr.Port) {
		t.Fatalf("unexpected first range client addr: %#v", firstOpen.ClientAddr)
	}

	firstDataFrame := readMessage(t, clientConn)
	if firstDataFrame.Type != protocol.TypeUDPData {
		t.Fatalf("expected first range udp.data, got %s", firstDataFrame.Type.String())
	}
	if firstDataFrame.StreamID != firstOpenFrame.StreamID || string(firstDataFrame.Body) != "range-a" {
		t.Fatalf("unexpected first range udp.data: %#v", firstDataFrame)
	}

	secondOpenFrame := writeUDPToAndReadOpenFrame(
		t,
		clientConn,
		rangeConn,
		&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: rangePort + 1},
		[]byte("range-b"),
	)
	if secondOpenFrame.Type != protocol.TypeUDPOpen {
		t.Fatalf("expected second range udp.open, got %s", secondOpenFrame.Type.String())
	}
	if secondOpenFrame.StreamID == firstOpenFrame.StreamID {
		t.Fatalf("expected distinct udp session ids for different remote ports, got %d", secondOpenFrame.StreamID)
	}
	secondOpen, err := protocol.UnmarshalUDPOpen(secondOpenFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal second range udp.open: %v", err)
	}
	if secondOpen.TunnelID != 12 || secondOpen.RemotePort != uint16(rangePort+1) {
		t.Fatalf("unexpected second range udp.open: %#v", secondOpen)
	}
	if !secondOpen.ClientAddr.IP.Equal(rangeClientAddr.IP.To4()) || secondOpen.ClientAddr.Port != uint16(rangeClientAddr.Port) {
		t.Fatalf("unexpected second range client addr: %#v", secondOpen.ClientAddr)
	}

	secondDataFrame := readMessage(t, clientConn)
	if secondDataFrame.Type != protocol.TypeUDPData {
		t.Fatalf("expected second range udp.data, got %s", secondDataFrame.Type.String())
	}
	if secondDataFrame.StreamID != secondOpenFrame.StreamID || string(secondDataFrame.Body) != "range-b" {
		t.Fatalf("unexpected second range udp.data: %#v", secondDataFrame)
	}

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

func TestServerRejectsSecondClientForSameGroup(t *testing.T) {
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
					ID:          1,
					Name:        "group-a",
					Enabled:     true,
					EffectiveIP: system.AnyIPv4,
					TokenHash:   tokenHash,
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

	firstConn, firstDone, _ := authenticateServerSession(t, server, tokenID, tokenHash)

	secondClientRaw, secondServerRaw := net.Pipe()
	secondClientConn := &connWithRemoteAddr{
		Conn:   secondClientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10002},
	}
	secondServerConn := &connWithRemoteAddr{
		Conn:   secondServerRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20002},
	}

	secondDone := make(chan struct{})
	server.registerConn(secondServerConn)
	server.connWG.Add(1)
	go func() {
		defer close(secondDone)
		server.handleConnection(secondServerConn)
	}()

	authBeginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		TokenID:       tokenID,
		ClientVersion: "test-client-2",
		Hostname:      "node-2",
		OS:            protocol.OSLinux,
		Arch:          protocol.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("marshal auth.begin: %v", err)
	}
	writeMessage(t, secondClientConn, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      authBeginBody,
	})

	challengeFrame := readMessage(t, secondClientConn)
	if challengeFrame.Type != protocol.TypeAuthChallenge {
		t.Fatalf("expected auth.challenge, got %s", challengeFrame.Type.String())
	}
	challenge, err := protocol.UnmarshalAuthChallenge(challengeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.challenge: %v", err)
	}

	authFinishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    protocol.ChallengeResponse(tokenHash, challenge.Nonce),
	})
	if err != nil {
		t.Fatalf("marshal auth.finish: %v", err)
	}
	writeMessage(t, secondClientConn, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 2,
		Body:      authFinishBody,
	})

	errorFrame := readMessage(t, secondClientConn)
	if errorFrame.Type != protocol.TypeError {
		t.Fatalf("expected error frame, got %s", errorFrame.Type.String())
	}
	errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if errorBody.ErrorCode != protocol.ErrorCodeAuthClientLimitReached {
		t.Fatalf("unexpected error code: %d", errorBody.ErrorCode)
	}

	_ = secondClientConn.Close()
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second server connection did not exit")
	}

	_ = firstConn.Close()
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first server connection did not exit")
	}

	thirdConn, thirdDone, _ := authenticateServerSession(t, server, tokenID, tokenHash)
	_ = thirdConn.Close()
	select {
	case <-thirdDone:
	case <-time.After(2 * time.Second):
		t.Fatal("third server connection did not exit")
	}
}

func TestServerHandlesUDPControlFramesWithoutEndingSession(t *testing.T) {
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
					ID:          1,
					Name:        "group-a",
					Enabled:     true,
					EffectiveIP: system.AnyIPv4,
					TokenHash:   tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    10,
								Protocol:    protocol.ProtocolUDP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: 21000,
								RemoteEnd:   21000,
								LocalHost:   host,
								LocalStart:  5300,
								LocalEnd:    5300,
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

	writeMessage(t, clientConn, protocol.Frame{
		Type:     protocol.TypeUDPData,
		StreamID: 77,
		Body:     []byte("hello"),
	})

	udpCloseFrame := readMessage(t, clientConn)
	if udpCloseFrame.Type != protocol.TypeUDPClose {
		t.Fatalf("expected udp.close, got %s", udpCloseFrame.Type.String())
	}
	udpClose, err := protocol.UnmarshalUDPClose(udpCloseFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal udp.close: %v", err)
	}
	if udpClose.ReasonCode != protocol.CloseReasonProtocolError || udpClose.Message != "udp session not found" {
		t.Fatalf("unexpected udp.close: %#v", udpClose)
	}

	udpCloseBody, err := protocol.MarshalUDPClose(protocol.UDPClose{
		ReasonCode: protocol.CloseReasonIdleTimeout,
		Initiator:  protocol.InitiatorFRPC,
		Message:    "cleanup",
	})
	if err != nil {
		t.Fatalf("marshal udp.close: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:     protocol.TypeUDPClose,
		StreamID: 77,
		Body:     udpCloseBody,
	})

	heartbeatBody, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{
		ClientUnixMs:           456,
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

func TestServerRefreshGroupPushesUpdatedConfigToActiveSession(t *testing.T) {
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

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: system.AnyIPv4,
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
			},
		},
	}

	server := NewServer(
		Options{
			Repository: repo,
			Network:    staticSnapshotReader{},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)
	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil {
		waitForActiveGroupSession(t, server, repo.group.ID)
		active, ok = server.activeSession(repo.group.ID)
	}
	if !ok || active == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(remotePort),
				RemoteEnd:   uint16(remotePort),
				LocalHost:   host,
				LocalStart:  2200,
				LocalEnd:    2200,
			},
		},
	}
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	refreshedFrame := readMessage(t, clientConn)
	if refreshedFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected refreshed config.push, got %s", refreshedFrame.Type.String())
	}
	refreshedPush, err := protocol.UnmarshalConfigPush(refreshedFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal refreshed config.push: %v", err)
	}
	if refreshedPush.ConfigVersion != 2 {
		t.Fatalf("unexpected refreshed config version: %d", refreshedPush.ConfigVersion)
	}
	writeConfigAck(t, clientConn, refreshedFrame.RequestID, refreshedPush.ConfigVersion)
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
	}

	pingBody, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{ClientUnixMs: 12345})
	if err != nil {
		t.Fatalf("marshal heartbeat.ping: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeHeartbeatPing,
		RequestID: 3,
		Body:      pingBody,
	})
	pongFrame := readMessage(t, clientConn)
	if pongFrame.Type != protocol.TypeHeartbeatPong || pongFrame.RequestID != 3 {
		t.Fatalf("unexpected heartbeat.pong after refresh: %#v", pongFrame)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerRefreshGroupClosesSessionWhenConfigPushPending(t *testing.T) {
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

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: system.AnyIPv4,
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
			},
		},
	}

	server := NewServer(
		Options{
			Repository: repo,
			Network:    staticSnapshotReader{},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, _ := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	waitForActiveGroupSession(t, server, repo.group.ID)
	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(remotePort),
				RemoteEnd:   uint16(remotePort),
				LocalHost:   host,
				LocalStart:  2200,
				LocalEnd:    2200,
			},
		},
	}

	server.RefreshGroup(repo.group.ID)

	if _, err := readMessageWithin(clientConn, time.Second); err == nil {
		t.Fatal("expected session to close while config push is still pending")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerRefreshGroupBlocksReplacementSessionUntilRefreshCompletes(t *testing.T) {
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

	repo := &blockingRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: system.AnyIPv4,
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
			},
		},
		loadByIDStarted: make(chan struct{}, 1),
		allowLoadByID:   make(chan struct{}),
	}

	server := NewServer(
		Options{
			Repository: repo,
			Network:    staticSnapshotReader{},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)
	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil {
		waitForActiveGroupSession(t, server, repo.group.ID)
		active, ok = server.activeSession(repo.group.ID)
	}
	if !ok || active == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(remotePort),
				RemoteEnd:   uint16(remotePort),
				LocalHost:   host,
				LocalStart:  2200,
				LocalEnd:    2200,
			},
		},
	}

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	select {
	case <-repo.loadByIDStarted:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach repository load")
	}

	_ = clientConn.Close()

	replacementClientRaw, replacementServerRaw := net.Pipe()
	replacementClientConn := &connWithRemoteAddr{
		Conn:   replacementClientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10002},
	}
	replacementServerConn := &connWithRemoteAddr{
		Conn:   replacementServerRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20002},
	}
	defer replacementClientConn.Close()

	replacementDone := make(chan struct{})
	server.registerConn(replacementServerConn)
	server.connWG.Add(1)
	go func() {
		defer close(replacementDone)
		server.handleConnection(replacementServerConn)
	}()

	authBeginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		TokenID:       tokenID,
		ClientVersion: "test-client",
		Hostname:      "node-2",
		OS:            protocol.OSLinux,
		Arch:          protocol.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("marshal replacement auth.begin: %v", err)
	}
	writeMessage(t, replacementClientConn, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      authBeginBody,
	})

	challengeFrame := readMessage(t, replacementClientConn)
	if challengeFrame.Type != protocol.TypeAuthChallenge {
		t.Fatalf("expected replacement auth.challenge, got %s", challengeFrame.Type.String())
	}
	challenge, err := protocol.UnmarshalAuthChallenge(challengeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal replacement auth.challenge: %v", err)
	}

	authFinishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    protocol.ChallengeResponse(tokenHash, challenge.Nonce),
	})
	if err != nil {
		t.Fatalf("marshal replacement auth.finish: %v", err)
	}
	writeMessage(t, replacementClientConn, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 2,
		Body:      authFinishBody,
	})

	if frame, err := readMessageWithin(replacementClientConn, 200*time.Millisecond); err == nil {
		t.Fatalf("expected replacement auth to wait for refresh completion, got %s", frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected replacement auth wait to time out, got %v", err)
	}

	close(repo.allowLoadByID)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stale session did not exit")
	}

	helloFrame := readMessage(t, replacementClientConn)
	if helloFrame.Type != protocol.TypeServerHello {
		t.Fatalf("expected replacement server.hello, got %s", helloFrame.Type.String())
	}

	replacementConfigFrame := readMessage(t, replacementClientConn)
	if replacementConfigFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected replacement config.push, got %s", replacementConfigFrame.Type.String())
	}
	replacementPush, err := protocol.UnmarshalConfigPush(replacementConfigFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal replacement config.push: %v", err)
	}
	if replacementPush.ConfigVersion != 2 {
		t.Fatalf("unexpected replacement config version: %d", replacementPush.ConfigVersion)
	}
	writeConfigAck(t, replacementClientConn, replacementConfigFrame.RequestID, replacementPush.ConfigVersion)

	pingBody, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{ClientUnixMs: 12345})
	if err != nil {
		t.Fatalf("marshal replacement heartbeat.ping: %v", err)
	}
	writeMessage(t, replacementClientConn, protocol.Frame{
		Type:      protocol.TypeHeartbeatPing,
		RequestID: 3,
		Body:      pingBody,
	})
	pongFrame := readMessage(t, replacementClientConn)
	if pongFrame.Type != protocol.TypeHeartbeatPong || pongFrame.RequestID != 3 {
		t.Fatalf("unexpected replacement heartbeat.pong: %#v", pongFrame)
	}

	_ = replacementClientConn.Close()
	select {
	case <-replacementDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement session did not exit")
	}
}

func TestServerRefreshGroupFreezesRuntimeUntilAckAndRebuildsListeners(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	oldTCPPort := freeTCPPort(t)
	newTCPPort := freeTCPPortExcept(t, oldTCPPort)
	oldUDPPort := freeUDPPort(t)
	newUDPPort := freeUDPPortExcept(t, oldUDPPort)

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: system.AnyIPv4,
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: uint16(oldTCPPort),
						RemoteEnd:   uint16(oldTCPPort),
						LocalHost:   host,
						LocalStart:  2200,
						LocalEnd:    2200,
					},
					{
						TunnelID:    8,
						Protocol:    protocol.ProtocolUDP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: uint16(oldUDPPort),
						RemoteEnd:   uint16(oldUDPPort),
						LocalHost:   host,
						LocalStart:  5300,
						LocalEnd:    5300,
					},
				},
			},
		},
	}

	server := NewServer(
		Options{
			Repository: repo,
			Network:    staticSnapshotReader{},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil {
		waitForActiveGroupSession(t, server, repo.group.ID)
		active, ok = server.activeSession(repo.group.ID)
	}
	if !ok || active == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	publicConn := waitForTCPDial(t, oldTCPPort)
	defer publicConn.Close()

	streamOpenFrame := readMessage(t, clientConn)
	if streamOpenFrame.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected stream.open, got %s", streamOpenFrame.Type.String())
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

	publicUDPConn := dialUDPConn(t, oldUDPPort)
	defer publicUDPConn.Close()

	udpOpenFrame := writeUDPAndReadOpenFrame(t, clientConn, publicUDPConn, []byte("hello"))
	if udpOpenFrame.Type != protocol.TypeUDPOpen {
		t.Fatalf("expected udp.open, got %s", udpOpenFrame.Type.String())
	}
	firstUDPData := readMessage(t, clientConn)
	if firstUDPData.Type != protocol.TypeUDPData {
		t.Fatalf("expected udp.data, got %s", firstUDPData.Type.String())
	}

	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(newTCPPort),
				RemoteEnd:   uint16(newTCPPort),
				LocalHost:   host,
				LocalStart:  2201,
				LocalEnd:    2201,
			},
			{
				TunnelID:    8,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(newUDPPort),
				RemoteEnd:   uint16(newUDPPort),
				LocalHost:   host,
				LocalStart:  5301,
				LocalEnd:    5301,
			},
		},
	}

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	var (
		gotStreamClose bool
		gotUDPClose    bool
		refreshedFrame protocol.Frame
	)
	deadline := time.Now().Add(2 * time.Second)
	for !(gotStreamClose && gotUDPClose && refreshedFrame.Type == protocol.TypeConfigPush) {
		frame, err := readMessageWithin(clientConn, 200*time.Millisecond)
		if err != nil {
			if isTimeoutError(err) && time.Now().Before(deadline) {
				continue
			}
			t.Fatalf("read refresh frame: %v", err)
		}

		switch frame.Type {
		case protocol.TypeStreamClose:
			streamClose, err := protocol.UnmarshalStreamClose(frame.Body)
			if err != nil {
				t.Fatalf("unmarshal stream.close: %v", err)
			}
			if frame.StreamID != streamOpenFrame.StreamID {
				t.Fatalf("unexpected stream.close stream id: got %d want %d", frame.StreamID, streamOpenFrame.StreamID)
			}
			if streamClose.ReasonCode != protocol.CloseReasonAdminTerminated || streamClose.Message != "reload in progress" {
				t.Fatalf("unexpected stream.close body: %#v", streamClose)
			}
			gotStreamClose = true
		case protocol.TypeUDPClose:
			udpClose, err := protocol.UnmarshalUDPClose(frame.Body)
			if err != nil {
				t.Fatalf("unmarshal udp.close: %v", err)
			}
			if frame.StreamID != udpOpenFrame.StreamID {
				t.Fatalf("unexpected udp.close session id: got %d want %d", frame.StreamID, udpOpenFrame.StreamID)
			}
			if udpClose.ReasonCode != protocol.CloseReasonAdminTerminated || udpClose.Message != "reload in progress" {
				t.Fatalf("unexpected udp.close body: %#v", udpClose)
			}
			gotUDPClose = true
		case protocol.TypeConfigPush:
			refreshedFrame = frame
		default:
			t.Fatalf("unexpected frame during refresh: %s", frame.Type.String())
		}

		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for refresh frames")
		}
	}

	refreshedPush, err := protocol.UnmarshalConfigPush(refreshedFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal refreshed config.push: %v", err)
	}
	if refreshedPush.ConfigVersion != 2 {
		t.Fatalf("unexpected refreshed config version: %d", refreshedPush.ConfigVersion)
	}

	assertTCPDialFails(t, oldTCPPort)
	assertTCPDialFails(t, newTCPPort)

	if err := publicConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("set public conn deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := publicConn.Read(buffer); err == nil {
		t.Fatal("expected active public tcp connection to close during refresh")
	}

	writeConfigAck(t, clientConn, refreshedFrame.RequestID, refreshedPush.ConfigVersion)
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
	}

	refreshedPublicConn := waitForTCPDial(t, newTCPPort)
	defer refreshedPublicConn.Close()

	refreshedStreamOpen := readMessage(t, clientConn)
	if refreshedStreamOpen.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected refreshed stream.open, got %s", refreshedStreamOpen.Type.String())
	}
	streamOpen, err := protocol.UnmarshalStreamOpen(refreshedStreamOpen.Body)
	if err != nil {
		t.Fatalf("unmarshal refreshed stream.open: %v", err)
	}
	if streamOpen.RemotePort != uint16(newTCPPort) {
		t.Fatalf("unexpected refreshed remote port: got %d want %d", streamOpen.RemotePort, newTCPPort)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: refreshedStreamOpen.RequestID,
		StreamID:  refreshedStreamOpen.StreamID,
		Body:      streamOpenedBody,
	})

	pingBody, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{ClientUnixMs: 12345})
	if err != nil {
		t.Fatalf("marshal heartbeat.ping: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeHeartbeatPing,
		RequestID: 3,
		Body:      pingBody,
	})
	pongFrame := readMessage(t, clientConn)
	if pongFrame.Type != protocol.TypeHeartbeatPong || pongFrame.RequestID != 3 {
		t.Fatalf("unexpected heartbeat.pong after refresh: %#v", pongFrame)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerRefreshGroupRebindsListenersWhenOnlyEffectiveIPChanges(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	tcpPort := freeTCPPort(t)
	udpPort := freeUDPPort(t)

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: uint16(tcpPort),
						RemoteEnd:   uint16(tcpPort),
						LocalHost:   host,
						LocalStart:  2200,
						LocalEnd:    2200,
					},
					{
						TunnelID:    8,
						Protocol:    protocol.ProtocolUDP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: uint16(udpPort),
						RemoteEnd:   uint16(udpPort),
						LocalHost:   host,
						LocalStart:  5300,
						LocalEnd:    5300,
					},
				},
			},
		},
	}

	server := NewServer(
		Options{
			Repository: repo,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
					},
				},
			},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil {
		waitForActiveGroupSession(t, server, repo.group.ID)
		active, ok = server.activeSession(repo.group.ID)
	}
	if !ok || active == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	publicConn := waitForTCPDial(t, tcpPort)
	defer publicConn.Close()

	streamOpenFrame := readMessage(t, clientConn)
	if streamOpenFrame.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected stream.open, got %s", streamOpenFrame.Type.String())
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

	publicUDPConn := dialUDPConn(t, udpPort)
	defer publicUDPConn.Close()

	udpOpenFrame := writeUDPAndReadOpenFrame(t, clientConn, publicUDPConn, []byte("hello"))
	if udpOpenFrame.Type != protocol.TypeUDPOpen {
		t.Fatalf("expected udp.open, got %s", udpOpenFrame.Type.String())
	}
	firstUDPData := readMessage(t, clientConn)
	if firstUDPData.Type != protocol.TypeUDPData {
		t.Fatalf("expected udp.data, got %s", firstUDPData.Type.String())
	}

	repo.group.EffectiveIP = system.AnyIPv4
	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(tcpPort),
				RemoteEnd:   uint16(tcpPort),
				LocalHost:   host,
				LocalStart:  2200,
				LocalEnd:    2200,
			},
			{
				TunnelID:    8,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(udpPort),
				RemoteEnd:   uint16(udpPort),
				LocalHost:   host,
				LocalStart:  5300,
				LocalEnd:    5300,
			},
		},
	}

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	var (
		gotStreamClose bool
		gotUDPClose    bool
	)
	deadline := time.Now().Add(2 * time.Second)
	for !(gotStreamClose && gotUDPClose) {
		frame, err := readMessageWithin(clientConn, 200*time.Millisecond)
		if err != nil {
			if isTimeoutError(err) && time.Now().Before(deadline) {
				continue
			}
			t.Fatalf("read effective_ip refresh frame: %v", err)
		}

		switch frame.Type {
		case protocol.TypeStreamClose:
			streamClose, err := protocol.UnmarshalStreamClose(frame.Body)
			if err != nil {
				t.Fatalf("unmarshal stream.close: %v", err)
			}
			if frame.StreamID != streamOpenFrame.StreamID {
				t.Fatalf("unexpected stream.close stream id: got %d want %d", frame.StreamID, streamOpenFrame.StreamID)
			}
			if streamClose.ReasonCode != protocol.CloseReasonAdminTerminated || streamClose.Message != "reload in progress" {
				t.Fatalf("unexpected stream.close body: %#v", streamClose)
			}
			gotStreamClose = true
		case protocol.TypeUDPClose:
			udpClose, err := protocol.UnmarshalUDPClose(frame.Body)
			if err != nil {
				t.Fatalf("unmarshal udp.close: %v", err)
			}
			if frame.StreamID != udpOpenFrame.StreamID {
				t.Fatalf("unexpected udp.close session id: got %d want %d", frame.StreamID, udpOpenFrame.StreamID)
			}
			if udpClose.ReasonCode != protocol.CloseReasonAdminTerminated || udpClose.Message != "reload in progress" {
				t.Fatalf("unexpected udp.close body: %#v", udpClose)
			}
			gotUDPClose = true
		case protocol.TypeConfigPush:
			t.Fatal("did not expect config.push when only effective_ip changed")
		default:
			t.Fatalf("unexpected frame during effective_ip refresh: %s", frame.Type.String())
		}
	}

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("effective_ip refresh did not complete")
	}

	if frame, err := readMessageWithin(clientConn, 200*time.Millisecond); err == nil {
		t.Fatalf("unexpected frame after effective_ip refresh: %s", frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected no config.push after effective_ip refresh, got %v", err)
	}

	currentGroup, currentSnapshot := active.session.currentGroupAndSnapshot()
	if currentGroup.EffectiveIP != system.AnyIPv4 {
		t.Fatalf("unexpected effective_ip after local rebind: %q", currentGroup.EffectiveIP)
	}
	if currentSnapshot.Version != 1 {
		t.Fatalf("expected snapshot version to remain unchanged, got %d", currentSnapshot.Version)
	}
	if pendingRequestID, _ := active.session.configAckState(); pendingRequestID != 0 {
		t.Fatalf("expected no pending config request after local rebind, got %d", pendingRequestID)
	}

	tcpListeners := active.session.listeners[7]
	if len(tcpListeners) != 1 {
		t.Fatalf("unexpected tcp listener count after rebind: %d", len(tcpListeners))
	}
	boundHost, _, err := net.SplitHostPort(tcpListeners[0].Addr().String())
	if err != nil {
		t.Fatalf("split rebound tcp listener addr: %v", err)
	}
	if boundHost != system.AnyIPv4 && boundHost != system.AnyIPv6 {
		t.Fatalf("unexpected rebound tcp listener host: %q", boundHost)
	}

	udpListeners := active.session.udpListeners[8]
	if len(udpListeners) != 1 {
		t.Fatalf("unexpected udp listener count after rebind: %d", len(udpListeners))
	}
	udpBoundHost, _, err := net.SplitHostPort(udpListeners[0].LocalAddr().String())
	if err != nil {
		t.Fatalf("split rebound udp listener addr: %v", err)
	}
	if udpBoundHost != system.AnyIPv4 && udpBoundHost != system.AnyIPv6 {
		t.Fatalf("unexpected rebound udp listener host: %q", udpBoundHost)
	}

	if err := publicConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("set public conn deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := publicConn.Read(buffer); err == nil {
		t.Fatal("expected active public tcp connection to close during effective_ip refresh")
	}

	reboundPublicConn := waitForTCPDial(t, tcpPort)
	defer reboundPublicConn.Close()

	reboundStreamOpen := readMessage(t, clientConn)
	if reboundStreamOpen.Type != protocol.TypeStreamOpen {
		t.Fatalf("expected rebound stream.open, got %s", reboundStreamOpen.Type.String())
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: reboundStreamOpen.RequestID,
		StreamID:  reboundStreamOpen.StreamID,
		Body:      streamOpenedBody,
	})

	reboundUDPOpen := writeUDPAndReadOpenFrame(t, clientConn, publicUDPConn, []byte("again"))
	if reboundUDPOpen.Type != protocol.TypeUDPOpen {
		t.Fatalf("expected rebound udp.open, got %s", reboundUDPOpen.Type.String())
	}
	if reboundUDPOpen.StreamID == udpOpenFrame.StreamID {
		t.Fatal("expected effective_ip refresh to create a new udp session")
	}
	reboundUDPData := readMessage(t, clientConn)
	if reboundUDPData.Type != protocol.TypeUDPData {
		t.Fatalf("expected rebound udp.data, got %s", reboundUDPData.Type.String())
	}

	pingBody, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{ClientUnixMs: 12345})
	if err != nil {
		t.Fatalf("marshal heartbeat.ping: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeHeartbeatPing,
		RequestID: 3,
		Body:      pingBody,
	})
	pongFrame := readMessage(t, clientConn)
	if pongFrame.Type != protocol.TypeHeartbeatPong || pongFrame.RequestID != 3 {
		t.Fatalf("unexpected heartbeat.pong after effective_ip refresh: %#v", pongFrame)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerFreezeGroupRuntimeDropsBufferedTCPData(t *testing.T) {
	server := NewServer(
		Options{
			WriteTimeout: time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	controlClient, controlServer := net.Pipe()
	defer controlClient.Close()
	defer controlServer.Close()

	publicClient, publicServer := net.Pipe()
	defer publicClient.Close()
	defer publicServer.Close()

	session := newTestSessionState(
		GroupRuntime{ID: 1, Name: "group-a", EffectiveIP: system.AnyIPv4},
		ConfigSnapshot{Version: 1},
	)

	streamID := uint32(7)
	stream := &publicStream{
		configVersion: 1,
		conn:          publicServer,
		ready:         make(chan error, 1),
	}

	session.runtimeMu.Lock()
	session.listenersStarted = true
	session.runtimeGeneration = 1
	session.streams[streamID] = stream
	session.runtimeMu.Unlock()

	session.writeMu.Lock()

	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		server.copyPublicToClient(controlServer, session, streamID, stream)
	}()

	if _, err := publicClient.Write([]byte("late")); err != nil {
		t.Fatalf("write public payload: %v", err)
	}

	listeners, udpListeners, streams, _ := session.freezeTunnelRuntime()
	closeStartedTunnelListeners(listeners, udpListeners)

	session.writeMu.Unlock()

	closeDone := make(chan error, 1)
	go func() {
		for capturedStreamID, capturedStream := range streams {
			if err := server.sendStreamClose(controlServer, session, capturedStreamID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil {
				closeDone <- err
				return
			}
			capturedStream.signalReady(net.ErrClosed)
			capturedStream.close()
		}
		closeDone <- nil
	}()

	closeFrame := readMessage(t, controlClient)
	if closeFrame.Type != protocol.TypeStreamClose {
		t.Fatalf("expected stream.close after freeze, got %s", closeFrame.Type.String())
	}

	if frame, err := readMessageWithin(controlClient, 200*time.Millisecond); err == nil {
		t.Fatalf("expected buffered tcp payload to be dropped after freeze, got %s", frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected buffered tcp read to time out, got %v", err)
	}

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("send stream.close after freeze: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream.close send did not complete")
	}

	select {
	case <-copyDone:
	case <-time.After(time.Second):
		t.Fatal("tcp copy goroutine did not exit")
	}
}

func TestServerFreezeGroupRuntimeDropsBufferedUDPData(t *testing.T) {
	server := NewServer(
		Options{
			WriteTimeout: time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	controlClient, controlServer := net.Pipe()
	defer controlClient.Close()
	defer controlServer.Close()

	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer listener.Close()

	session := newTestSessionState(
		GroupRuntime{ID: 1, Name: "group-a", EffectiveIP: system.AnyIPv4},
		ConfigSnapshot{Version: 1},
	)

	tunnel := protocol.TunnelEntry{
		TunnelID:    8,
		Protocol:    protocol.ProtocolUDP,
		TunnelFlags: protocol.TunnelFlagEnabled,
	}
	remotePort := uint16(listener.LocalAddr().(*net.UDPAddr).Port)
	clientAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53000}
	udpSession := newPublicUDPSession(9, tunnel, remotePort, listener, clientAddr, time.Now().UTC())

	session.runtimeMu.Lock()
	session.listenersStarted = true
	session.runtimeGeneration = 1
	session.udpSessions[udpSession.sessionID] = udpSession
	session.udpSessionKeys[udpSession.key()] = udpSession.sessionID
	session.runtimeMu.Unlock()

	session.writeMu.Lock()

	forwardDone := make(chan error, 1)
	go func() {
		forwardDone <- server.handlePublicUDPDatagram(
			controlServer,
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			session,
			1,
			tunnel,
			remotePort,
			listener,
			clientAddr,
			[]byte("late"),
		)
	}()

	listeners, udpListeners, _, udpSessions := session.freezeTunnelRuntime()
	closeStartedTunnelListeners(listeners, udpListeners)

	session.writeMu.Unlock()

	closeDone := make(chan error, 1)
	go func() {
		for _, capturedUDPSession := range udpSessions {
			if err := server.sendUDPClose(controlServer, session, capturedUDPSession.sessionID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil {
				closeDone <- err
				return
			}
		}
		closeDone <- nil
	}()

	closeFrame := readMessage(t, controlClient)
	if closeFrame.Type != protocol.TypeUDPClose {
		t.Fatalf("expected udp.close after freeze, got %s", closeFrame.Type.String())
	}

	if frame, err := readMessageWithin(controlClient, 200*time.Millisecond); err == nil {
		t.Fatalf("expected buffered udp payload to be dropped after freeze, got %s", frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected buffered udp read to time out, got %v", err)
	}

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("send udp.close after freeze: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("udp.close send did not complete")
	}

	select {
	case err := <-forwardDone:
		if err != nil {
			t.Fatalf("forward buffered udp payload: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("udp forward goroutine did not exit")
	}
}

func TestServerRefreshGroupClosesSessionAfterTokenReset(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: system.AnyIPv4,
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
			},
		},
	}

	server := NewServer(
		Options{
			Repository: repo,
			Network:    staticSnapshotReader{},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)
	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil {
		waitForActiveGroupSession(t, server, repo.group.ID)
		active, ok = server.activeSession(repo.group.ID)
	}
	if !ok || active == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	var rotatedSecret [32]byte
	copy(rotatedSecret[:], []byte("fedcba9876543210fedcba9876543210"))
	repo.group.TokenHash = sha256.Sum256(rotatedSecret[:])

	server.RefreshGroup(repo.group.ID)

	if _, err := readMessageWithin(clientConn, time.Second); err == nil {
		t.Fatal("expected refreshed session to close after token reset")
	}

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

func (r stubRepository) LoadGroupRuntimeByID(_ context.Context, _ int64) (GroupRuntime, error) {
	if r.err != nil {
		return GroupRuntime{}, r.err
	}
	return r.group, nil
}

type mutableRepository struct {
	group   GroupRuntime
	loadErr error
}

func (r *mutableRepository) LoadGroupRuntime(_ context.Context, _ [16]byte) (GroupRuntime, error) {
	if r.loadErr != nil {
		return GroupRuntime{}, r.loadErr
	}
	return r.group, nil
}

func (r *mutableRepository) LoadGroupRuntimeByID(_ context.Context, _ int64) (GroupRuntime, error) {
	if r.loadErr != nil {
		return GroupRuntime{}, r.loadErr
	}
	return r.group, nil
}

type blockingRepository struct {
	group           GroupRuntime
	loadErr         error
	loadByIDStarted chan struct{}
	allowLoadByID   chan struct{}
}

func (r *blockingRepository) LoadGroupRuntime(_ context.Context, _ [16]byte) (GroupRuntime, error) {
	if r.loadErr != nil {
		return GroupRuntime{}, r.loadErr
	}
	return r.group, nil
}

func (r *blockingRepository) LoadGroupRuntimeByID(ctx context.Context, _ int64) (GroupRuntime, error) {
	if r.loadErr != nil {
		return GroupRuntime{}, r.loadErr
	}
	if r.loadByIDStarted != nil {
		select {
		case r.loadByIDStarted <- struct{}{}:
		default:
		}
	}
	if r.allowLoadByID != nil {
		select {
		case <-r.allowLoadByID:
		case <-ctx.Done():
			return GroupRuntime{}, ctx.Err()
		}
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

func freeTCPPortExcept(t *testing.T, excluded ...int) int {
	t.Helper()

	blocked := make(map[int]struct{}, len(excluded))
	for _, port := range excluded {
		blocked[port] = struct{}{}
	}

	for attempt := 0; attempt < 100; attempt++ {
		port := freeTCPPort(t)
		if _, exists := blocked[port]; exists {
			continue
		}
		return port
	}

	t.Fatal("failed to allocate distinct tcp port")
	return 0
}

func freeTCPPortRange(t *testing.T, size int) int {
	t.Helper()

	if size <= 0 {
		t.Fatal("tcp port range size must be positive")
	}

	tryRange := func(start int) bool {
		listeners := make([]net.Listener, 0, size)
		for offset := 0; offset < size; offset++ {
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(start+offset))
			listener, err := net.Listen("tcp", addr)
			if err != nil {
				for _, started := range listeners {
					_ = started.Close()
				}
				return false
			}
			listeners = append(listeners, listener)
		}
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return true
	}

	seed := freeTCPPort(t)
	maxStart := 65535 - size + 1
	for start := seed; start <= maxStart; start++ {
		if tryRange(start) {
			return start
		}
	}
	for start := 1024; start < seed; start++ {
		if tryRange(start) {
			return start
		}
	}

	t.Fatalf("failed to allocate contiguous tcp port range of size %d", size)
	return 0
}

func freeUDPPort(t *testing.T) int {
	t.Helper()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	listener, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen for free udp port: %v", err)
	}
	defer listener.Close()
	return listener.LocalAddr().(*net.UDPAddr).Port
}

func freeUDPPortExcept(t *testing.T, excluded ...int) int {
	t.Helper()

	blocked := make(map[int]struct{}, len(excluded))
	for _, port := range excluded {
		blocked[port] = struct{}{}
	}

	for attempt := 0; attempt < 100; attempt++ {
		port := freeUDPPort(t)
		if _, exists := blocked[port]; exists {
			continue
		}
		return port
	}

	t.Fatal("failed to allocate distinct udp port")
	return 0
}

func freeUDPPortRange(t *testing.T, size int) int {
	t.Helper()

	if size <= 0 {
		t.Fatal("udp port range size must be positive")
	}

	tryRange := func(start int) bool {
		listeners := make([]*net.UDPConn, 0, size)
		for offset := 0; offset < size; offset++ {
			listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: start + offset})
			if err != nil {
				for _, started := range listeners {
					_ = started.Close()
				}
				return false
			}
			listeners = append(listeners, listener)
		}
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return true
	}

	seed := freeUDPPort(t)
	maxStart := 65535 - size + 1
	for start := seed; start <= maxStart; start++ {
		if tryRange(start) {
			return start
		}
	}
	for start := 1024; start < seed; start++ {
		if tryRange(start) {
			return start
		}
	}

	t.Fatalf("failed to allocate contiguous udp port range of size %d", size)
	return 0
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

func dialUDPConn(t *testing.T, port int) *net.UDPConn {
	t.Helper()

	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("dial udp 127.0.0.1:%d: %v", port, err)
	}
	return conn
}

func writeUDPAndReadOpenFrame(t *testing.T, controlConn net.Conn, publicConn *net.UDPConn, payload []byte) protocol.Frame {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := publicConn.Write(payload); err != nil {
			t.Fatalf("write udp datagram: %v", err)
		}

		frame, err := readMessageWithin(controlConn, 100*time.Millisecond)
		if err == nil {
			return frame
		}
		if !isTimeoutError(err) && !errors.Is(err, io.EOF) {
			t.Fatalf("read udp.open: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for udp.open: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeUDPToAndReadOpenFrame(t *testing.T, controlConn net.Conn, publicConn *net.UDPConn, remoteAddr *net.UDPAddr, payload []byte) protocol.Frame {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := publicConn.WriteToUDP(payload, remoteAddr); err != nil {
			t.Fatalf("write udp datagram to %s: %v", remoteAddr.String(), err)
		}

		frame, err := readMessageWithin(controlConn, 100*time.Millisecond)
		if err == nil {
			return frame
		}
		if !isTimeoutError(err) && !errors.Is(err, io.EOF) {
			t.Fatalf("read udp.open: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for udp.open: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readMessageWithin(conn net.Conn, timeout time.Duration) (protocol.Frame, error) {
	frameBytes, err := transport.ReadFrame(conn, timeout)
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.ParseFrame(frameBytes)
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func waitForActiveGroupSession(t *testing.T, server *Server, groupID int64) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := server.activeSession(groupID); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for active group session %d", groupID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForIdleConfig(t *testing.T, session *sessionState) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		pendingRequestID, _ := session.configAckState()
		if pendingRequestID == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for config ack to settle")
		}
		time.Sleep(10 * time.Millisecond)
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
		Response:    protocol.ChallengeResponse(tokenHash, challenge.Nonce),
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

func newTestSessionState(group GroupRuntime, snapshot ConfigSnapshot) *sessionState {
	return &sessionState{
		ID:             1,
		Group:          group,
		Snapshot:       snapshot,
		streams:        make(map[uint32]*publicStream),
		udpSessions:    make(map[uint32]*publicUDPSession),
		udpSessionKeys: make(map[string]uint32),
		listeners:      make(map[uint32][]net.Listener),
		udpListeners:   make(map[uint32][]*net.UDPConn),
		done:           make(chan struct{}),
	}
}

type staticSnapshotReader struct {
	snapshot system.Snapshot
}

func (r staticSnapshotReader) Current() system.Snapshot {
	return r.snapshot
}
