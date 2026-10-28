// Package ibwire is the framing layer of the Interactive Brokers TWS socket
// API, shared by internal/twslink (display groups, client 77) and
// internal/orderticket (client 80). It knows nothing about message contents:
// only the "API\0" greeting, the client version range, and the
// 4-byte-big-endian-length + NUL-terminated-fields frame.
package ibwire

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// ClientVersionRange is what both clients offer; TWS answers with the highest
// server version it supports inside the range (176 on a current TWS).
const ClientVersionRange = "v100..176"

// Encode frames one message: 4-byte big-endian length + NUL-terminated fields.
func Encode(fields ...any) []byte {
	var b strings.Builder
	for _, f := range fields {
		switch v := f.(type) {
		case string:
			b.WriteString(v)
		case int:
			b.WriteString(strconv.Itoa(v))
		case int64:
			b.WriteString(strconv.FormatInt(v, 10))
		default:
			b.WriteString(fmt.Sprint(v))
		}
		b.WriteByte(0)
	}
	payload := []byte(b.String())
	out := make([]byte, 4, 4+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(payload)))
	return append(out, payload...)
}

// ReadMsg reads one length-prefixed message and splits its NUL-terminated
// fields. The trailing empty field after the final separator is dropped.
func ReadMsg(r *bufio.Reader) ([]string, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > 8<<20 {
		return nil, fmt.Errorf("bad message length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	parts := strings.Split(string(buf), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts, nil
}

// Handshake sends the "API\0" greeting + version range and returns the server
// version. It reads through the caller's reader — never its own — so bytes
// TWS packed after the greeting reply stay buffered for the caller.
func Handshake(conn net.Conn, r *bufio.Reader, timeout time.Duration) (int, error) {
	if _, err := conn.Write([]byte("API\x00")); err != nil {
		return 0, err
	}
	v := []byte(ClientVersionRange)
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(v)))
	if _, err := conn.Write(append(hdr, v...)); err != nil {
		return 0, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	fields, err := ReadMsg(r)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return 0, fmt.Errorf("handshake: %w", err)
	}
	if len(fields) < 1 {
		return 0, fmt.Errorf("handshake: empty reply")
	}
	return strconv.Atoi(fields[0])
}
