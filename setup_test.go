package smtpd_test

import (
    "crypto/rand"
    "crypto/rsa"
    "crypto/tls"
    "crypto/x509"
    "crypto/x509/pkix"
    "math/big"
    "net"
    "sync"
    "testing"
    "time"

    "github.com/hownowstephen/email/smtpd"
)

var tlsGen sync.Once
var tlsConfig *tls.Config

// TestingTLSConfig generates a TLS certificate for the testing session
func TestingTLSConfig() *tls.Config {

    tlsGen.Do(func() {

        priv, err := rsa.GenerateKey(rand.Reader, 2048)
        if err != nil {
            panic(err)
        }
        serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
        serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
        if err != nil {
            panic(err)
        }
        xc := x509.Certificate{
            SerialNumber: serialNumber,
            Subject: pkix.Name{
                Organization: []string{"Acme Co"},
            },
            IsCA:                  true,
            KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
            ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
            BasicConstraintsValid: true,
            IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
        }

        b, err := x509.CreateCertificate(rand.Reader, &xc, &xc, &priv.PublicKey, priv)
        if err != nil {
            panic(err)
        }

        tlsConfig = &tls.Config{
            Certificates: []tls.Certificate{
                tls.Certificate{
                    Certificate: [][]byte{b},
                    PrivateKey:  priv,
                    Leaf:        &xc,
                },
            },
            ClientAuth: tls.VerifyClientCertIfGiven,
            Rand:       rand.Reader,
        }
    })

    return tlsConfig
}

// WaitUntilAlive is a helper function to allow us to not start tests until a server boots
func WaitUntilAlive(s *smtpd.Server) {
    d, _ := time.ParseDuration("20ms")
    for s.Address() == "" {
        time.Sleep(d)
    }
}

// TestLogger sends all log messages to the testing.T object, to be displayed as it sees fit
type TestLogger struct {
    t *testing.T
}

func (t *TestLogger) Print(v ...interface{}) {
    t.t.Log(v...)
}

func (t *TestLogger) Println(v ...interface{}) {
    t.Print(v...)
}

func (t *TestLogger) Printf(format string, v ...interface{}) {
    t.t.Logf(format, v)
}
