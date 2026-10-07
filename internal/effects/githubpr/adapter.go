package githubpr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ObservationKind string

const (
	ObservationAppliedOnce ObservationKind = "APPLIED_ONCE"
	ObservationAbsent      ObservationKind = "ABSENT"
	ObservationUnknown     ObservationKind = "UNKNOWN"
	ObservationDivergent   ObservationKind = "DIVERGENT"
)

type ExecutionKind string

const (
	ExecutionDispatched ExecutionKind = "DISPATCHED"
	ExecutionDenied     ExecutionKind = "DENIED"
)

var ErrAmbiguousDispatch = errors.New("github pull request dispatch outcome is ambiguous")

type Spec struct {
	EffectID        string
	Owner           string
	Repo            string
	Head            string
	Base            string
	ExpectedHeadSHA string
	Title           string
	Body            string
	Draft           bool
}

func (s Spec) Validate() error {
	for name, value := range map[string]string{
		"effect id":         s.EffectID,
		"owner":             s.Owner,
		"repository":        s.Repo,
		"head ref":          s.Head,
		"base ref":          s.Base,
		"expected head sha": s.ExpectedHeadSHA,
		"title":             s.Title,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	if strings.Contains(s.Body, markerPrefix) {
		return errors.New("pull request body contains reserved governed-effect marker prefix")
	}

	if len(s.ExpectedHeadSHA) != 40 {
		return errors.New("expected head sha must be a 40-character git sha")
	}
	if _, err := hex.DecodeString(s.ExpectedHeadSHA); err != nil {
		return errors.New("expected head sha must be hexadecimal")
	}

	return nil
}

type PullReceipt struct {
	Number  int
	URL     string
	State   string
	HeadSHA string
}

type Observation struct {
	Kind    ObservationKind
	Receipt *PullReceipt
	Reason  string
}

type Execution struct {
	Kind   ExecutionKind
	Reason string
}

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	APIVersion string
}

const markerPrefix = "<!-- governed-agent-runtime:github-pr:v1:"

func (c Client) withDefaults() Client {
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = "https://api.github.com"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if strings.TrimSpace(c.APIVersion) == "" {
		c.APIVersion = "2026-03-10"
	}
	return c
}

func (s Spec) bindingMarker() string {
	canonical := strings.Join([]string{
		"github-pr-effect-v1",
		s.EffectID,
		strings.ToLower(s.Owner),
		strings.ToLower(s.Repo),
		s.Base,
		s.Head,
		strings.ToLower(s.ExpectedHeadSHA),
	}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	return markerPrefix + hex.EncodeToString(sum[:]) + " -->"
}

func (s Spec) bodyWithMarker() string {
	body := strings.TrimRight(s.Body, "\n")
	if body == "" {
		return s.bindingMarker()
	}
	return body + "\n\n" + s.bindingMarker()
}

type pullWire struct {
	Number  int     `json:"number"`
	HTMLURL string  `json:"html_url"`
	State   string  `json:"state"`
	Title   string  `json:"title"`
	Body    *string `json:"body"`
	Head    struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (c Client) Observe(ctx context.Context, spec Spec) (Observation, error) {
	if err := spec.Validate(); err != nil {
		return Observation{}, err
	}
	c = c.withDefaults()

	query := url.Values{}
	query.Set("state", "all")
	query.Set("head", spec.Owner+":"+spec.Head)
	query.Set("base", spec.Base)
	query.Set("per_page", "100")

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/pulls?%s",
		c.BaseURL,
		escapePath(spec.Owner),
		escapePath(spec.Repo),
		query.Encode(),
	)

	var pulls []pullWire
	status, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &pulls)
	if err != nil {
		return Observation{Kind: ObservationUnknown, Reason: "GITHUB_LIST_PULLS_FAILED"}, err
	}
	if status != http.StatusOK {
		return Observation{
			Kind:   ObservationUnknown,
			Reason: fmt.Sprintf("GITHUB_LIST_PULLS_HTTP_%d", status),
		}, nil
	}

	targetRepo := strings.ToLower(spec.Owner + "/" + spec.Repo)
	marker := spec.bindingMarker()
	var target []pullWire
	var bound []pullWire

	for _, pull := range pulls {
		if pull.Head.Ref != spec.Head || pull.Base.Ref != spec.Base {
			continue
		}
		if pull.Head.Repo != nil && strings.ToLower(pull.Head.Repo.FullName) != targetRepo {
			continue
		}
		target = append(target, pull)

		body := ""
		if pull.Body != nil {
			body = *pull.Body
		}
		if strings.Contains(body, marker) {
			bound = append(bound, pull)
		}
	}

	if len(target) == 0 {
		return Observation{Kind: ObservationAbsent}, nil
	}
	if len(bound) == 0 {
		return Observation{
			Kind:   ObservationDivergent,
			Reason: "TARGET_PULL_REQUEST_EXISTS_WITHOUT_GOVERNED_EFFECT_BINDING",
		}, nil
	}
	if len(bound) != 1 || len(target) != 1 {
		return Observation{
			Kind:   ObservationDivergent,
			Reason: "MULTIPLE_PULL_REQUESTS_MATCH_GOVERNED_EFFECT_TARGET",
		}, nil
	}

	pull := bound[0]
	if !strings.EqualFold(pull.Head.SHA, spec.ExpectedHeadSHA) {
		return Observation{
			Kind:   ObservationDivergent,
			Reason: "GOVERNED_PULL_REQUEST_HEAD_SHA_DRIFTED",
		}, nil
	}

	return Observation{
		Kind: ObservationAppliedOnce,
		Receipt: &PullReceipt{
			Number:  pull.Number,
			URL:     pull.HTMLURL,
			State:   pull.State,
			HeadSHA: pull.Head.SHA,
		},
	}, nil
}

