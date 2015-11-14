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
    "strings"
    "time"

    "github.com/hownowstephen/email"
)

// MessageHandler functions handle application of business logic to the inbound message
type MessageHandler func(m *email.Message) error

type Server struct {
    Name string

    TLSConfig  *tls.Config
    ServerName string

    // MaxSize of incoming message objects, zero for no cap otherwise
    // larger messages are thrown away
    MaxSize int

    // MaxConn limits the number of concurrent connections being handled
    MaxConn int

    // MaxCommands is the maximum number of commands a server will accept
    // from a single client before terminating the session
    MaxCommands int

    // RateLimiter gets called before proceeding through to message handling
    RateLimiter func(*Conn) bool

    // Handler is the handoff function for messages
    Handler MessageHandler

    // Auth is an authentication-handling extension
    Auth Extension

    // Extensions is a map of server-specific extensions & overrides, by verb
    Extensions map[string]Extension

    // Disabled features
    Disabled map[string]bool

    // Server flags
    listeners []net.Listener

    // help message to display in response to a HELP request
    Help string

    // Logger to print out status info
    Logger email.Logger
}

// NewServer creates a server with the default settings
func NewServer(handler func(*email.Message) error) *Server {
    name, err := os.Hostname()
    if err != nil {
        name = "localhost"
    }
    return &Server{
        Name:        name,
        ServerName:  name,
        MaxSize:     131072,
        MaxCommands: 100,
        Handler:     handler,
        Extensions:  make(map[string]Extension),
        Disabled:    make(map[string]bool),
        Logger:      &email.QuietLogger{},
    }
}

// Close the server connection (not happy with this)
func (s *Server) Close() {
    for _, listener := range s.listeners {
        listener.Close()
    }
}

func (s *Server) Greeting(conn *Conn) string {
    return fmt.Sprintf("Welcome! [%v]", conn.LocalAddr())
}

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

    // Start listening for SMTP connections
    listener, err := net.Listen("tcp", addr)
    if err != nil {
        s.Logger.Printf("Cannot listen on %v (%v)", addr, err)
        return err
    }

    var clientID int64
    clientID = 1

    s.listeners = append(s.listeners, listener)

    // @TODO maintain a fixed-size connection pool, throw immediate 554s otherwise
    // see http://www.greenend.org.uk/rjk/tech/smtpreplies.html
    // https://blog.golang.org/context?
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
        go s.HandleSMTP(&Conn{
            Conn:         conn,
            IsTLS:        false,
            Errors:       []error{},
            MaxSize:      s.MaxSize,
            WriteTimeout: 10,
            ReadTimeout:  10,
        })
        clientID++

    }
    return nil

}

func (s *Server) Address() string {
    if len(s.listeners) > 0 {
        return s.listeners[0].Addr().String()
    }
    return ""
}

func (s *Server) handleMessage(m *email.Message) error {
    return s.Handler(m)
}

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

                if message, err := email.NewMessage([]byte(data)); err == nil && (conn.EndTX() == nil) {

                    if err := s.handleMessage(message); err == nil {
                        conn.WriteSMTP(250, fmt.Sprintf("OK : queued as %v", message.ID()))
                    } else {
                        conn.WriteSMTP(554, fmt.Sprintf("Error: I blame me. %v", err))
                    }

                } else {
                    conn.WriteSMTP(554, fmt.Sprintf("Error: I blame you. %v", err))
                }

            } else {
                s.Logger.Println("DATA read error: %v", err)
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

            tlsConn.SetDeadline(time.Now().Add(10 * time.Second))
            if err := tlsConn.Handshake(); err == nil {
                conn = &Conn{
                    Conn:         tlsConn,
                    IsTLS:        true,
                    User:         conn.User,
                    Errors:       conn.Errors,
                    MaxSize:      conn.MaxSize,
                    WriteTimeout: conn.WriteTimeout,
                    ReadTimeout:  conn.ReadTimeout,
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
                        conn.WriteSMTP(serr.Code(), serr.Error())
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

func (s *Server) GetAddressArg(argName string, args string) (*mail.Address, error) {
    argSplit := strings.SplitN(args, ":", 2)
    if len(argSplit) == 2 && strings.ToUpper(argSplit[0]) == argName {
        return mail.ParseAddress(argSplit[1])
    }

    return nil, fmt.Errorf("Bad arguments")
}
