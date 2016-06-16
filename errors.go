package smtpd

import "errors"

// Well-defined errors
var (
	ErrAuthFailed    = SMTPError{535, errors.New("Authentication credentials invalid")}
	ErrAuthCancelled = SMTPError{501, errors.New("Cancelled")}
	ErrRequiresTLS   = SMTPError{538, errors.New("Encryption required for requested authentication mechanism")}
	ErrTransaction   = SMTPError{501, errors.New("Transaction unsuccessful")}
)

// SMTPError is an error + SMTP response code
type SMTPError struct {
	Code int
	Err  error
}

// Error pulls the base error value
func (a SMTPError) Error() string {
	return a.Err.Error()
}

// NewError creates an SMTPError with the supplied code
func NewError(code int, message string) SMTPError {
	return SMTPError{code, errors.New(message)}
}
