package accesslists

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/balancer/backend/internal/models"
)

const MaxEntries = 200000
const MaxBytes = 8 << 20

// Parse accepts plain IP/CIDR lists, with blank lines and # comments.
// Rejecting the entire response on an invalid entry prevents an HTML error
// page or a truncated feed from replacing the last working list.
func Parse(raw string) (string, int, error) {
	if len(raw) > MaxBytes {
		return "", 0, fmt.Errorf("список больше 8 МБ")
	}
	raw = strings.TrimPrefix(raw, "\ufeff")
	seen := make(map[string]bool)
	var entries []string
	for n, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		prefixes, err := models.ParsePrefixes(line)
		if err != nil {
			return "", 0, fmt.Errorf("строка %d: %w", n+1, err)
		}
		for _, p := range prefixes {
			value := p.String()
			if p.IsSingleIP() {
				value = p.Addr().String()
			}
			if !seen[value] {
				seen[value] = true
				entries = append(entries, value)
			}
			if len(entries) > MaxEntries {
				return "", 0, fmt.Errorf("в списке больше %d записей", MaxEntries)
			}
		}
	}
	return strings.Join(entries, "\n"), len(entries), nil
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("укажите HTTP или HTTPS ссылку на текстовый список без логина и пароля")
	}
	if port := u.Port(); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("некорректный порт в ссылке")
		}
	}
	return nil
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

func newHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{
		// No environment proxy: every destination is resolved, checked, and pinned
		// here, including redirects, so public URLs cannot reach local services.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("не удалось разрешить имя сервера")
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("у сервера нет IP-адресов")
			}
			for _, ip := range ips {
				if !publicAddress(ip) {
					return nil, fmt.Errorf("ссылки на локальные и служебные адреса запрещены")
				}
			}
			var conn net.Conn
			for _, ip := range ips {
				conn, err = dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
			}
			return nil, fmt.Errorf("не удалось подключиться к источнику: %w", err)
		},
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Timeout: 20 * time.Second, Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("слишком много перенаправлений")
			}
			return ValidateURL(req.URL.String())
		},
	}
}

func fetch(ctx context.Context, client *http.Client, source string) (string, error) {
	if err := ValidateURL(source); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Balancer-IP-Lists/1")
	resp, err := client.Do(req)
	if err != nil {
		// net/url errors include URLs, which may contain a private feed token.
		if uerr, ok := err.(*url.Error); ok {
			err = uerr.Err
		}
		return "", fmt.Errorf("загрузка не удалась: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("источник вернул HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать список: %w", err)
	}
	entries, count, err := Parse(string(body))
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", fmt.Errorf("источник вернул пустой список; прежние адреса сохранены")
	}
	return entries, nil
}
