package smtpd

// XXX: maybe just switch to using https://github.com/joefitzgerald/standardlog/blob/master/logger.go instead

type Logger interface {
    Print(v ...interface{})
    Println(v ...interface{})
    Printf(format string, v ...interface{})
}

type QuietLogger struct{}

func (l *QuietLogger) Print(v ...interface{})                 {}
func (l *QuietLogger) Println(v ...interface{})               {}
func (l *QuietLogger) Printf(format string, v ...interface{}) {}
