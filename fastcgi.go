package fcgirt

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
)

func panicf(format string, v ...interface{}) {
	panic(fmt.Sprintf(format, v...))
}

// only the lower byte of the role
const FcgiRoleResponder byte = 1

const FcgiFlagKeepConn byte = 1

type FastCGIRecordType byte
const (
	RecBeginRequest FastCGIRecordType	= 1
	RecAbortRequest						= 2
	RecEndRequest						= 3
	RecParams							= 4
	RecStdin							= 5
	RecStdout							= 6
	RecStderr							= 7
)

type FastCGIParam struct {
	Name string
	Value []byte
}

type FastCGIConn struct {
	c net.Conn
	wr *bufio.Writer
}

type FastCGIResponse struct {
	Stdout io.Reader
	Stderr io.Reader
}

func (c *FastCGIConn) Read(p []byte) (int, error) {
	return c.c.Read(p)
}

func (c *FastCGIConn) WriteByte(b byte) error {
	return c.wr.WriteByte(b)
}

func (c *FastCGIConn) WriteUint16(n int) error {
	if n < 0 {
		panicf("unexpected negative value %d in WriteUint16", n)
	} else if n > 0xFFFF {
		panicf("unexpectedly large value %d in WriteUint16", n)
	}
	err := c.WriteByte(byte((n & 0xFF00) >> 8))
	if err != nil {
		return err
	}
	return c.WriteByte(byte(n & 0xFF))
}

type FastCGIRecord struct {
	Type FastCGIRecordType
	Payload []byte
}

func (c *FastCGIConn) Discard(n int) error {
	discarded := make([]byte, n)
	_, err := io.ReadFull(c.c, discarded)
	_ = discarded
	return err
}

func (c *FastCGIConn) ReadRecord() (*FastCGIRecord, error) {
	header := make([]byte, 8)
	_, err := io.ReadFull(c.c, header)
	if err != nil {
		return nil, err
	}
	if header[0] != 1 {
		return nil, fmt.Errorf("unexpected header version %d", header[0])
	}
	contentLength := int(header[4]) << 8 + int(header[5])
	payload := make([]byte, contentLength)
	_, err = io.ReadFull(c.c, payload)
	if err != nil {
		return nil, err
	}
	paddingLength := header[6]
	err = c.Discard(int(paddingLength))
	if err != nil {
		return nil, err
	}
	return &FastCGIRecord{
		Type: FastCGIRecordType(header[1]),
		Payload: payload,
	}, nil
}

func (c *FastCGIConn) ExpectRecord(typ FastCGIRecordType) (rec *FastCGIRecord, err error) {
	rec, err = c.ReadRecord()
	if err != nil {
		return nil, err
	}
	if rec.Type != typ {
		return nil, fmt.Errorf("unexpected record type %d (was expecting type %d)", rec.Type, typ)
	}
	return rec, nil
}

func (c *FastCGIConn) WriteRecord(typ FastCGIRecordType, data []byte) error {
	// version, always 1
	err := c.WriteByte(byte(1))
	if err != nil {
		return err
	}
	// record type
	err = c.WriteByte(byte(typ))
	if err != nil {
		return err
	}
	// requestid
	err = c.WriteUint16(1)
	if err != nil {
		return err
	}
	// content length
	err = c.WriteUint16(len(data))
	if err != nil {
		return err
	}
	// padding length and a reserved byte
	err = c.WriteUint16(0)
	if err != nil {
		return err
	}
	// data, if any
	if data != nil {
		_, err = c.Write(data)
		if err != nil {
			return err
		}
	}
	return c.Flush()
}

func NewParamStream(params []FastCGIParam) io.Reader {
	buf := &bytes.Buffer{}
	for _, p := range(params) {
		Write14Len(buf, len([]byte(p.Name)))
		Write14Len(buf, len(p.Value))
		buf.Write([]byte(p.Name))
		buf.Write(p.Value)
	}
	return buf
}

func (c *FastCGIConn) Write14Len(length int) error {
	return Write14Len(c.wr, length)
}

func (c *FastCGIConn) WriteStream(typ FastCGIRecordType, r io.Reader, bufsize int) error {
	var buf []byte
	if r == nil {
		goto closeStream
	}

	buf = make([]byte, bufsize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			err = c.WriteRecord(typ, buf[:n])
			if err != nil {
				return err
			}
		}
		if err == io.EOF {
			// done
			goto closeStream
		} else if err != nil {
			return err
		}
	}

closeStream:
	return c.WriteRecord(typ, nil)
}

func (c *FastCGIConn) Write(p []byte) (int, error) {
	return c.wr.Write(p)
}

func (c *FastCGIConn) Flush() error {
	return c.wr.Flush()
}

func (c *FastCGIConn) Close() error {
	c.wr = nil
	return c.c.Close()
}

func NewFastCGIConn(nc net.Conn) *FastCGIConn {
	c := &FastCGIConn{
		c: nc,
		wr: bufio.NewWriter(nc),
	}
	return c
}

func (c *FastCGIConn) Do(stdin io.Reader, params io.Reader) (*FastCGIResponse, error) {
	err := c.WriteRecord(RecBeginRequest, []byte{0,FcgiRoleResponder,FcgiFlagKeepConn,0,0,0,0,0})
	if err != nil {
		return nil, err
	}

	err = c.WriteStream(RecParams, params, 2048)
	if err != nil {
		return nil, err
	}

	// According to the FastCGI spec the app is free to start writing to
	// stdout/stderr without reading all (or indeed any) of its stdin, so we
	// write it in a separate goroutine.
	sinwerr := make(chan error, 1)
	go func() {
		err = c.WriteStream(RecStdin, stdin, 2048)
		sinwerr <- err
		close(sinwerr)
	}()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	response := &FastCGIResponse{Stdout: stdout, Stderr: stderr}
	for {
		rec, err := c.ReadRecord()
		if err != nil {
			return response, err
		}

		switch rec.Type {
			case RecStdout:
				stdout.Write(rec.Payload)
			case RecStderr:
				fmt.Printf("\nSTDERR: %s\n", rec.Payload)
				stderr.Write(rec.Payload)
			case RecEndRequest:
				return response, <-sinwerr
			default:
				return response, fmt.Errorf("unexpected record type %d", rec.Type)
		}
	}
}

type byteWriter interface {
	WriteByte(c byte) error
}

func Write14Len(wr byteWriter, length int) error {
	if length < 0 {
		panicf("unexpected negative length %d", length)
	}

	if length > 0x7F {
		err := wr.WriteByte(byte((length & 0xFF000000) >> 24) | 0x80)
		if err != nil {
			return err
		}
		err = wr.WriteByte(byte((length & 0x00FF0000) >> 16))
		if err != nil {
			return err
		}
		err = wr.WriteByte(byte((length & 0x0000FF00) >>  8))
		if err != nil {
			return err
		}
		return wr.WriteByte(byte((length & 0x000000FF)))
	} else {
		return wr.WriteByte(byte(length))
	}
}

