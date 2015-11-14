package smtpd

import (
	"fmt"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

type Conn struct {
	// Conn is primarily a wrapper around a net.Conn object
	net.Conn

	// Track some mutable for this connection
	IsTLS    bool
	Errors   []error
	User     AuthUser
	FromAddr *mail.Address
	ToAddr   []*mail.Address

	// Configuration options
	MaxSize      int
	ReadTimeout  int64
	WriteTimeout int64

	// internal state
	lock        sync.Mutex
	transaction int

	asTextProto sync.Once
	textProto   *textproto.Conn
}

// tp returns a textproto wrapper for this connection
func (c *Conn) tp() *textproto.Conn {
	c.asTextProto.Do(func() {
		c.textProto = textproto.NewConn(c)
	})
	return c.textProto
}

// StartTX starts a new MAIL transaction
func (c *Conn) StartTX(from *mail.Address) error {
	if c.transaction != 0 {
		return ErrTransaction
	}
	c.transaction = int(time.Now().UnixNano())
	c.FromAddr = from
	return nil
}

// EndTX closes off a MAIL transaction and returns a message object
func (c *Conn) EndTX() error {
	if c.transaction == 0 {
		return ErrTransaction
	}
	c.transaction = 0
	return nil
}

func (c *Conn) Reset() {
	c.User = nil
	c.FromAddr = nil
	c.ToAddr = make([]*mail.Address, 0)
	c.transaction = 0
}

// ReadSMTP pulls a single SMTP command line (ending in a carriage return + newline)
func (c *Conn) ReadSMTP() (string, string, error) {
	if line, err := c.tp().ReadLine(); err == nil {
		var args string
		command := strings.SplitN(line, " ", 2)

		verb := strings.ToUpper(command[0])
		if len(command) > 1 {
			args = command[1]
		}

		return verb, args, nil
	} else {
		return "", "", err
	}
}

// ReadLine reads a single line from the client
func (c *Conn) ReadLine() (string, error) {
	c.SetReadDeadline(time.Now().Add(time.Duration(c.ReadTimeout) * time.Second))
	return c.tp().ReadLine()
}

// ReadData brokers the special case of SMTP data messages
func (c *Conn) ReadData() (string, error) {
	c.SetReadDeadline(time.Now().Add(time.Duration(c.ReadTimeout) * time.Second))
	lines, err := c.tp().ReadDotLines()
	return strings.Join(lines, "\n"), err
}

// WriteSMTP writes a general SMTP line
func (c *Conn) WriteSMTP(code int, message string) error {
	c.SetWriteDeadline(time.Now().Add(time.Duration(c.WriteTimeout) * time.Second))
	_, err := c.Write([]byte(fmt.Sprintf("%v %v", code, message) + "\r\n"))
	return err
}

// WriteEHLO writes an EHLO line, see https://tools.ietf.org/html/rfc2821#section-4.1.1.1
func (c *Conn) WriteEHLO(message string) error {
	c.SetWriteDeadline(time.Now().Add(time.Duration(c.WriteTimeout) * time.Second))
	_, err := c.Write([]byte(fmt.Sprintf("250-%v", message) + "\r\n"))
	return err
}

// WriteOK is a convenience function for sending the default OK response
func (c *Conn) WriteOK() error {
	return c.WriteSMTP(250, "OK")
}
