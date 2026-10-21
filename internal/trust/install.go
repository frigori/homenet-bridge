package trust

import (
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirFiles lists the *.pem and *.crt files in dir. A missing dir is not an error: release
// binaries have no embedded roots and most people add theirs with Install.
func DirFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && isCertFile(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Install copies the certificates in src into dir, re-encoded as PEM so private keys or
// other blocks in src are never copied. It returns the destination and each certificate's
// SHA-256 fingerprint, to compare against the one printed when the CA was made.
func Install(src, dir string) (dest string, fingerprints []string, err error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", nil, err
	}
	certs, err := parsePEM(data)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", src, err)
	}
	var out []byte
	for _, c := range certs {
		if !c.IsCA {
			return "", nil, fmt.Errorf("%s: %q is not a CA certificate", src, c.Subject.CommonName)
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
		fingerprints = append(fingerprints, fingerprint(c.Raw))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	dest = filepath.Join(dir, base+".pem")
	return dest, fingerprints, os.WriteFile(dest, out, 0o644)
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
