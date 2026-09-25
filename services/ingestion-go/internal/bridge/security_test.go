package bridge

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
	"testing"
	"time"

	"ingestion-go/internal/config"
)

func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestTLSConfigUsesSystemRootsWithoutCA(t *testing.T) {
	cfg, err := tlsConfig("MQTT_CA_FILE", "")
	if err != nil {
		t.Fatalf("tlsConfig returned error: %v", err)
	}
	if cfg.RootCAs != nil {
		t.Fatal("RootCAs should be nil so the system roots are used")
	}
}

func TestTLSConfigLoadsCAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, testCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := tlsConfig("MQTT_CA_FILE", path)
	if err != nil {
		t.Fatalf("tlsConfig returned error: %v", err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs not set from CA file")
	}
}

func TestTLSConfigRejectsUnusableCA(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := tlsConfig("MQTT_CA_FILE", filepath.Join(dir, "missing.pem")); err == nil {
		t.Fatal("expected error for missing CA file")
	}
	if _, err := tlsConfig("MQTT_CA_FILE", junk); err == nil {
		t.Fatal("expected error for CA file without certificates")
	}
}

func TestKafkaSecurityOpts(t *testing.T) {
	tests := []struct {
		name     string
		sec      config.KafkaSecurity
		wantOpts int
		wantErr  bool
	}{
		{name: "plaintext adds nothing", sec: config.KafkaSecurity{Protocol: config.ProtocolPlaintext}},
		{name: "ssl adds tls", sec: config.KafkaSecurity{Protocol: config.ProtocolSSL}, wantOpts: 1},
		{
			name: "sasl_ssl scram adds tls and sasl",
			sec: config.KafkaSecurity{
				Protocol: config.ProtocolSASLSSL, SASLMechanism: config.MechanismSCRAMSHA256,
				SASLUsername: "u", SASLPassword: "p", CAPEM: string(testCAPEM(t)),
			},
			wantOpts: 2,
		},
		{
			name: "sasl_plaintext plain adds sasl only",
			sec: config.KafkaSecurity{
				Protocol: config.ProtocolSASLPlaintext, SASLMechanism: config.MechanismPlain,
				SASLUsername: "u", SASLPassword: "p",
			},
			wantOpts: 1,
		},
		{
			name:    "unknown mechanism",
			sec:     config.KafkaSecurity{Protocol: config.ProtocolSASLPlaintext, SASLMechanism: "GSSAPI"},
			wantErr: true,
		},
		{
			name:    "bad inline CA",
			sec:     config.KafkaSecurity{Protocol: config.ProtocolSSL, CAPEM: "junk"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := kafkaSecurityOpts(tt.sec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tt.wantErr)
			}
			if len(opts) != tt.wantOpts {
				t.Fatalf("len(opts) = %d, want %d", len(opts), tt.wantOpts)
			}
		})
	}
}
