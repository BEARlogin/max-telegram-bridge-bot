package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestSendMaxDirectFormattedKeepsHTMLFormat(t *testing.T) {
	const wantText = "<blockquote><b>важная</b> цитата</blockquote>"

	bridge := &Bridge{
		cfg: Config{MaxToken: "test-token"},
		apiClient: &http.Client{Transport: crosspostEditRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method=%s, want POST", req.Method)
			}
			if req.URL.Query().Get("chat_id") != "-123" {
				t.Fatalf("url=%s", req.URL)
			}
			if req.Header.Get("Authorization") != "test-token" {
				t.Fatalf("authorization=%q", req.Header.Get("Authorization"))
			}

			var payload struct {
				Text   string `json:"text"`
				Format string `json:"format"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Text != wantText || payload.Format != "html" {
				t.Fatalf("payload=%+v", payload)
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(bytes.NewBufferString(
					`{"message":{"body":{"mid":"mid.formatted"}}}`,
				)),
				Header: make(http.Header),
			}, nil
		})},
	}

	mid, err := bridge.sendMaxDirectFormatted(context.Background(), -123, wantText, "", "", "", "html")
	if err != nil {
		t.Fatal(err)
	}
	if mid != "mid.formatted" {
		t.Fatalf("mid=%q", mid)
	}
}