func (c Client) ExecuteGuarded(ctx context.Context, spec Spec) (Execution, error) {
	if err := spec.Validate(); err != nil {
		return Execution{}, err
	}
	c = c.withDefaults()

	current, status, err := c.currentHeadSHA(ctx, spec)
	if err != nil {
		return Execution{}, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return Execution{Kind: ExecutionDenied, Reason: "GITHUB_HEAD_REF_NOT_FOUND"}, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return Execution{Kind: ExecutionDenied, Reason: "GITHUB_AUTHORIZATION_DENIED"}, nil
	default:
		return Execution{}, fmt.Errorf(
			"%w: read head ref returned http %d",
			ErrAmbiguousDispatch,
			status,
		)
	}

	if !strings.EqualFold(current, spec.ExpectedHeadSHA) {
		return Execution{Kind: ExecutionDenied, Reason: "STALE_GITHUB_HEAD_SHA"}, nil
	}

	requestBody := struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
	}{
		Title: spec.Title,
		Head:  spec.Head,
		Base:  spec.Base,
		Body:  spec.bodyWithMarker(),
		Draft: spec.Draft,
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/pulls",
		c.BaseURL,
		escapePath(spec.Owner),
		escapePath(spec.Repo),
	)

	var created pullWire
	status, err = c.doJSON(ctx, http.MethodPost, endpoint, requestBody, &created)
	if err != nil {
		return Execution{}, fmt.Errorf("%w: %v", ErrAmbiguousDispatch, err)
	}

	switch status {
	case http.StatusCreated:
		return Execution{Kind: ExecutionDispatched}, nil
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return Execution{Kind: ExecutionDenied, Reason: fmt.Sprintf("GITHUB_CREATE_PULL_HTTP_%d", status)}, nil
	default:
		// 422 is deliberately ambiguous: GitHub documents it as validation
		// failure OR abuse/rate protection. It can also be the shape returned
		// when a logically equivalent PR already exists. The caller must
		// observe before any retry authority exists.
		return Execution{}, fmt.Errorf(
			"%w: create pull request returned http %d",
			ErrAmbiguousDispatch,
			status,
		)
	}
}

func (c Client) currentHeadSHA(ctx context.Context, spec Spec) (string, int, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/git/ref/heads/%s",
		c.BaseURL,
		escapePath(spec.Owner),
		escapePath(spec.Repo),
		escapeRef(spec.Head),
	)

	var response struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	status, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response)
	if err != nil {
		return "", 0, err
	}
	return response.Object.SHA, status, nil
}

func (c Client) doJSON(ctx context.Context, method, endpoint string, input any, output any) (int, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", c.APIVersion)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if output != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			return resp.StatusCode, err
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}

	return resp.StatusCode, nil
}

func escapePath(value string) string {
	return url.PathEscape(value)
}

func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
