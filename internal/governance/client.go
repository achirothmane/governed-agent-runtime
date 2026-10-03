package governance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || token == "" {
		return nil, ErrAdmission
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, ErrAdmission
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), token: token,
		http: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) Request(ctx context.Context, request AdmissionRequest) (SignedAdmission, error) {
	if err := validateRequest(request); err != nil {
		return SignedAdmission{}, err
	}
	var signed SignedAdmission
	if err := c.post(ctx, "/v1/admissions", request, &signed, http.StatusCreated); err != nil {
		return SignedAdmission{}, err
	}
	return signed, nil
}

// Verifier uses the independently configured issuer key and authenticated live
// witness endpoint. Credentials remain inside the host-configured client.
func (c *Client) Verifier(key ed25519.PublicKey, clock func() time.Time) Verifier {
	return Verifier{
		PublicKey: append(ed25519.PublicKey(nil), key...), Clock: clock,
		Current: func(ctx context.Context, admission Admission) (Witness, error) {
			var witness Witness
			err := c.post(ctx, "/v1/admissions/current", admission, &witness, http.StatusOK)
			return witness, err
		},
	}
}

func (c *Client) post(ctx context.Context, path string, input, output any, status int) error {
	document, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(document))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		return fmt.Errorf("%w: service status %d", ErrAdmission, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return ErrAdmission
	}
	return decodeStrict(bytes.NewReader(body), output)
}
