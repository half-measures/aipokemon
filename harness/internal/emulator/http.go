package emulator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HTTPClient talks to emu/server.py.
type HTTPClient struct {
	base string
	hc   *http.Client
}

var _ Emulator = (*HTTPClient)(nil)

func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{base: baseURL, hc: &http.Client{Timeout: 30 * time.Second}}
}

type Health struct {
	OK    bool   `json:"ok"`
	Frame int    `json:"frame"`
	Title string `json:"title"`
}

func (c *HTTPClient) Health(ctx context.Context) (Health, error) {
	var h Health
	body, err := c.do(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return h, err
	}
	return h, json.Unmarshal(body, &h)
}

func (c *HTTPClient) Press(ctx context.Context, in Input) error {
	_, err := c.do(ctx, http.MethodPost, "/press", in)
	return err
}

func (c *HTTPClient) Screen(ctx context.Context) (image.Image, error) {
	body, err := c.do(ctx, http.MethodGet, "/screen.png", nil)
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(body))
}

func (c *HTTPClient) ReadMem(ctx context.Context, addr uint16, n int) ([]byte, error) {
	q := url.Values{"addr": {strconv.Itoa(int(addr))}, "len": {strconv.Itoa(n)}}
	return c.do(ctx, http.MethodGet, "/mem?"+q.Encode(), nil)
}

func (c *HTTPClient) SaveState(ctx context.Context, slot string) error {
	_, err := c.do(ctx, http.MethodPost, "/state/save", map[string]string{"slot": slot})
	return err
}

func (c *HTTPClient) LoadState(ctx context.Context, slot string) error {
	_, err := c.do(ctx, http.MethodPost, "/state/load", map[string]string{"slot": slot})
	return err
}

// do sends one request and returns the body, turning non-2xx responses into
// errors that carry the server's message.
func (c *HTTPClient) do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var reqBody io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, e.Error)
	}
	return body, nil
}
