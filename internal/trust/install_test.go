package trust

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCert(t *testing.T, dir string, isCA bool, withKey bool) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test root"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if withKey {
		kb, _ := x509.MarshalECPrivateKey(key)
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})...)
	}
	p := filepath.Join(dir, "root.crt")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallDropsKeysAndIsFoundByDirFiles(t *testing.T) {
	src := writeCert(t, t.TempDir(), true, true)
	dir := filepath.Join(t.TempDir(), "homenet-bridge")

	dest, fps, err := Install(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fps) != 1 || len(fps[0]) != 95 {
		t.Errorf("fingerprints = %v", fps)
	}
	data, _ := os.ReadFile(dest)
	if strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("private key copied")
	}
	files, err := DirFiles(dir)
	if err != nil || len(files) != 1 || files[0] != dest {
		t.Fatalf("DirFiles = %v, %v", files, err)
	}
	if _, n, err := Pool(files); err != nil || n < 1 {
		t.Fatalf("Pool: %d roots, %v", n, err)
	}
}

func TestInstallRejectsLeafCertificates(t *testing.T) {
	src := writeCert(t, t.TempDir(), false, false)
	if _, _, err := Install(src, t.TempDir()); err == nil {
		t.Fatal("leaf certificate accepted as a root")
	}
}

func TestDirFilesMissingDir(t *testing.T) {
	files, err := DirFiles(filepath.Join(t.TempDir(), "nope"))
	if err != nil || files != nil {
		t.Fatalf("got %v, %v", files, err)
	}
}
