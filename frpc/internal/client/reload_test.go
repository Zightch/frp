package client

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestClientApplyConfigPushClosesLocalRuntimeBeforeAck(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5000,
				LocalEnd:    5000,
			},
			{
				TunnelID:    8,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 21000,
				RemoteEnd:   21000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5300,
				LocalEnd:    5300,
			},
		},
	})

	streamConn, streamPeer := net.Pipe()
	defer streamPeer.Close()
	if !state.addStream(41, &localStream{conn: streamConn}) {
		t.Fatal("failed to register existing stream")
	}

	udpServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1").To4(), Port: 0})
	if err != nil {
		t.Fatalf("listen udp server: %v", err)
	}
	defer udpServer.Close()

	udpConn, err := net.DialUDP("udp", nil, udpServer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial udp server: %v", err)
	}
	defer udpConn.Close()

	if !state.addUDPSession(52, &localUDPSession{
		conn:   udpConn,
		target: udpServer.LocalAddr().String(),
		open: protocol.UDPOpen{
			TunnelID:   8,
			RemotePort: 21000,
		},
	}) {
		t.Fatal("failed to register existing udp session")
	}

	pushBody, err := protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: 2,
		GeneratedAtMs: 1234,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  6000,
				LocalEnd:    6000,
			},
			{
				TunnelID:    8,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 21000,
				RemoteEnd:   21000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  6300,
				LocalEnd:    6300,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal config.push: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.applyConfigPush(clientConn, state, protocol.Frame{
			Type:      protocol.TypeConfigPush,
			RequestID: 7,
			Body:      pushBody,
		})
	}()

	waitForPipeEOF(t, streamPeer)
	waitForUDPConnClosed(t, udpConn)

	ackFrame := readFrame(t, serverConn)
	if ackFrame.Type != protocol.TypeConfigAck {
		t.Fatalf("expected config.ack, got %s", ackFrame.Type.String())
	}
	if ackFrame.RequestID != 7 || ackFrame.StreamID != 0 {
		t.Fatalf("unexpected config.ack header: requestId=%d streamId=%d", ackFrame.RequestID, ackFrame.StreamID)
	}

	ack, err := protocol.UnmarshalConfigAck(ackFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.ack: %v", err)
	}
	if ack.Status != protocol.StatusOK || ack.ConfigVersion != 2 {
		t.Fatalf("unexpected config.ack: %#v", ack)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("apply config.push: %v", err)
	}

	snapshot := state.snapshotValue()
	if snapshot.ConfigVersion != 2 {
		t.Fatalf("unexpected config version after reload: %d", snapshot.ConfigVersion)
	}
	if state.activeStreams.Load() != 0 {
		t.Fatalf("unexpected active stream count: %d", state.activeStreams.Load())
	}
	if state.activeUDPSessions.Load() != 0 {
		t.Fatalf("unexpected active udp session count: %d", state.activeUDPSessions.Load())
	}
	if state.lastAckedConfigVersion.Load() != 2 {
		t.Fatalf("unexpected last acked config version: %d", state.lastAckedConfigVersion.Load())
	}
}

