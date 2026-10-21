package trust

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// parsePEM returns every CERTIFICATE block in data. A file with none is an error, so a
// private key or a typo'd path dropped into roots/ is not silently ignored.
func parsePEM(data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, errors.New("no PEM certificate found")
	}
	return certs, nil
}
