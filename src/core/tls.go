package core

import (
	"crypto/tls"
	"crypto/x509"
)

func GenerateTLSConfig(cert *tls.Certificate) (*tls.Config, error) {
	config := &tls.Config{
		Certificates: []tls.Certificate{*cert},
		ClientAuth:   tls.NoClientCert,
		GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return cert, nil
		},
		VerifyPeerCertificate: verifyTLSCertificate,
		VerifyConnection:      verifyTLSConnection,
		InsecureSkipVerify:    true,
		MinVersion:            tls.VersionTLS13,
	}
	return config, nil
}

func verifyTLSCertificate(_ [][]byte, _ [][]*x509.Certificate) error {
	return nil
}

func verifyTLSConnection(_ tls.ConnectionState) error {
	return nil
}
