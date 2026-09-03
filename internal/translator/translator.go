// Package translator is a client for a locally running manga-image-translator
// server (https://github.com/zyddnys/manga-image-translator), which handles
// bubble detection, OCR, translation (via a configured LLM/translator) and
// inpainting of a single comic page.
package translator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// ErrDisabled is returned when no translator URL is configured.
var ErrDisabled = errors.New("translator: no translator_url configured")

// maxResultSize caps a translated page image (PNG pages can be large).
const maxResultSize = 100 * 1024 * 1024 // 100 MB

// Client talks to one manga-image-translator server.
type Client struct {
	baseURL    string
	engine     string // e.g. "chatgpt", "gemini", "deepl"
	targetLang string // e.g. "POL"
	httpClient *http.Client
}

// New returns a client; an empty baseURL yields a disabled client. Per-page
// timeout is generous: with GPU a page takes seconds, on CPU it can take
// minutes.
func New(baseURL, engine, targetLang string) *Client {
	if engine == "" {
		engine = "chatgpt"
	}
	if targetLang == "" {
		targetLang = "POL"
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		engine:     engine,
		targetLang: targetLang,
		httpClient: &http.Client{Timeout: 15 * time.Minute},
	}
}

// Enabled reports whether a server URL is configured.
func (c *Client) Enabled() bool { return c.baseURL != "" }

// TargetLang returns the configured target language code.
func (c *Client) TargetLang() string { return c.targetLang }

// pageConfig is the config JSON accepted by manga-image-translator; sections
// not listed here keep the server's defaults (lama inpainting, 48px OCR,
// default detector).
type pageConfig struct {
	Translator struct {
		Translator string `json:"translator"`
		TargetLang string `json:"target_lang"`
	} `json:"translator"`
}

// TranslatePage sends one page image to POST /translate/with-form/image and
// returns the translated page as PNG bytes.
func (c *Client) TranslatePage(image []byte, filename string) ([]byte, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	var cfg pageConfig
	cfg.Translator.Translator = c.engine
	cfg.Translator.TargetLang = c.targetLang
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("translator: config: %w", err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("image", filename)
	if err != nil {
		return nil, fmt.Errorf("translator: form: %w", err)
	}
	if _, err := fw.Write(image); err != nil {
		return nil, fmt.Errorf("translator: form: %w", err)
	}
	if err := mw.WriteField("config", string(cfgJSON)); err != nil {
		return nil, fmt.Errorf("translator: form: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("translator: form: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/translate/with-form/image", &body)
	if err != nil {
		return nil, fmt.Errorf("translator: request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("translator: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("translator: server returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResultSize+1))
	if err != nil {
		return nil, fmt.Errorf("translator: reading result: %w", err)
	}
	if len(out) > maxResultSize {
		return nil, fmt.Errorf("translator: result exceeds %d byte limit", maxResultSize)
	}
	if len(out) == 0 {
		return nil, errors.New("translator: server returned an empty image")
	}
	return out, nil
}
