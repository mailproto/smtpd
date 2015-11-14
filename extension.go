package smtpd

type Extension interface {
    Handle(*Conn, string) error
    EHLO() string
}

type SimpleExtension struct {
    Handler func(*Conn, string) error
    Ehlo    string
}

func (s *SimpleExtension) Handle(c *Conn, args string) error {
    return s.Handler(c, args)
}

func (s *SimpleExtension) EHLO() string {
    return s.Ehlo
}
