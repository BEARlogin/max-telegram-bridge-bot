package billing

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

// Встроенные сертификаты должны парситься и быть именно Russian Trusted CA (Минцифры),
// иначе клиент T-Bank молча потеряет доверие при миграции.
func TestEmbeddedRussianCAs(t *testing.T) {
	cases := map[string]string{
		"certs/russian_trusted_root.pem": "Russian Trusted Root CA",
		"certs/russian_trusted_sub.pem":  "Russian Trusted Sub CA",
	}
	for path, wantCN := range cases {
		raw, err := russianCAFS.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			t.Fatalf("%s: not PEM", path)
		}
		crt, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("%s: parse: %v", path, err)
		}
		if crt.Subject.CommonName != wantCN {
			t.Errorf("%s: CN=%q, want %q", path, crt.Subject.CommonName, wantCN)
		}
		if !strings.Contains(crt.Subject.Organization[0], "Ministry of Digital Development") {
			t.Errorf("%s: unexpected O=%v", path, crt.Subject.Organization)
		}
		if !crt.IsCA {
			t.Errorf("%s: not a CA cert", path)
		}
	}
}

func TestTbankHTTPClientBuilds(t *testing.T) {
	c := tbankHTTPClient()
	if c == nil || c.Transport == nil {
		t.Fatal("tbankHTTPClient returned nil/no transport")
	}
	if c.Timeout == 0 {
		t.Error("client has no timeout")
	}
}
