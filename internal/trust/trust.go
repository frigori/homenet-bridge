// Package trust builds the certificate pool used to verify homenet upstreams.
//
// Roots placed in internal/trust/roots (*.pem, *.crt) are embedded in the binary at build
// time, so a household can ship one binary that already trusts its private CA. The folder
// is git-ignored apart from its README: nobody's CA ends up in the public repository.
package trust

import (
	"crypto/x509"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

//go:embed roots
var embedded embed.FS

// Pool returns the system roots plus the embedded roots plus extraFiles, and how many
// non-system certificates were added.
func Pool(extraFiles []string) (*x509.CertPool, int, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	added := 0
	err = fs.WalkDir(embedded, "roots", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isCertFile(path) {
			return err
		}
		data, err := embedded.ReadFile(path)
		if err != nil {
			return err
		}
		n, err := appendPEM(pool, data)
		if err != nil {
			return fmt.Errorf("embedded %s: %w", path, err)
		}
		added += n
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	for _, path := range extraFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		n, err := appendPEM(pool, data)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", path, err)
		}
		added += n
	}
	return pool, added, nil
}

func isCertFile(path string) bool {
	return strings.HasSuffix(path, ".pem") || strings.HasSuffix(path, ".crt")
}

func appendPEM(pool *x509.CertPool, data []byte) (int, error) {
	certs, err := parsePEM(data)
	if err != nil {
		return 0, err
	}
	for _, c := range certs {
		pool.AddCert(c)
	}
	return len(certs), nil
}
