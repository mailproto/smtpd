package smtpd

import (
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"time"
)

// MessageHandler functions handle application of business logic to the inbound message
type MessageHandler func(m *Message) error

// Default values
const (
	DefaultReadTimeout        = time.Second * 10
	DefaultWriteTimeout       = time.Second * 10
	DefaultMessageSizeMax     = 131072
	DefaultSessionCommandsMax = 100
)

// Server is an RFC2821/5321 compatible SMTP server
type Server struct {
	Name string

	TLSConfig  *tls.Config
	ServerName string

	// MaxSize of incoming message objects, zero for no cap otherwise
	// larger messages are thrown away
	MaxSize int64

	// MaxConn limits the number of concurrent connections being handled
	MaxConn int

	// MaxCommands is the maximum number of commands a server will accept
	// from a single client before terminating the session
	MaxCommands int

	// RateLimiter gets called before proceeding through to message handling
	// TODO: Implement
	RateLimiter func(*Conn) bool

	// Handler is the handoff function for messages
	Handler MessageHandler

	// Auth is an authentication-handling extension
	Auth Extension

	// Extensions is a map of server-specific extensions & overrides, by verb
	Extensions map[string]Extension

	// Disabled features
	Disabled map[string]bool

	// Server meta
	listener *net.Listener

	// help message to display in response to a HELP request
	Help string

	// Logger to print out status info
	// TODO: implement better logging with configurable verbosity
	Logger *log.Logger

	// Timeout handlers
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	// Ready is a channel that will receive a single `true` when the server has started
	Ready chan bool
}

// NewServer creates a server with the default settings
func NewServer(handler func(*Message) error) *Server {
	return NewServerWithLogger(handler, log.New(os.Stdout, "smtpd ", 0))
}

// NewServerWithLogger creates a server with a customer logger
func NewServerWithLogger(handler func(*Message) error, logger *log.Logger) *Server {
	name, err := os.Hostname()
	if err != nil {
		name = "localhost"
	}
	return &Server{
		Name:         name,
		ServerName:   name,
		MaxSize:      DefaultMessageSizeMax,
		MaxCommands:  DefaultSessionCommandsMax,
		Handler:      handler,
		Extensions:   make(map[string]Extension),
		Disabled:     make(map[string]bool),
		Logger:       logger,
		ReadTimeout:  DefaultReadTimeout,
		WriteTimeout: DefaultWriteTimeout,
		Ready:        make(chan bool, 1),
	}
}

// Close the server connection
func (s *Server) Close() error {
	return (*s.listener).Close()
}

// Greeting is a humanized response to EHLO to precede the list of available commands
func (s *Server) Greeting(conn *Conn) string {
	return fmt.Sprintf("Welcome! [%v]", conn.LocalAddr())
}

// Extend the server to handle the supplied verb
func (s *Server) Extend(verb string, extension Extension) error {
	if _, ok := s.Extensions[verb]; ok {
		return fmt.Errorf("Extension for %v has already been registered", verb)
	}

	s.Extensions[verb] = extension
	return nil
}

// Disable server capabilities
func (s *Server) Disable(verbs ...string) {
	for _, verb := range verbs {
		s.Disabled[strings.ToUpper(verb)] = true
	}
}

// Enable server capabilities that have previously been disabled
func (s *Server) Enable(verbs ...string) {
	for _, verb := range verbs {
		s.Disabled[strings.ToUpper(verb)] = false
	}
}

// UseTLS tries to enable TLS on the server (can also just explicitly set the TLSConfig)
func (s *Server) UseTLS(cert, key string) error {
	c, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return fmt.Errorf("Could not load TLS keypair, %v", err)
	}
	s.TLSConfig = &tls.Config{
		Certificates: []tls.Certificate{c},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		Rand:         rand.Reader,
		ServerName:   s.ServerName,
	}
	return nil
}

// UseAuth assigns the server authentication extension
func (s *Server) UseAuth(auth Extension) {
	s.Auth = auth
}

// SetHelp sets a help message
func (s *Server) SetHelp(message string) error {
	if len(message) > 100 || strings.TrimSpace(message) == "" {
		return fmt.Errorf("Message '%v' is not a valid HELP message. Must be less than 100 characters and non-empty", message)
	}
	s.Help = message
	return nil
}

// ListenAndServe starts listening for SMTP commands at the supplied TCP address
func (s *Server) ListenAndServe(addr string) error {

	if s.listener != nil {
		return ErrAlreadyRunning
	}

	// close the Ready channel on exit
	defer func() {
		close(s.Ready)
	}()

	// Start listening for SMTP connections
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		s.Logger.Printf("Cannot listen on %v (%v)", addr, err)
		return err
	}
	s.Ready <- true

	var clientID int64 = 1

	s.listener = &listener

	// @TODO maintain a fixed-size connection pool, throw immediate 554s otherwise
	// see http://www.greenend.org.uk/rjk/tech/smtpreplies.html
	// maybe also pass around a context? https://blog.golang.org/context
	for {

		conn, err := listener.Accept()

		if netErr, ok := err.(*net.OpError); ok && netErr.Timeout() {
			// it was a timeout
			continue
		} else if ok && !netErr.Temporary() {
			return netErr
		}

		if err != nil {
			log.Println("Could not handle request:", err)
			continue
		}

		c := &Conn{
			Conn: conn,
			// TODO: implement ListenAndServeSSL for :465 servers
			IsTLS:        false,
			Errors:       []error{},
			MaxSize:      s.MaxSize,
			ReadTimeout:  s.ReadTimeout,
			WriteTimeout: s.WriteTimeout,
		}

		c.SetReadDeadline(time.Now().Add(s.ReadTimeout))
		c.SetWriteDeadline(time.Now().Add(s.WriteTimeout))

		go s.HandleSMTP(c)
		clientID++

	}
}

