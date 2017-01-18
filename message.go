package smtpd

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
)

// Message is a nicely packaged representation of the
// recieved message
type Message struct {
	To      []*mail.Address
	From    *mail.Address
	Headers map[string]string
	Subject string
	Body    []*Part
	RawBody []byte

	// meta info
	Logger *log.Logger
}

// Part represents a single part of the message
type Part struct {
	part     *multipart.Part
	Body     []byte
	Children []*Part
}

func (m *Message) ID() string {
	return "not-implemented"
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
		mediaType, _, err := mime.ParseMediaType(p.part.Header.Get("Content-Type"))
		if err == nil && mediaType == contentType {
			return p
		}
	}
	return nil
}

// FindByType finds the first part of the message with the specified Content-Type
func (m *Message) FindBody(contentType string) ([]byte, error) {

	// XXX: this is awful and can be done much better
	var alternative *Part
	if mixed := findTypeInParts("multipart/mixed", m.Body); mixed != nil {
		alternative = findTypeInParts("multipart/alternative", []*Part{mixed})
	} else {
		alternative = findTypeInParts("multipart/alternative", m.Body)
	}

	if alternative == nil {
		return nil, fmt.Errorf("No %v content found", contentType)
	}

	part := findTypeInParts(contentType, alternative.Children)
	if part == nil {
		return nil, fmt.Errorf("No %v content found", contentType)
	}

	return part.Body, nil
}

func parseContent(header textproto.MIMEHeader, content io.Reader) ([]*Part, error) {

	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil && err.Error() == "mime: no media type" {
		mediaType = "text/plain"
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

			// XXX: LimitReader?
			slurp, err := ioutil.ReadAll(p)
			if err != nil {
				return nil, err
			}
			multi := &Part{part: p, Body: slurp}

			subParts, err := parseContent(p.Header, bytes.NewBuffer(slurp))
			if err != nil {
				return nil, err
			}

			multi.Children = subParts
			parts = append(parts, multi)

		}
	} else {
		// XXX: LimitReader?
		slurp, err := ioutil.ReadAll(content)
		if err != nil {
			return nil, err
		}

		parts = append(parts, &Part{
			part: &multipart.Part{
				Header: header,
			},
			Body: slurp,
		})
	}

	return parts, nil
}

// parseBody unwraps the body io.Reader into a set of *Part structs
func parseBody(m *mail.Message) ([]byte, []*Part, error) {

	// XXX: LimitReader?
	mbody, err := ioutil.ReadAll(m.Body)
	if err != nil {
		return []byte{}, []*Part{}, err
	}
	buf := bytes.NewBuffer(mbody)

	parts, err := parseContent(textproto.MIMEHeader(m.Header), buf)
	if err != nil {
		return nil, nil, err
	}

	return mbody, parts, nil
}

// NewMessage creates a Message from a data blob
func NewMessage(data []byte, logger *log.Logger) (*Message, error) {
	m, err := mail.ReadMessage(bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}

	to, err := m.Header.AddressList("to")
	if err != nil {
		return nil, err
	}

	from, err := m.Header.AddressList("from")
	if err != nil {
		return nil, err
	}

	header := make(map[string]string)

	for k, v := range m.Header {
		if len(v) == 1 {
			header[k] = v[0]
		}
	}

	raw, parts, err := parseBody(m)
	if err != nil {
		return nil, err
	}

	return &Message{
		To:      to,
		From:    from[0],
		Headers: header,
		Subject: m.Header.Get("subject"),
		Body:    parts,
		RawBody: raw,
		Logger:  logger,
	}, nil

}