func TestClientReadLoopUsesReloadedSnapshotForNewStreams(t *testing.T) {
	newListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen reload target: %v", err)
	}
	defer newListener.Close()

	newTargetDone := make(chan error, 1)
	go func() {
		conn, err := newListener.Accept()
		if err != nil {
			newTargetDone <- err
			return
		}
		defer conn.Close()

		buffer := make([]byte, 16)
		n, err := conn.Read(buffer)
		if err != nil {
			newTargetDone <- err
			return
		}
		if string(buffer[:n]) != "ping" {
			newTargetDone <- errors.New("unexpected payload delivered to reloaded target")
			return
		}
		if _, err := conn.Write([]byte("reloaded")); err != nil {
			newTargetDone <- err
			return
		}
		newTargetDone <- nil
	}()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	state := newSessionState(1000)
	oldPort := freeTCPPort(t)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  uint16(oldPort),
				LocalEnd:    uint16(oldPort),
			},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readDone := make(chan error, 1)
	go func() {
		readDone <- client.readLoop(ctx, clientConn, state)
	}()

	pushBody, err := protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: 2,
		GeneratedAtMs: 5678,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  uint16(newListener.Addr().(*net.TCPAddr).Port),
				LocalEnd:    uint16(newListener.Addr().(*net.TCPAddr).Port),
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal config.push: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:      protocol.TypeConfigPush,
		RequestID: 3,
		Body:      pushBody,
	})

	ackFrame := readFrame(t, serverConn)
	if ackFrame.Type != protocol.TypeConfigAck {
		t.Fatalf("expected config.ack, got %s", ackFrame.Type.String())
	}
	ack, err := protocol.UnmarshalConfigAck(ackFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.ack: %v", err)
	}
	if ack.Status != protocol.StatusOK || ack.ConfigVersion != 2 {
		t.Fatalf("unexpected config.ack: %#v", ack)
	}

	openBody, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   7,
		RemotePort: 20000,
		ClientAddr: protocol.SockAddr{
			IP:   net.ParseIP("203.0.113.50").To4(),
			Port: 40000,
		},
		OpenedAtMs: 9999,
	})
	if err != nil {
		t.Fatalf("marshal stream.open: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:      protocol.TypeStreamOpen,
		RequestID: 4,
		StreamID:  70,
		Body:      openBody,
	})

	openedFrame := readFrame(t, serverConn)
	if openedFrame.Type != protocol.TypeStreamOpened {
		t.Fatalf("expected stream.opened, got %s", openedFrame.Type.String())
	}
	opened, err := protocol.UnmarshalStreamOpened(openedFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal stream.opened: %v", err)
	}
	if opened.Status != protocol.StatusOK {
		t.Fatalf("unexpected stream.opened: %#v", opened)
	}

	writeFrame(t, serverConn, protocol.Frame{
		Type:     protocol.TypeStreamData,
		StreamID: 70,
		Body:     []byte("ping"),
	})

	dataFrame := readFrame(t, serverConn)
	if dataFrame.Type != protocol.TypeStreamData {
		t.Fatalf("expected stream.data, got %s", dataFrame.Type.String())
	}
	if string(dataFrame.Body) != "reloaded" {
		t.Fatalf("unexpected payload from reloaded target: %q", string(dataFrame.Body))
	}

	closeFrame := readFrame(t, serverConn)
	if closeFrame.Type != protocol.TypeStreamClose {
		t.Fatalf("expected stream.close, got %s", closeFrame.Type.String())
	}

	select {
	case err := <-newTargetDone:
		if err != nil {
			t.Fatalf("reloaded target: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reloaded target did not finish")
	}

	cancel()
	_ = clientConn.Close()

	select {
	case err := <-readDone:
		if err != nil && !isNetClosed(err) {
			t.Fatalf("read loop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not exit")
	}
}

func TestSummarizeConfigReloadMarksRatePolicyChangeAsReplacement(t *testing.T) {
	previous := protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{{
			TunnelID:    7,
			Protocol:    protocol.ProtocolTCP,
			TunnelFlags: protocol.TunnelFlagEnabled,
			RemoteStart: 20000,
			RemoteEnd:   20000,
			LocalHost:   mustHost(t, "127.0.0.1"),
			LocalStart:  5000,
			LocalEnd:    5000,
			RatePolicy: protocol.TunnelRatePolicy{
				PolicyID:    9,
				Mode:        protocol.RatePolicyModeIndependent,
				DownlinkBPS: 10_000_000,
				UplinkBPS:   5_000_000,
			},
		}},
	}

	next := previous
	next.Tunnels = append([]protocol.TunnelEntry(nil), previous.Tunnels...)
	next.Tunnels[0].RatePolicy.DownlinkBPS = 20_000_000

	summary := summarizeConfigReload(previous, next)
	if summary.replacedTunnels != 1 {
		t.Fatalf("expected rate policy change to replace tunnel, got %#v", summary)
	}
	if summary.unchangedTunnels != 0 || summary.addedTunnels != 0 || summary.removedTunnels != 0 {
		t.Fatalf("unexpected summary for rate policy reload: %#v", summary)
	}
}

func waitForPipeEOF(t *testing.T, conn net.Conn) {
	t.Helper()

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err := conn.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected pipe EOF after reload cleanup, got %v", err)
	}
}

func waitForUDPConnClosed(t *testing.T, conn *net.UDPConn) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		_, err := conn.Write([]byte("x"))
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("udp session was not closed before config.ack")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free tcp port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
