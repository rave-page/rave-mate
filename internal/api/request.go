package api

// Shared authed-JSON request helper for the hand-written endpoints (those newer than the
// generated spec). Same redacted-logging doers as everything else.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// doJSON performs an authed JSON request. body nil = no request body; out nil = discard the
// response body. doer picks the timeout class (c.doer short, c.bulkDoer bulk, c.uploadDoer long).
func (c *Client) doJSON(ctx context.Context, method, url, token string, body, out any, doer *loggingDoer) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if token == "" {
		return fmt.Errorf("%s %s: unauthenticated", method, req.URL.Path)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := doer.Do(req)
	if err != nil {
		return err
	}
	return decode(resp, out)
}
