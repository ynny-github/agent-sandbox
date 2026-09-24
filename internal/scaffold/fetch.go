package scaffold

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// maxAssetBytes bounds what a single asset may be. The largest thing init
// fetches is a skill of a few kilobytes; anything near this ceiling means the
// URL is answering with something other than the file.
const maxAssetBytes = 1 << 20

// Fetched is one asset's bytes together with what init reports about their
// origin. Profiles are a security boundary, so where each byte came from is
// printed rather than left implicit.
type Fetched struct {
	Asset Asset
	URL   string
	Body  []byte
	// ETag is the file's content hash as raw.githubusercontent reports it.
	// It comes free with the response, unlike the branch head sha, which
	// would cost a second request against an unauthenticated rate limit.
	ETag string
}

// Fetch retrieves every asset into memory. It returns no partial result: a
// failure anywhere means nothing is written, because a half-initialized
// project combined with "never overwrite" would persist.
func Fetch(ctx context.Context, client *http.Client, base string) ([]Fetched, error) {
	if client == nil {
		client = http.DefaultClient
	}
	out := make([]Fetched, 0, len(Assets))
	for _, a := range Assets {
		url := base + a.Source
		body, etag, err := get(ctx, client, url)
		if err != nil {
			return nil, err
		}
		out = append(out, Fetched{Asset: a, URL: url, Body: body, ETag: etag})
	}
	return out, nil
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s: %d %s", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", url, err)
	}
	if len(body) > maxAssetBytes {
		return nil, "", fmt.Errorf("%s: larger than %d bytes", url, maxAssetBytes)
	}
	return body, resp.Header.Get("ETag"), nil
}
