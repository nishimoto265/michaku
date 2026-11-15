package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nishimoto265/opsledger/internal/config"
)

type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Kind  string `json:"kind"` // raised | resolved | reminder
}

// Send delivers msg to one target with up to 3 attempts for webhooks.
func Send(t config.NotifyTarget, msg Message, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	switch t.Type {
	case "file":
		f, err := os.OpenFile(t.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format(time.RFC3339), msg.Kind, msg.Body)
		return err
	case "webhook":
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*200) * time.Millisecond)
			}
			if err := sendWebhook(t, msg, client); err != nil {
				lastErr = err
				continue
			}
			return nil
		}
		return lastErr
	default:
		return fmt.Errorf("unknown notify type %q", t.Type)
	}
}

func sendWebhook(t config.NotifyTarget, msg Message, client *http.Client) error {
	format := t.Format
	if format == "" {
		format = "generic"
	}
	var body []byte
	var contentType = "application/json"
	var headers = map[string]string{}
	switch format {
	case "slack":
		body, _ = json.Marshal(map[string]string{"text": msg.Title + "\n" + msg.Body})
	case "discord":
		body, _ = json.Marshal(map[string]string{"content": msg.Title + "\n" + msg.Body})
	case "ntfy":
		body = []byte(msg.Body)
		contentType = "text/plain"
		headers["Title"] = msg.Title
		headers["Tags"] = "opsledger"
	default: // generic
		body, _ = json.Marshal(map[string]any{"title": msg.Title, "body": msg.Body, "kind": msg.Kind, "ts": time.Now().Format(time.RFC3339)})
	}
	req, err := http.NewRequest(http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook status %d", resp.StatusCode)
	}
	return nil
}

func SendAll(targets []config.NotifyTarget, msg Message) []error {
	var errs []error
	for _, t := range targets {
		if err := Send(t, msg, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

var _ = strings.TrimSpace
