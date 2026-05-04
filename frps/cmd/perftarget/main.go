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

type transferPattern struct {
	writeChunkBytes int
	burstChunks     int
	burstPause      time.Duration
}

type commandSpec struct {
	name      string
	byteCount int64
	pattern   transferPattern
}

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

	command, err := parseCommand(commandLine)
	if err != nil {
		log.Printf("bad command remote=%s error=%v", conn.RemoteAddr().String(), err)
		return
	}

	switch command.name {
	case "ECHO":
		written, err := echoPayload(conn, reader, command.byteCount)
		if err != nil {
			log.Printf("echo failed remote=%s error=%v", conn.RemoteAddr().String(), err)
			return
		}
		if written != command.byteCount {
			log.Printf("echo short read remote=%s got=%d want=%d", conn.RemoteAddr().String(), written, command.byteCount)
		}
	case "UPLOAD":
		written, err := io.CopyBuffer(io.Discard, io.LimitReader(reader, command.byteCount), make([]byte, chunkSize))
		if err != nil {
			log.Printf("upload failed remote=%s error=%v", conn.RemoteAddr().String(), err)
			return
		}
		if written != command.byteCount {
			log.Printf("upload short read remote=%s got=%d want=%d", conn.RemoteAddr().String(), written, command.byteCount)
			return
		}
		if _, err := io.WriteString(conn, fmt.Sprintf("OK %d\n", command.byteCount)); err != nil {
			log.Printf("upload ack failed remote=%s error=%v", conn.RemoteAddr().String(), err)
		}
	case "DOWNLOAD":
		if err := writePatternedDownload(conn, command.byteCount, command.pattern); err != nil {
			log.Printf("download failed remote=%s error=%v", conn.RemoteAddr().String(), err)
			return
		}
	case "SINK":
		written, err := io.CopyBuffer(io.Discard, io.LimitReader(reader, command.byteCount), make([]byte, chunkSize))
		if err != nil {
			log.Printf("sink failed remote=%s error=%v", conn.RemoteAddr().String(), err)
			return
		}
		if written != command.byteCount {
			log.Printf("sink short read remote=%s got=%d want=%d", conn.RemoteAddr().String(), written, command.byteCount)
			return
		}
	default:
		log.Printf("unsupported command remote=%s command=%s", conn.RemoteAddr().String(), command.name)
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

func writePatternedDownload(conn net.Conn, byteCount int64, pattern transferPattern) error {
	chunk := make([]byte, pattern.writeChunkBytes)
	for i := range chunk {
		chunk[i] = 'x'
	}
	remaining := byteCount
	sentChunks := 0
	for remaining > 0 {
		payload := chunk
		if remaining < int64(len(payload)) {
			payload = payload[:remaining]
		}
		if err := writeFull(conn, payload); err != nil {
			return err
		}
		remaining -= int64(len(payload))
		sentChunks = maybePauseAfterBurst(pattern, sentChunks, remaining)
	}
	return nil
}

func maybePauseAfterBurst(pattern transferPattern, sentChunks int, remaining int64) int {
	if pattern.burstChunks <= 0 {
		return sentChunks
	}

	sentChunks++
	if sentChunks < pattern.burstChunks || remaining <= 0 {
		return sentChunks
	}

	if pattern.burstPause > 0 {
		time.Sleep(pattern.burstPause)
	}
	return 0
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

func parseCommand(line string) (commandSpec, error) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return commandSpec{}, fmt.Errorf("missing byte count")
	}
	byteCount, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return commandSpec{}, fmt.Errorf("parse byte count: %w", err)
	}
	if byteCount < 0 {
		return commandSpec{}, fmt.Errorf("byte count must be non-negative")
	}

	pattern := transferPattern{
		writeChunkBytes: chunkSize,
	}
	if len(fields) >= 3 {
		writeChunkBytes, err := strconv.Atoi(fields[2])
		if err != nil {
			return commandSpec{}, fmt.Errorf("parse write chunk bytes: %w", err)
		}
		pattern.writeChunkBytes = writeChunkBytes
	}
	if len(fields) >= 4 {
		burstChunks, err := strconv.Atoi(fields[3])
		if err != nil {
			return commandSpec{}, fmt.Errorf("parse burst chunks: %w", err)
		}
		pattern.burstChunks = burstChunks
	}
	if len(fields) >= 5 {
		burstPauseMicros, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			return commandSpec{}, fmt.Errorf("parse burst pause micros: %w", err)
		}
		pattern.burstPause = time.Duration(burstPauseMicros) * time.Microsecond
	}
	if pattern.writeChunkBytes <= 0 {
		return commandSpec{}, fmt.Errorf("write chunk bytes must be positive")
	}
	if pattern.burstChunks < 0 {
		return commandSpec{}, fmt.Errorf("burst chunks must be non-negative")
	}
	if pattern.burstPause < 0 {
		return commandSpec{}, fmt.Errorf("burst pause must be non-negative")
	}

	return commandSpec{
		name:      strings.ToUpper(fields[0]),
		byteCount: byteCount,
		pattern:   pattern,
	}, nil
}
