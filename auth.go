package smtpd

import (
    "crypto/hmac"
    "crypto/md5"
    "crypto/rand"
    "encoding/base64"
    "fmt"
    "math"
    "math/big"
    "os"
    "strconv"
    "strings"
    "time"
)

type Auth struct {
    Mechanisms map[string]AuthExtension
}

func NewAuth() *Auth {
    return &Auth{
        Mechanisms: make(map[string]AuthExtension),
    }
}

// Handle authentication by handing off to one of the configured auth mechanisms
func (a *Auth) Handle(c *Conn, args string) error {

    mech := strings.SplitN(args, " ", 2)

    if m, ok := a.Mechanisms[strings.ToUpper(mech[0])]; ok {
        var args string
        if len(mech) == 2 {
            args = mech[1]
        }
        if user, err := m.Handle(c, args); err == nil {
            c.User = user
            return nil
        } else {
            return err
        }
    }

    return &SMTPError{500, fmt.Errorf("AUTH mechanism %v not available", mech[0])}

}

// EHLO returns a stringified list of the installed Auth mechanisms
func (a *Auth) EHLO() string {
    var mechanisms []string
    for m := range a.Mechanisms {
        mechanisms = append(mechanisms, m)
    }
    return strings.Join(mechanisms, " ")
}

// Extend the auth handler by adding a new mechanism
func (a *Auth) Extend(mechanism string, extension AuthExtension) error {
    mechanism = strings.ToUpper(mechanism)
    if _, ok := a.Mechanisms[mechanism]; ok {
        return fmt.Errorf("AUTH mechanism %v is already implemented", mechanism)
    }
    a.Mechanisms[mechanism] = extension
    return nil
}

// AuthUser should check if a given string identifies that user
type AuthUser interface {
    IsUser(value string) bool
    Password() string
}

// http://tools.ietf.org/html/rfc4422#section-3.1
// https://en.wikipedia.org/wiki/Simple_Authentication_and_Security_Layer
type AuthExtension interface {
    Handle(*Conn, string) (AuthUser, error)
}

type SimpleAuthFunc func(string, string) (AuthUser, bool)

type AuthPlain struct {
    Auth SimpleAuthFunc
}

func (a *AuthPlain) unpack(line string) (string, string, error) {
    rawCreds, err := base64.StdEncoding.DecodeString(line)
    if err != nil {
        return "", "", err
    }
    creds := strings.SplitN(string(rawCreds), "\x00", 3)

    if len(creds) != 3 {
        return "", "", fmt.Errorf("Malformed auth string")
    }

    return creds[1], creds[2], nil
}

// Handles the negotiation of an AUTH PLAIN request
func (a *AuthPlain) Handle(conn *Conn, params string) (AuthUser, error) {

    if !conn.IsTLS {
        return nil, ErrRequiresTLS
    }

    if strings.TrimSpace(params) == "" {
        conn.WriteSMTP(334, "")
        if line, err := conn.ReadLine(); err == nil {
            username, password, err := a.unpack(line)
            if err != nil {
                return nil, err
            } else if user, isAuth := a.Auth(username, password); isAuth {
                return user, nil
            }
        } else {
            return nil, err
        }
    } else if username, password, err := a.unpack(params); err == nil {
        if user, isAuth := a.Auth(username, password); isAuth {
            return user, nil
        }
    }

    return nil, ErrAuthFailed
}

type AuthCramMd5 struct {
    FindUser func(string) (AuthUser, error)
}

// challenge generates a CramMD5 challenge using the http://www.jwz.org/doc/mid.html recommendation
func (a *AuthCramMd5) challenge() []byte {

    wallTime := time.Now().Unix()
    randValue, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
    if err != nil {
        panic(err)
    }

    hostname, err := os.Hostname()
    if err != nil {
        hostname = "localhost"
    }

    messageId := "<" + strconv.FormatInt(wallTime, 36) + "." + strconv.FormatInt(randValue.Int64(), 36) + "@" + hostname + ">"

    return []byte(messageId)
}

// Note: This is currently very weak & requires storing of the user's password in plaintext
// one good alternative is to do the HMAC manually and expose handlers for pre-processing the
// password MD5s
func (a *AuthCramMd5) CheckResponse(response string, challenge []byte) (AuthUser, bool) {
    if a.FindUser == nil {
        return nil, false
    }

    if decoded, err := base64.StdEncoding.DecodeString(response); err == nil {
        if parts := strings.SplitN(string(decoded), " ", 2); len(parts) == 2 {

            if user, err := a.FindUser(parts[0]); err == nil {

                d := hmac.New(md5.New, []byte(user.Password()))
                d.Write(challenge)

                if fmt.Sprintf("%x", d.Sum(nil)) == parts[1] {
                    return user, true
                }
            }
        }
    }

    return nil, false

}

// Handles the negotiation of an AUTH CRAM-MD5 request
// https://en.wikipedia.org/wiki/CRAM-MD5
// http://www.samlogic.net/articles/smtp-commands-reference-auth.htm
func (a *AuthCramMd5) Handle(conn *Conn, params string) (AuthUser, error) {

    if !conn.IsTLS {
        return nil, ErrRequiresTLS
    }

    myChallenge := a.challenge()
    conn.WriteSMTP(334, base64.StdEncoding.EncodeToString(myChallenge))
    if line, err := conn.ReadLine(); err == nil {
        if strings.TrimSpace(line) == "*" {
            return nil, ErrAuthCancelled
        } else if user, ok := a.CheckResponse(strings.TrimSpace(line), myChallenge); ok {
            return user, nil
        }
    }

    return nil, ErrAuthFailed
}
