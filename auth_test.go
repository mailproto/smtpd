package smtpd_test

import (
    "crypto/tls"
    "net/smtp"
    "testing"
    "time"

    "github.com/hownowstephen/email/smtpd"
)

type TestUser struct {
    username string
    password string
}

func (t *TestUser) IsUser(ident string) bool {
    return true
}

func (t *TestUser) Password() string {
    return t.password
}

func TestSMTPAuthPlain(t *testing.T) {
    recorder := &MessageRecorder{}
    server := smtpd.NewServer(recorder.Record)

    serverAuth := smtpd.NewAuth()
    serverAuth.Extend("PLAIN", &smtpd.AuthPlain{
        Auth: func(username, password string) (smtpd.AuthUser, bool) {
            return &TestUser{}, true
        },
    })

    server.Auth = serverAuth
    server.TLSConfig = TestingTLSConfig()

    go server.ListenAndServe("localhost:0")
    defer server.Close()

    WaitUntilAlive(server)

    // Connect to the remote SMTP server.
    c, err := smtp.Dial(server.Address())
    if err != nil {
        t.Errorf("Should be able to dial localhost: %v", err)
    }

    if err := c.StartTLS(&tls.Config{ServerName: server.Name, InsecureSkipVerify: true}); err != nil {
        t.Errorf("Should be able to negotiate some TLS? %v", err)
    }

    auth := smtp.PlainAuth("", "user@example.com", "password", "127.0.0.1")

    if err := c.Auth(auth); err != nil {
        t.Errorf("Auth should have succeeded: %v", err)
    }
}

func TestSMTPAuthPlainRejection(t *testing.T) {
    recorder := &MessageRecorder{}
    server := smtpd.NewServer(recorder.Record)

    passwd := map[string]string{
        "user@example.com": "password",
        "user@example.ca":  "canadian-password",
    }

    serverAuth := smtpd.NewAuth()
    serverAuth.Extend("PLAIN", &smtpd.AuthPlain{
        Auth: func(username, password string) (smtpd.AuthUser, bool) {
            if passwd[username] == password {
                return &TestUser{username, password}, true
            }
            return nil, false
        },
    })

    server.Auth = serverAuth
    server.TLSConfig = TestingTLSConfig()

    go server.ListenAndServe("localhost:0")
    defer server.Close()

    WaitUntilAlive(server)

    // Connect to the remote SMTP server.
    c, err := smtp.Dial(server.Address())
    if err != nil {
        t.Errorf("Should be able to dial localhost: %v", err)
        return
    }

    c.StartTLS(&tls.Config{ServerName: server.Name, InsecureSkipVerify: true})

    auth := smtp.PlainAuth("", "user@example.com", "password", "127.0.0.1")

    if err := c.Auth(auth); err != nil {
        t.Errorf("Auth should have succeded! %v", err)
    }

    // Connect to the remote SMTP server.
    c, err = smtp.Dial(server.Address())
    if err != nil {
        t.Errorf("Should be able to dial localhost: %v", err)
        return
    }

    c.StartTLS(&tls.Config{ServerName: server.Name, InsecureSkipVerify: true})

    auth = smtp.PlainAuth("", "user@example.ca", "password", "127.0.0.1")

    if err := c.Auth(auth); err == nil {
        t.Errorf("Auth should have failed!")
    }

}

func TestSMTPAuthLocking(t *testing.T) {
    recorder := &MessageRecorder{}
    server := smtpd.NewServer(recorder.Record)

    serverAuth := smtpd.NewAuth()
    serverAuth.Extend("PLAIN", &smtpd.AuthPlain{
        Auth: func(username, password string) (smtpd.AuthUser, bool) {
            return &TestUser{}, true
        },
    })

    server.Auth = serverAuth

    go server.ListenAndServe("localhost:0")
    defer server.Close()

    WaitUntilAlive(server)

    // Connect to the remote SMTP server.
    c, err := smtp.Dial(server.Address())
    if err != nil {
        t.Errorf("Should be able to dial localhost: %v", err)
    }

    if err := c.Mail("sender@example.org"); err == nil {
        t.Errorf("Should not be able to set a sender before Authenticating")
    }
}

func TestSMTPAuthPlainEncryption(t *testing.T) {
    recorder := &MessageRecorder{}
    server := smtpd.NewServer(recorder.Record)

    serverAuth := smtpd.NewAuth()
    serverAuth.Extend("PLAIN", &smtpd.AuthPlain{
        Auth: func(username, password string) (smtpd.AuthUser, bool) {
            return &TestUser{}, true
        },
    })

    server.Auth = serverAuth
    server.TLSConfig = TestingTLSConfig()

    go server.ListenAndServe("localhost:0")
    defer server.Close()

    time.Sleep(time.Second)

    // Connect to the remote SMTP server.
    c, err := smtp.Dial(server.Address())
    if err != nil {
        t.Errorf("Should be able to dial localhost: %v", err)
    }

    auth := smtp.PlainAuth("", "user@example.com", "password", "127.0.0.1")

    if err := c.Auth(auth); err == nil {
        t.Errorf("Should not be able to do PLAIN auth on an unencrypted connection")
    }
}

func TestSMTPAuthCramMd5(t *testing.T) {
    recorder := &MessageRecorder{}
    server := smtpd.NewServer(recorder.Record)

    serverAuth := smtpd.NewAuth()
    serverAuth.Extend("CRAM-MD5", &smtpd.AuthCramMd5{
        FindUser: func(username string) (smtpd.AuthUser, error) {
            return &TestUser{"user@test.com", "password"}, nil
        },
    })

    server.Auth = serverAuth
    server.TLSConfig = TestingTLSConfig()

    go server.ListenAndServe("localhost:0")
    defer server.Close()

    WaitUntilAlive(server)

    // Connect to the remote SMTP server.
    c, err := smtp.Dial(server.Address())
    if err != nil {
        t.Errorf("Should be able to dial localhost: %v", err)
    }

    if err := c.StartTLS(&tls.Config{ServerName: server.Name, InsecureSkipVerify: true}); err != nil {
        t.Errorf("Should be able to negotiate some TLS? %v", err)
    }

    auth := smtp.CRAMMD5Auth("user@test.com", "password")

    if err := c.Auth(auth); err != nil {
        t.Errorf("Auth should have succeeded: %v", err)
    }
}
