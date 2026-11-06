package gateway

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// ReverseProxy owns the byte transfer and close frames. This wrapper only checks
// frame lengths and bounds fragmented messages without buffering or re-encoding.
type frameGuard struct {
	header      [14]byte
	n           int
	need        int
	remaining   uint64
	messageSize uint64
	fragmented  bool
	masked      bool
	limit       uint64
}

func (g *frameGuard) feed(data []byte) error {
	for len(data) > 0 {
		if g.remaining > 0 {
			n := min(uint64(len(data)), g.remaining)
			g.remaining -= n
			data = data[n:]
			continue
		}
		if g.need == 0 {
			g.need = 2
		}
		n := min(len(data), g.need-g.n)
		copy(g.header[g.n:], data[:n])
		g.n += n
		data = data[n:]
		if g.n < g.need {
			continue
		}
		if g.need == 2 {
			if (g.header[1]&0x80 != 0) != g.masked || g.header[0]&0x70 != 0 {
				return errors.New("unsupported websocket framing")
			}
			switch g.header[1] & 0x7f {
			case 126:
				g.need += 2
			case 127:
				g.need += 8
			}
			if g.masked {
				g.need += 4
			}
			if g.n < g.need {
				continue
			}
		}
		length := uint64(g.header[1] & 0x7f)
		switch length {
		case 126:
			length = uint64(binary.BigEndian.Uint16(g.header[2:4]))
		case 127:
			length = binary.BigEndian.Uint64(g.header[2:10])
		}
		opcode := g.header[0] & 0x0f
		fin := g.header[0]&0x80 != 0
		switch opcode {
		case 0, 1, 2:
			if (opcode == 0) != g.fragmented {
				return errors.New("invalid websocket continuation")
			}
			if length > g.limit || g.messageSize > g.limit-length {
				return errors.New("websocket message exceeds limit")
			}
			g.messageSize += length
			g.fragmented = !fin
			if fin {
				g.messageSize = 0
			}
		case 8, 9, 10:
			if !fin || length > 125 {
				return errors.New("invalid websocket control frame")
			}
		default:
			return errors.New("invalid websocket opcode")
		}
		g.remaining = length
		g.n = 0
		g.need = 0
	}
	return nil
}

type guardedSocket struct {
	io.ReadWriteCloser
	in  frameGuard
	out frameGuard
}

func newGuardedSocket(socket io.ReadWriteCloser, limit uint64) *guardedSocket {
	return &guardedSocket{
		ReadWriteCloser: socket, in: frameGuard{limit: limit}, out: frameGuard{limit: limit, masked: true},
	}
}

func (s *guardedSocket) Read(p []byte) (int, error) {
	n, err := s.ReadWriteCloser.Read(p)
	if guardErr := s.in.feed(p[:n]); guardErr != nil {
		return 0, guardErr
	}
	return n, err
}

func (s *guardedSocket) Write(p []byte) (int, error) {
	if err := s.out.feed(p); err != nil {
		return 0, err
	}
	return s.ReadWriteCloser.Write(p)
}

type hijackWriter struct {
	http.ResponseWriter
	ctx context.Context
}

func (w *hijackWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, nil, err
	}
	stop := context.AfterFunc(w.ctx, func() { conn.Close() })
	return &cancelConn{Conn: conn, stop: stop}, rw, nil
}

type cancelConn struct {
	net.Conn
	stop func() bool
}

func (c *cancelConn) Write(p []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func (c *cancelConn) Close() error {
	c.stop()
	return c.Conn.Close()
}
