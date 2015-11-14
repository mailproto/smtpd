[![Build Status](https://drone.io/github.com/mailproto/smtpd/status.png)](https://drone.io/github.com/mailproto/smtpd/latest)
# smtpd
smtpd is a pure Go implementation of an SMTP server. It allows you to do things like:

```go
package main

import (
    "github.com/mailproto/smtpd"
    "fmt"
)

func main(){
    server := smtpd.NewServer(func(msg *smtpd.Message){
        fmt.Println("Got message from:", msg.Sender())
        fmt.Println(msg.Body())
    })

    server.ListenAndServe(":2525")
}
```

*@todo* document


