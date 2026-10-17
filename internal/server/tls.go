package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/gsoultan/mimir-server/internal/api"
)

// TLS loads the console's certificate, or makes one in dataDir when auto
// is set and none is given. pin is the fingerprint join tokens carry,
// set when the certificate isn't one the usual checks would trust, so
// workers check this exact key instead.
func TLS(certFile, keyFile string, auto bool, dataDir string) (cfg *tls.Config, pin string, err error) {
	switch {
	case certFile != "" || keyFile != "":
		if certFile == "" || keyFile == "" {
			return nil, "", errors.New("give both -tls-cert and -tls-key")
		}
	case auto:
		certFile, keyFile = filepath.Join(dataDir, "tls", "console.crt"), filepath.Join(dataDir, "tls", "console.key")
		if _, err := os.Stat(certFile); errors.Is(err, os.ErrNotExist) {
			if err := selfSigned(certFile, keyFile); err != nil {
				return nil, "", err
			}
		}
	default:
		return nil, "", nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, "", fmt.Errorf("load TLS certificate: %w", err)
	}
	if !trusted(cert) {
		pin = api.PinOf(&cert)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, pin, nil
}

// trusted reports whether the certificate chains to the system's roots.
func trusted(cert tls.Certificate) bool {
	inter := x509.NewCertPool()
	for _, der := range cert.Certificate[1:] {
		if c, err := x509.ParseCertificate(der); err == nil {
			inter.AddCert(c)
		}
	}
	_, err := cert.Leaf.Verify(x509.VerifyOptions{Intermediates: inter})
	return err == nil
}

// selfSigned makes a long-lived certificate for this machine. Workers
// check its key by fingerprint, so who signed it doesn't matter to them.
func selfSigned(certFile, keyFile string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "mimir console"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}
