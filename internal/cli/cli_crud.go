package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func (c *Client) cmdUpload(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "upload requires <key> <file>")
		return 2
	}
	key, file := args[0], args[1]
	f, err := os.Open(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer f.Close()
	st, _ := f.Stat()
	req, _ := http.NewRequest("PUT", c.endpoint+"/v1/files/"+escapeKey(key), f)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.tenant != "" {
		req.Header.Set("X-Aero-Tenant", c.tenant)
	}
	if st != nil {
		req.ContentLength = st.Size()
	}
	resp, err := c.http.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(body))
		return 1
	}
	fmt.Println(string(body))
	return 0
}

func (c *Client) cmdGet(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "get requires <key>")
		return 2
	}
	resp, err := c.do("GET", "/v1/files/"+escapeKey(args[0]), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(body))
		return 1
	}
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func (c *Client) cmdList(args []string) int {
	q := url.Values{}
	if len(args) > 0 {
		q.Set("prefix", args[0])
	}
	resp, err := c.do("GET", "/v1/files?"+q.Encode(), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(body))
		return 1
	}
	fmt.Println(string(body))
	return 0
}

func (c *Client) cmdRemove(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "rm requires <key>")
		return 2
	}
	resp, err := c.do("DELETE", "/v1/files/"+escapeKey(args[0]), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		fmt.Fprintln(os.Stderr, renderError(resp))

		return 1
	}
	return 0
}

func escapeKey(k string) string {
	parts := strings.Split(k, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