// Address retrieves the address of the server
func (s *Server) Address() string {
	if s.listener != nil {
		return (*s.listener).Addr().String()
	}
	return ""
}

func (s *Server) handleMessage(m *Message) error {
	return s.Handler(m)
}

// HandleSMTP handles a single SMTP request
func (s *Server) HandleSMTP(conn *Conn) error {
	defer conn.Close()
	conn.WriteSMTP(220, fmt.Sprintf("%v %v", s.Name, time.Now().Format(time.RFC1123Z)))

ReadLoop:
	for i := 0; i < s.MaxCommands; i++ {

		var verb, args string
		var err error

		if verb, args, err = conn.ReadSMTP(); err != nil {
			s.Logger.Printf("Read error: %v", err)
			if err == io.EOF {
				// client closed the connection already
				break ReadLoop
			}
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				// too slow, timeout
				break ReadLoop
			}

			return err
		}

		s.Logger.Printf("%v %v", verb, args)

		// Always check for disabled features first
		if s.Disabled[verb] {
			if verb == "EHLO" {
				conn.WriteSMTP(550, "Not implemented")
			} else {
				conn.WriteSMTP(502, "Command not implemented")
			}
			continue
		}

		// Auth overrides
		if s.Auth != nil && conn.User == nil {
			switch verb {
			case "AUTH", "EHLO", "HELO", "NOOP", "RSET", "QUIT", "STARTTLS":
				// these are okay to call without authentication on an Auth-enabled server
			case "*":
				conn.WriteSMTP(501, "Cancelled")
				continue
			default:
				conn.WriteSMTP(530, "Authentication required")
				continue
			}
		}

		// Handle any extensions / overrides before running default logic
		if _, ok := s.Extensions[verb]; ok {
			err := s.Extensions[verb].Handle(conn, args)
			if err != nil {
				s.Logger.Printf("Error? %v", err)
			}
			continue
		}

		switch verb {
		// https://tools.ietf.org/html/rfc2821#section-4.1.1.1
		case "HELO":
			conn.WriteSMTP(250, fmt.Sprintf("%v Hello", s.ServerName))
		case "EHLO":
			// see: https://tools.ietf.org/html/rfc2821#section-4.1.4
			conn.Reset()

			conn.WriteEHLO(fmt.Sprintf("%v %v", s.ServerName, s.Greeting(conn)))
			conn.WriteEHLO(fmt.Sprintf("SIZE %v", s.MaxSize))
			if !conn.IsTLS && s.TLSConfig != nil {
				conn.WriteEHLO("STARTTLS")
			}
			if conn.User == nil && s.Auth != nil {
				conn.WriteEHLO(fmt.Sprintf("AUTH %v", s.Auth.EHLO()))
			}
			for verb, extension := range s.Extensions {
				conn.WriteEHLO(fmt.Sprintf("%v %v", verb, extension.EHLO()))
			}
			conn.WriteSMTP(250, "HELP")
		// The MAIL command starts off a new mail transaction
		// see: https://tools.ietf.org/html/rfc2821#section-4.1.1.2
		// This doesn't implement the RFC4594 addition of an AUTH param to the MAIL command
		// see: http://tools.ietf.org/html/rfc4954#section-3 for details
		case "MAIL":
			if from, err := s.GetAddressArg("FROM", args); err == nil {
				if conn.User == nil || conn.User.IsUser(from.Address) {
					if err := conn.StartTX(from); err == nil {
						conn.WriteSMTP(250, "Accepted")
					} else {
						conn.WriteSMTP(501, err.Error())
					}
				} else {
					conn.WriteSMTP(501, fmt.Sprintf("Cannot send mail as %v", from))
				}
			} else {
				conn.WriteSMTP(501, err.Error())
			}
		// https://tools.ietf.org/html/rfc2821#section-4.1.1.3
		case "RCPT":
			// TODO: bubble these up to the message,
			if to, err := s.GetAddressArg("TO", args); err == nil {
				conn.ToAddr = append(conn.ToAddr, to)
				conn.WriteSMTP(250, "Accepted")
			} else {
				conn.WriteSMTP(501, err.Error())
			}
		// https://tools.ietf.org/html/rfc2821#section-4.1.1.4
		case "DATA":
			conn.WriteSMTP(354, "Enter message, ending with \".\" on a line by itself")

			if data, err := conn.ReadData(); err == nil {
				if message, err := NewMessage([]byte(data), conn.ToAddr, s.Logger); err == nil && (conn.EndTX() == nil) {
					if err := s.handleMessage(message); err == nil {
						conn.WriteSMTP(250, fmt.Sprintf("OK : queued as %v", message.ID()))
					} else if serr, ok := err.(SMTPError); ok {
						conn.WriteSMTP(serr.Code, serr.Error())
					} else {
						conn.WriteSMTP(554, fmt.Sprintf("Error: I blame me. %v", err))
					}

				} else {
					conn.WriteSMTP(554, fmt.Sprintf("Error: I blame you. %v", err))
				}

			} else {
				s.Logger.Printf("DATA read error: %v", err)
			}
		// Reset the connection
		// see: https://tools.ietf.org/html/rfc2821#section-4.1.1.5
		case "RSET":
			conn.Reset()
			conn.WriteOK()

		// Since this is a commonly abused SPAM aid, it's better to just
		// default to 252 (apparent validity / could not verify). If this is not a concern, then
		// the full `params` value will be the address to verify, respond with `conn.WriteOK()`
		// see: https://tools.ietf.org/html/rfc2821#section-4.1.1.6
		case "VRFY":
			conn.WriteSMTP(252, "But it was worth a shot, right?")

		// see: https://tools.ietf.org/html/rfc2821#section-4.1.1.7
		case "EXPN":
			conn.WriteSMTP(252, "Maybe, maybe not")

		// see: https://tools.ietf.org/html/rfc2821#section-4.1.1.8
		case "HELP":
			msg := fmt.Sprintf("contact the owner of %v for more information", s.ServerName)
			if s.Help != "" {
				msg = s.Help
			}
			conn.WriteSMTP(214, msg)

		// NOOP doesn't do anything. Big surprise
		// see: https://tools.ietf.org/html/rfc2821#section-4.1.1.9
		case "NOOP":
			conn.WriteOK()

		// Say goodbye and close the connection
		// see: https://tools.ietf.org/html/rfc2821#section-4.1.1.10
		case "QUIT":
			conn.WriteSMTP(221, "Bye")
			break ReadLoop

		// https://tools.ietf.org/html/rfc2487
		case "STARTTLS":
			conn.WriteSMTP(220, "Ready to start TLS")

			// upgrade to TLS
			tlsConn := tls.Server(conn, s.TLSConfig)
			if tlsConn == nil {
				s.Logger.Printf("Couldn't upgrade to TLS")
				break ReadLoop
			}

			tlsConn.SetDeadline(time.Now().Add(s.WriteTimeout))
			if err := tlsConn.Handshake(); err == nil {
				conn = &Conn{
					Conn:         tlsConn,
					IsTLS:        true,
					User:         conn.User,
					Errors:       conn.Errors,
					MaxSize:      conn.MaxSize,
					ReadTimeout:  s.ReadTimeout,
					WriteTimeout: s.WriteTimeout,
				}
			} else {
				s.Logger.Printf("Could not TLS handshake:%v", err)
				break ReadLoop
			}

		// AUTH uses the configured authentication handler to perform an SMTP-AUTH
		// as defined by the ESMTP AUTH extension
		// see: http://tools.ietf.org/html/rfc4954
		case "AUTH":
			if conn.User != nil {
				conn.WriteSMTP(503, "You are already authenticated")
			} else if s.Auth != nil {
				if err := s.Auth.Handle(conn, args); err != nil {
					if serr, ok := err.(*SMTPError); ok {
						conn.WriteSMTP(serr.Code, serr.Error())
					} else {
						conn.WriteSMTP(500, "Authentication failed")
					}
				} else {
					conn.WriteSMTP(235, "Authentication succeeded")
				}
			} else {
				conn.WriteSMTP(502, "Command not implemented")
			}
		default:
			conn.WriteSMTP(500, "Syntax error, command unrecognised")
			conn.Errors = append(conn.Errors, fmt.Errorf("bad input: %v %v", verb, args))
			if len(conn.Errors) > 3 {
				conn.WriteSMTP(500, "Too many unrecognized commands")
				break ReadLoop
			}

		}
	}

	// conn.Close() is handled in a defer
	return nil
}

var pathRegex = regexp.MustCompile(`<([^@>]+@[^@>]+)>`)

// GetAddressArg extracts the address value from a supplied SMTP argument
// for handling MAIL FROM:address@example.com and RCPT TO:address@example.com
// XXX: don't like this, feels like a hack
func (s *Server) GetAddressArg(argName string, args string) (*mail.Address, error) {
	argSplit := strings.SplitN(args, ":", 2)
	if len(argSplit) == 2 && strings.ToUpper(argSplit[0]) == argName {

		path := pathRegex.FindString(argSplit[1])
		if path == "" {
			return nil, fmt.Errorf("couldnt find valid FROM path in %v", argSplit[1])
		}

		return mail.ParseAddress(path)
	}

	return nil, fmt.Errorf("Bad arguments")
}
