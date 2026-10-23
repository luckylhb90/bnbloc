package roomtools

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
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The egress proxy's local CA (docs/architecture/service-components.md,
// egress): one per member directory, its key 0600 there and never sent
// anywhere. It issues short-lived leaf certificates only for the hosts a
// service claims for a turn, and only the turn's commands trust it (the
// bundle files named in EgressEnv); the member's own trust store is never
// touched.

const (
	egressCACertFile  = "egress-ca.pem"
	egressCAKeyFile   = "egress-ca.key"
	egressBundleFile  = "egress-ca-bundle.pem"
	egressCALifetime  = 2 * 365 * 24 * time.Hour
	egressLeafLife    = 24 * time.Hour
	egressCARenewDays = 30
)

// systemRootBundles are where a system keeps its PEM root bundle: macOS and
// the BSDs, Debian/Ubuntu/Alpine, Fedora/RHEL.
var systemRootBundles = []string{"/etc/ssl/cert.pem", "/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt"}

type egressCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	// caFile holds the CA alone, bundleFile the system roots and the CA
	// ("" when the system keeps no PEM bundle).
	caFile, bundleFile string

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// loadEgressCA reads the CA in dir, or makes one (and its files) when there
// is none or it nears its end. dir "" keeps a CA in memory for this run,
// with no files (the commands then get no CA variables).
func loadEgressCA(dir string) (*egressCA, error) {
	if dir == "" {
		ca, err := newEgressCA("", "")
		if err == nil {
			ca.leaves = map[string]*tls.Certificate{}
		}
		return ca, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(dir, egressCACertFile), filepath.Join(dir, egressCAKeyFile)
	ca, err := readEgressCA(certPath, keyPath)
	if err != nil || time.Until(ca.cert.NotAfter) < egressCARenewDays*24*time.Hour {
		if ca, err = newEgressCA(certPath, keyPath); err != nil {
			return nil, err
		}
	}
	ca.caFile = certPath
	ca.leaves = map[string]*tls.Certificate{}
	for _, p := range systemRootBundles {
		roots, err := os.ReadFile(p)
		if err != nil || len(roots) == 0 {
			continue
		}
		bundle := append(append(append([]byte(nil), roots...), '\n'), ca.certPEM...)
		path := filepath.Join(dir, egressBundleFile)
		if err := os.WriteFile(path, bundle, 0o644); err == nil {
			ca.bundleFile = path
		}
		break
	}
	return ca, nil
}

func readEgressCA(certPath, keyPath string) (*egressCA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("egress CA files are not PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &egressCA{cert: cert, key: key, certPEM: certPEM}, nil
}

func newEgressCA(certPath, keyPath string) (*egressCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "bnbloc egress (this computer's agents only)"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(egressCALifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if certPath == "" {
		return &egressCA{cert: cert, key: key, certPEM: certPEM}, nil
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, fmt.Errorf("save the egress CA key: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("save the egress CA: %w", err)
	}
	return &egressCA{cert: cert, key: key, certPEM: certPEM}, nil
}

// leaf is a certificate for host, issued now or taken from the cache while
// it has more than an hour left.
func (ca *egressCA) leaf(host string) (*tls.Certificate, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if c := ca.leaves[host]; c != nil && time.Until(c.Leaf.NotAfter) > time.Hour {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(egressLeafLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key, Leaf: leaf}
	if len(ca.leaves) > 512 {
		clear(ca.leaves)
	}
	ca.leaves[host] = c
	return c, nil
}

func readFileQuiet(p string) ([]byte, error) { return os.ReadFile(p) }
