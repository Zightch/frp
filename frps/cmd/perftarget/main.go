package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	lineLimitBytes = 256
	chunkSize      = 64 * 1024
)

func main() {
	listenAddr := flag.String("listen", "", "listen address, for example 127.0.0.1:9000")
	readTimeout := flag.Duration("read-timeout", 60*time.Second, "per-connection read timeout")
	writeTimeout := flag.Duration("write-timeout", 60*time.Second, "per-connection write timeout")
	flag.Parse()

	if strings.TrimSpace(*listenAddr) == "" {
		log.Fatal("listen address is required")
	}

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", *listenAddr, err)
	}
	defer listener.Close()

	log.Printf("perf target listening addr=%s", listener.Addr().String())
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept failed: %v", err)
			continue
		}
		go handleConn(conn, *readTimeout, *writeTimeout)
	}
}

func handleConn(conn net.Conn, readTimeout, writeTimeout time.Duration) {
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))

	reader := bufio.NewReaderSize(conn, chunkSize)
	commandLine, err := readLineLimited(reader, lineLimitBytes)
	if err != nil {
		if err != io.EOF {
			log.Printf("read command failed remote=%s error=%v", conn.RemoteAddr().String(), err)
		}
		return
	}

	command, byteCount, err := parseCommand(commandLine)
	if err != nil {
		log.Printf("bad command remote=%s error=%v", conn.RemoteAddr().String(), err)
		return
	}

	switch command {
	case "ECHO":
		written, err := echoPayload(conn, reader, byteCount)
		if err != nil {
			log.Printf("echo failed remote=%s error=%v", conn.RemoteAddr().String(), err)
			return
		}
		if written != byteCount {
			log.Printf("echo short read remote=%s got=%d want=%d", conn.RemoteAddr().String(), written, byteCount)
		}
	case "UPLOAD":
		written, err := io.CopyBuffer(io.Discard, io.LimitReader(reader, byteCount), make([]byte, chunkSize))
		if err != nil {
			log.Printf("upload failed remote=%s error=%v", conn.RemoteAddr().String(), err)
			return
		}
		if written != byteCount {
			log.Printf("upload short read remote=%s got=%d want=%d", conn.RemoteAddr().String(), written, byteCount)
			return
		}
		if _, err := io.WriteString(conn, fmt.Sprintf("OK %d\n", byteCount)); err != nil {
			log.Printf("upload ack failed remote=%s error=%v", conn.RemoteAddr().String(), err)
		}
	case "DOWNLOAD":
		chunk := make([]byte, chunkSize)
		for i := range chunk {
			chunk[i] = 'x'
		}
		remaining := byteCount
		for remaining > 0 {
			payload := chunk
			if remaining < int64(len(payload)) {
				payload = payload[:remaining]
			}
			n, err := conn.Write(payload)
			if err != nil {
				log.Printf("download failed remote=%s error=%v", conn.RemoteAddr().String(), err)
				return
			}
			remaining -= int64(n)
		}
	default:
		log.Printf("unsupported command remote=%s command=%s", conn.RemoteAddr().String(), command)
	}
}

func echoPayload(conn net.Conn, reader *bufio.Reader, byteCount int64) (int64, error) {
	buffer := make([]byte, chunkSize)
	var written int64
	remaining := byteCount
	for remaining > 0 {
		payload := buffer
		if remaining < int64(len(payload)) {
			payload = payload[:remaining]
		}
		n, err := io.ReadFull(reader, payload)
		if n > 0 {
			if writeErr := writeFull(conn, payload[:n]); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
			remaining -= int64(n)
		}
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func writeFull(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		n, err := conn.Write(payload)
		if err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

func readLineLimited(reader *bufio.Reader, limit int) (string, error) {
	var builder strings.Builder
	for builder.Len() < limit {
		b, err := reader.ReadByte()
		if err != nil {
			if err == io.EOF && builder.Len() > 0 {
				return builder.String(), nil
			}
			return "", err
		}
		builder.WriteByte(b)
		if b == '\n' {
			break
		}
	}
	return builder.String(), nil
}

func parseCommand(line string) (string, int64, error) {
	trimmed := strings.TrimSpace(line)
	command, value, found := strings.Cut(trimmed, " ")
	if !found {
		return "", 0, fmt.Errorf("missing byte count")
	}
	byteCount, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("parse byte count: %w", err)
	}
	if byteCount < 0 {
		return "", 0, fmt.Errorf("byte count must be non-negative")
	}
	return strings.ToUpper(strings.TrimSpace(command)), byteCount, nil
}
