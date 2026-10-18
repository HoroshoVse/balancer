package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/caddyserver/certmagic"
)

// atomicFileStorage is certmagic's file storage with crash-safe writes.
// certmagic rewrites a certificate's metadata in place (e.g. on ACME renewal
// info updates); an interrupted os.WriteFile leaves an empty file, and certmagic
// then neither loads that certificate nor obtains a new one.
type atomicFileStorage struct {
	*certmagic.FileStorage
}

func newCertStorage() atomicFileStorage {
	return atomicFileStorage{&certmagic.FileStorage{Path: certStoragePath}}
}

// Store writes to a temporary file in the same directory and renames it into
// place, so readers see either the old value or the new one, never a partial one.
func (s atomicFileStorage) Store(_ context.Context, key string, value []byte) error {
	filename := s.Filename(key)
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*") // created with mode 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filename)
}

// repair fixes what keeps certmagic from using the stored certificate for each
// domain, and returns a description of every change:
//   - metadata that is missing or not valid JSON is rebuilt from the certificate
//     (certmagic would refuse to load the certificate and not obtain a new one);
//   - an unreadable certificate or key, or a pair that doesn't match, is moved
//     to certs/broken so that certmagic obtains a new certificate.
func (s atomicFileStorage) repair(ctx context.Context, issuerKey string, domains []string) []string {
	var notes []string
	for _, domain := range domains {
		certFile := s.Filename(certmagic.StorageKeys.SiteCert(issuerKey, domain))
		keyFile := s.Filename(certmagic.StorageKeys.SitePrivateKey(issuerKey, domain))
		metaKey := certmagic.StorageKeys.SiteMeta(issuerKey, domain)

		certPEM, certErr := os.ReadFile(certFile)
		keyPEM, keyErr := os.ReadFile(keyFile)
		if errors.Is(certErr, fs.ErrNotExist) && errors.Is(keyErr, fs.ErrNotExist) {
			continue // nothing stored yet: certmagic obtains a certificate
		}

		var leaf *x509.Certificate
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err == nil {
			leaf, err = x509.ParseCertificate(pair.Certificate[0])
		}
		if certErr != nil || keyErr != nil || err != nil {
			dir := filepath.Dir(certFile)
			dest := filepath.Join(filepath.Dir(s.Path), "broken", issuerKey, filepath.Base(dir)+"-"+time.Now().Format("20060102-150405"))
			mvErr := os.MkdirAll(filepath.Dir(dest), 0700)
			if mvErr == nil {
				mvErr = os.Rename(dir, dest)
			}
			if mvErr != nil {
				notes = append(notes, fmt.Sprintf("%s: сертификат или ключ повреждены, убрать их не удалось: %v", domain, mvErr))
			} else {
				notes = append(notes, fmt.Sprintf("%s: сертификат или ключ повреждены — перенесены в %s, будет выпущен новый", domain, dest))
			}
			continue
		}

		// Same decoding certmagic does when loading
		var meta struct {
			SANs []string `json:"sans"`
		}
		if data, err := s.Load(ctx, metaKey); err == nil && json.Unmarshal(data, &meta) == nil {
			continue
		}
		names := leaf.DNSNames
		if len(names) == 0 {
			names = []string{domain}
		}
		data, _ := json.Marshal(map[string][]string{"sans": names})
		if err := s.Store(ctx, metaKey, data); err != nil {
			notes = append(notes, fmt.Sprintf("%s: файл описания сертификата повреждён, восстановить не удалось: %v", domain, err))
			continue
		}
		notes = append(notes, fmt.Sprintf("%s: файл описания сертификата был пустым или повреждённым — восстановлен", domain))
	}
	return notes
}
