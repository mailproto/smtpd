[![Build Status](https://travis-ci.org/mailproto/smtpd.svg?branch=master)](https://travis-ci.org/mailproto/smtpd)
# smtpd
smtpd is a pure Go implementation of an SMTP server. It allows you to do things like:

```go
package main

import (
    "fmt"
    "net/smtp"

    "github.com/mailproto/smtpd"
)

var helloWorld = `To: sender@example.org
From: recipient@example.net
Content-Type: text/plain

This is the email body`

func main() {
    var server *smtpd.Server
    server = smtpd.NewServer(func(msg *smtpd.Message) error {
        fmt.Println("Got message from:", msg.From)
        fmt.Println(string(msg.RawBody))
        return server.Close()
    })

    go server.ListenAndServe(":2525")
    <-server.Ready

    log.Fatal(smtp.SendMail(server.Address(), nil, "sender@example.com", []string{"recipient@example.com"}, []byte(helloWorld)))
}


```

*@todo* document


