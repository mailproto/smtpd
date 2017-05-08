package smtpd_test

import (
	"fmt"
	"net/smtp"
	"testing"
	"time"

	"github.com/mailproto/smtpd"
)

type MessageRecorder struct {
	Messages []*smtpd.Message
}

func (m *MessageRecorder) Record(msg *smtpd.Message) error {
	m.Messages = append(m.Messages, msg)
	return nil
}

func TestSMTPServer(t *testing.T) {

	recorder := &MessageRecorder{}
	server := smtpd.NewServer(recorder.Record)
	go server.ListenAndServe("localhost:0")
	defer server.Close()

	WaitUntilAlive(server)

	// Connect to the remote SMTP server.
	c, err := smtp.Dial(server.Address())
	if err != nil {
		t.Errorf("Should be able to dial localhost: %v", err)
	}

	// Set the sender and recipient first
	if err := c.Mail("sender@example.org"); err != nil {
		t.Errorf("Should be able to set a sender: %v", err)
	}
	if err := c.Rcpt("recipient@example.net"); err != nil {
		t.Errorf("Should be able to set a RCPT: %v", err)
	}

	if err := c.Rcpt("bcc@example.net"); err != nil {
		t.Errorf("Should be able to set a second RCPT: %v", err)
	}

	// Send the email body.
	wc, err := c.Data()
	if err != nil {
		t.Errorf("Error creating the data body: %v", err)
	}

	var emailBody = "This is the email body"

	_, err = fmt.Fprintf(wc, `From: sender@example.org
To: recipient@example.net
Content-Type: text/html

%v`, emailBody)
	if err != nil {
		t.Errorf("Error writing email: %v", err)
	}

	if err := wc.Close(); err != nil {
		t.Error(err)
	}

	// Send the QUIT command and close the connection.
	if err := c.Quit(); err != nil {
		t.Errorf("Server wouldn't accept QUIT: %v", err)
	}

	if len(recorder.Messages) != 1 {
		t.Fatalf("Expected 1 message, got: %v", len(recorder.Messages))
	}

	if h, err := recorder.Messages[0].HTML(); err == nil {
		if string(h) != emailBody {
			t.Errorf("Wrong body - want: %v, got: %v", emailBody, string(h))
		}
	} else {
		t.Fatalf("Error getting HTML body: %v", err)
	}

	bcc := recorder.Messages[0].BCC()
	if len(bcc) != 1 {
		t.Fatalf("Expected 1 BCC, got: %v", len(bcc))
	}

	if bcc[0].Address != "bcc@example.net" {
		t.Errorf("wrong BCC value, want: bcc@example.net, got: %v", bcc[0].Address)
	}

}

func TestSMTPServerTimeout(t *testing.T) {

	recorder := &MessageRecorder{}
	server := smtpd.NewServer(recorder.Record)

	// Set some really short timeouts
	server.ReadTimeout = time.Millisecond * 1
	server.WriteTimeout = time.Millisecond * 1

	go server.ListenAndServe("localhost:0")
	defer server.Close()

	WaitUntilAlive(server)

	// Connect to the remote SMTP server.
	c, err := smtp.Dial(server.Address())
	if err != nil {
		t.Errorf("Should be able to dial localhost: %v", err)
	}

	// Sleep for twice the timeout
	time.Sleep(time.Millisecond * 20)

	// Set the sender and recipient first
	if err := c.Hello("sender@example.org"); err == nil {
		t.Errorf("Should have gotten a timeout from the upstream server")
	}

}

func TestSMTPServerNoTLS(t *testing.T) {

	recorder := &MessageRecorder{}
	server := smtpd.NewServer(recorder.Record)

	go server.ListenAndServe("localhost:0")
	defer server.Close()

	WaitUntilAlive(server)

	// Connect to the remote SMTP server.
	c, err := smtp.Dial(server.Address())
	if err != nil {
		t.Errorf("Should be able to dial localhost: %v", err)
	}

	err = c.StartTLS(nil)
	if err == nil {
		t.Error("Server should return a failure for a TLS request when there is no config available")
	}

}
