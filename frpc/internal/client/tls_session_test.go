package client

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestClientRunSessionKeepsTLSConnAfterLogin(t *testing.T) {
	credentials := testCredentials(t)

	certPEM, pair := newTLSServerCertificate(t)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("append tls test certificate to root pool")
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")
	client.buildTLSConfig = func(host string) (*tls.Config, error) {
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: host,
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer serverConn.Close()

		helloFrame := readFrame(t, serverConn)
		if helloFrame.Type != protocol.TypeTransportClientHello {
			t.Errorf("expected transport.client_hello, got %s", helloFrame.Type.String())
			return
		}
		hello, err := protocol.UnmarshalTransportClientHello(helloFrame.Body)
		if err != nil {
			t.Errorf("unmarshal transport.client_hello: %v", err)
			return
		}
		if hello.ClientID != credentials.ClientID {
			t.Error("unexpected client id in transport.client_hello")
			return
		}

		serverHelloBody, err := protocol.MarshalTransportServerHello(protocol.TransportServerHello{
			SelectedSecurityMode: protocol.TransportSecurityModeTLS,
			CapabilityBits:       0,
		})
		if err != nil {
			t.Errorf("marshal transport.server_hello: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeTransportServerHello,
			RequestID: helloFrame.RequestID,
			Body:      serverHelloBody,
		})

		tlsServer := tls.Server(serverConn, &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{pair},
		})
		if err := tlsServer.Handshake(); err != nil {
			t.Errorf("server tls handshake: %v", err)
			return
		}

		frame := readFrame(t, tlsServer)
		if frame.Type != protocol.TypeAuthBegin {
			t.Errorf("expected auth.begin, got %s", frame.Type.String())
			return
		}

		challenge := protocol.AuthChallenge{
			ChallengeID: 7,
			ExpiresInMs: 5000,
		}
		copy(challenge.Nonce[:], []byte("nonce-1234567890"))
		challengeBody, err := protocol.MarshalAuthChallenge(challenge)
		if err != nil {
			t.Errorf("marshal auth.challenge: %v", err)
			return
		}
		writeFrame(t, tlsServer, protocol.Frame{
			Type:      protocol.TypeAuthChallenge,
			RequestID: frame.RequestID,
			Body:      challengeBody,
		})

		frame = readFrame(t, tlsServer)
		if frame.Type != protocol.TypeAuthFinish {
			t.Errorf("expected auth.finish, got %s", frame.Type.String())
			return
		}
		finish, err := protocol.UnmarshalAuthFinish(frame.Body)
		if err != nil {
			t.Errorf("unmarshal auth.finish: %v", err)
			return
		}
		secretHash := sha256.Sum256(credentials.ClientSecret[:])
		expected := protocol.ChallengeResponse(secretHash, challenge.Nonce)
		if finish.Response != expected {
			t.Error("unexpected auth response")
			return
		}

		helloBody, err := protocol.MarshalServerHello(protocol.ServerHello{
			HeartbeatIntervalMs: 50,
			SessionID:           11,
			ServerVersion:       "test-server",
		})
		if err != nil {
			t.Errorf("marshal server.hello: %v", err)
			return
		}
		writeFrame(t, tlsServer, protocol.Frame{
			Type:      protocol.TypeServerHello,
			RequestID: frame.RequestID,
			Body:      helloBody,
		})

		configPushBody, err := protocol.MarshalConfigPush(protocol.ConfigPush{
			ConfigVersion: 99,
			GeneratedAtMs: 1234,
		})
		if err != nil {
			t.Errorf("marshal config.push: %v", err)
			return
		}
		writeFrame(t, tlsServer, protocol.Frame{
			Type:      protocol.TypeConfigPush,
			RequestID: 2147483648,
			Body:      configPushBody,
		})

		frame = readFrame(t, tlsServer)
		if frame.Type != protocol.TypeConfigAck {
			t.Errorf("expected config.ack, got %s", frame.Type.String())
			return
		}

		frame = readFrame(t, tlsServer)
		if frame.Type != protocol.TypeHeartbeatPing {
			t.Errorf("expected heartbeat.ping, got %s", frame.Type.String())
			return
		}
		ping, err := protocol.UnmarshalHeartbeatPing(frame.Body)
		if err != nil {
			t.Errorf("unmarshal heartbeat.ping: %v", err)
			return
		}

		pongBody, err := protocol.MarshalHeartbeatPong(protocol.HeartbeatPong{
			ClientUnixMs: ping.ClientUnixMs,
			ServerUnixMs: uint64(time.Now().UTC().UnixMilli()),
		})
		if err != nil {
			t.Errorf("marshal heartbeat.pong: %v", err)
			return
		}
		writeFrame(t, tlsServer, protocol.Frame{
			Type:      protocol.TypeHeartbeatPong,
			RequestID: frame.RequestID,
			Body:      pongBody,
		})

		cancel()
		<-ctx.Done()
	}()

	if err := client.runSession(ctx, clientConn, credentials); err != nil {
		t.Fatalf("run session: %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("mock tls server did not exit")
	}
}

func newTLSServerCertificate(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate tls test key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create tls test certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build tls test key pair: %v", err)
	}
	return certPEM, pair
}
