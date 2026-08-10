package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// request performs one API call and prints whatever came back.
func request(method, target string, body io.Reader) error {
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return err
	}
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(payload))
		if detail == "" {
			return errors.New(resp.Status)
		}
		return fmt.Errorf("%s: %s", resp.Status, detail)
	}
	fmt.Println(formatBody(payload))
	return nil
}

// upload sends a TOML document — a plan or a recipe — as the request body.
func upload(target, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return request(http.MethodPost, target, f)
}

// formatBody pretty-prints a JSON response, falls back to the raw bytes for
// anything else, and says OK for the endpoints that answer 204 with no content.
func formatBody(payload []byte) string {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return "OK"
	}
	var v any
	if json.Unmarshal(payload, &v) != nil {
		return string(payload)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(payload)
	}
	return string(pretty)
}

// componentPath builds /v1/components/{name}:{action}, escaping the name so a
// shell-mangled or unusual component name cannot reshape the URL.
func componentPath(name, action string) string {
	return "/v1/components/" + url.PathEscape(strings.Trim(name, "/")) + ":" + action
}

func encode(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
