// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package gpucloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// errNotFound is a machine the provider no longer knows.
var errNotFound = errors.New("not found")

// restClient calls a provider's JSON API.
type restClient struct {
	HTTP *http.Client
	// Base is the API's address, which tests point at a fake.
	Base string
}

func (r restClient) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: time.Minute}
}

// do sends in as JSON, when not nil, to path with the bearer token, and
// reads the answer into out, when not nil. A 404 is errNotFound.
func (r restClient) do(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.Base, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := r.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", errNotFound, apiMessage(b))
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s (HTTP %d)", apiMessage(b), res.StatusCode)
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// apiMessage picks the message out of an error answer, in the shapes the
// providers use.
func apiMessage(b []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Msg     string          `json:"msg"`
	}
	if json.Unmarshal(b, &e) == nil {
		var s string
		var o struct {
			Message    string `json:"message"`
			Suggestion string `json:"suggestion"`
		}
		switch {
		case json.Unmarshal(e.Error, &s) == nil && s != "":
			if e.Msg != "" {
				return s + ": " + e.Msg
			}
			return s
		case json.Unmarshal(e.Error, &o) == nil && o.Message != "":
			return strings.TrimSpace(o.Message + " " + o.Suggestion)
		case e.Message != "":
			return e.Message
		case e.Msg != "":
			return e.Msg
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	if s == "" {
		s = "no answer"
	}
	return s
}
