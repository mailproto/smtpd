package smtpd

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

const idEntropy = 64
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const (
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
)

// Message is a nicely packaged representation of the
// recieved message
type Message struct {
	To      []*mail.Address
	From    *mail.Address
	Header  mail.Header
	Subject string
	RawBody []byte

	messageID    string
	genMessageID sync.Once
	rcpt         []*mail.Address

	// meta info
	Logger *log.Logger
}

// Part represents a single part of the message
type Part struct {
	Header   textproto.MIMEHeader
	part     *multipart.Part
	Body     []byte
	Children []*Part
}

// ID returns an identifier for this message, or generates one if none available using the masked string
// algorithm from https://stackoverflow.com/questions/22892120/how-to-generate-a-random-string-of-a-fixed-length-in-golang
func (m *Message) ID() string {
	m.genMessageID.Do(func() {
		if m.messageID = m.Header.Get("Message-ID"); m.messageID != "" {
			return
		}
		var src = rand.NewSource(time.Now().UnixNano())

		b := make([]byte, idEntropy)
		// A src.Int63() generates 63 random bits, enough for letterIdxMax characters!
		for i, cache, remain := idEntropy-1, src.Int63(), letterIdxMax; i >= 0; {
			if remain == 0 {
				cache, remain = src.Int63(), letterIdxMax
			}
			if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
				b[i] = letterBytes[idx]
				i--
			}
			cache >>= letterIdxBits
			remain--
		}

		m.messageID = string(b)
	})
	return m.messageID
}

// BCC returns a list of addresses this message should be
func (m *Message) BCC() []*mail.Address {

	var inHeaders = make(map[string]struct{})
	for _, to := range m.To {
		inHeaders[to.Address] = struct{}{}
	}

	var bcc []*mail.Address
	for _, recipient := range m.rcpt {
		if _, ok := inHeaders[recipient.Address]; !ok {
			bcc = append(bcc, recipient)
		}
	}

	return bcc
}

// Plain returns the text/plain content of the message, if any
func (m *Message) Plain() ([]byte, error) {
	return m.FindBody("text/plain")
}

// HTML returns the text/html content of the message, if any
func (m *Message) HTML() ([]byte, error) {
	return m.FindBody("text/html")
}

func findTypeInParts(contentType string, parts []*Part) *Part {
	for _, p := range parts {
		mediaType, _, err := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if err == nil && mediaType == contentType {
			return p
		}
	}
	return nil
}

// Attachments returns the list of attachments on this message
// XXX: this assumes that the only mimetype supporting attachments is multipart/mixed
// need to review https://en.wikipedia.org/wiki/MIME#Multipart_messages to ensure that is the case
func (m *Message) Attachments() ([]*Part, error) {
	mediaType, _, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}

	parts, err := m.Parts()
	if err != nil {
		return nil, err
	}

	var attachments []*Part
	if mediaType == "multipart/mixed" {
		for _, part := range parts {
			mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(mediaType, "multipart/") {
				// XXX: any cases where this would still be an attachment?
				continue
			}
			attachments = append(attachments, part)
		}
	}
	return attachments, nil
}

// FindBody finds the first part of the message with the specified Content-Type
func (m *Message) FindBody(contentType string) ([]byte, error) {

	mediaType, _, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}

	parts, err := m.Parts()
	if err != nil {
		return nil, err
	}

	var alternatives []*Part
	switch mediaType {
	case contentType:
		if len(parts) > 0 {
			return parts[0].Body, nil
		}
		return nil, fmt.Errorf("%v found, but no data in body", contentType)
	case "multipart/alternative":
		alternatives = parts
	default:
		if alt := findTypeInParts("multipart/alternative", parts); alt != nil {
			alternatives = alt.Children
		}
	}

	if len(alternatives) == 0 {
		return nil, fmt.Errorf("No multipart/alternative section found, can't find %v", contentType)
	}

	part := findTypeInParts(contentType, alternatives)
	if part == nil {
		return nil, fmt.Errorf("No %v content found in multipart/alternative section", contentType)
	}

	return part.Body, nil
}

func readToPart(header textproto.MIMEHeader, content io.Reader) (*Part, error) {
	cte := strings.ToLower(header.Get("Content-Transfer-Encoding"))

	if cte == "quoted-printable" {
		content = quotedprintable.NewReader(content)
	}

	slurp, err := ioutil.ReadAll(content)
	if err != nil {
		return nil, err
	}

	if cte == "base64" {
		dst := make([]byte, base64.StdEncoding.DecodedLen(len(slurp)))
		decodedLen, err := base64.StdEncoding.Decode(dst, slurp)
		if err != nil {
			return nil, err
		}

		slurp = dst[:decodedLen]
	}
	return &Part{
		Header: header,
		Body:   slurp,
	}, nil
}

func parseContent(header textproto.MIMEHeader, content io.Reader) ([]*Part, error) {

	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil && err.Error() == "mime: no media type" {
		mediaType = "application/octet-stream"
	} else if err != nil {
		return nil, fmt.Errorf("Media Type error: %v", err)
	}

	var parts []*Part

	if strings.HasPrefix(mediaType, "multipart/") {

		mr := multipart.NewReader(content, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				return nil, fmt.Errorf("MIME error: %v", err)
			}

			part, err := readToPart(p.Header, p)

			// XXX: maybe want to implement a less strict mode that gets what it can out of the message
			// instead of erroring out on individual sections?
			partType, _, err := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(partType, "multipart/") {
				subParts, err := parseContent(p.Header, bytes.NewBuffer(part.Body))
				if err != nil {
					return nil, err
				}
				part.Children = subParts
			}
			parts = append(parts, part)
		}
	} else {
		part, err := readToPart(header, content)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}

	return parts, nil
}

// Parts breaks a message body into its mime parts
func (m *Message) Parts() ([]*Part, error) {
	parts, err := parseContent(textproto.MIMEHeader(m.Header), bytes.NewBuffer(m.RawBody))
	if err != nil {
		return nil, err
	}

	return parts, nil
}

// NewMessage creates a Message from a data blob and a recipients list
func NewMessage(data []byte, rcpt []*mail.Address, logger *log.Logger) (*Message, error) {
	m, err := mail.ReadMessage(bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}

	// TODO: This isn't accurate, the To field should be all the values from RCPT TO:
	to, err := m.Header.AddressList("To")
	if err != nil {
		return nil, err
	}

	from, err := m.Header.AddressList("From")
	if err != nil {
		return nil, err
	}

	header := make(map[string]string)

	for k, v := range m.Header {
		if len(v) == 1 {
			header[k] = v[0]
		}
	}

	raw, err := ioutil.ReadAll(m.Body)
	if err != nil && err != io.EOF {
		return nil, err
	}

	return &Message{
		rcpt:    rcpt,
		To:      to,
		From:    from[0],
		Header:  m.Header,
		Subject: m.Header.Get("subject"),
		RawBody: raw,
		Logger:  logger,
	}, nil

}
