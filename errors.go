package smtpd

import "errors"

var ErrAuthFailed = &SMTPError{535, errors.New("Authentication credentials invalid")}
var ErrAuthCancelled = &SMTPError{501, errors.New("Cancelled")}
var ErrRequiresTLS = &SMTPError{538, errors.New("Encryption required for requested authentication mechanism")}
var ErrTransaction = &SMTPError{501, errors.New("Transaction unsuccessful")}

// SMTPError is an error + SMTP response code
type SMTPError struct {
    code int
    err  error
}

// Code pulls the code
func (a *SMTPError) Code() int {
    return a.code
}

// Error pulls the base error value
func (a *SMTPError) Error() string {
    return a.err.Error()
}
