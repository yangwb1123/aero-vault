package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func (c *Client) cmdSearch(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "search requires <query>")
		return 2
	}
	q := args[0]
	k := 10
	mode := "vector"
	for i := 1; i < len(args)-1; i++ {
		switch args[i] {
		case "-k":
			fmt.Sscanf(args[i+1], "%d", &k)
		case "--mode":
			mode = args[i+1]
		}
	}
	body, _ := json.Marshal(map[string]any{"query": q, "k": k, "mode": mode})
	resp, err := c.do("POST", "/v1/search", bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(respBody))
		return 1
	}
	fmt.Println(string(respBody))
	return 0
}

func (c *Client) cmdTag(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "tag requires <key> [k1=v1 ...]")
		return 2
	}
	key := args[0]
	tags := map[string]string{}
	for _, kv := range args[1:] {
		if i := strings.Index(kv, "="); i > 0 {
			tags[kv[:i]] = kv[i+1:]
		}
	}
	body, _ := json.Marshal(tags)
	resp, err := c.do("PUT", "/v1/files/"+escapeKey(key)+"/tags", bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(respBody))
		return 1
	}
	fmt.Println(string(respBody))
	return 0
}

func (c *Client) cmdVersions(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "versions requires <key>")
		return 2
	}
	resp, err := c.do("GET", "/v1/files/"+escapeKey(args[0])+"/versions", nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(respBody))
		return 1
	}
	fmt.Println(string(respBody))
	return 0
}

func (c *Client) cmdLineage(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "lineage requires <objectID>")
		return 2
	}
	resp, err := c.do("GET", "/v1/lineage/objects/"+args[0], nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(respBody))
		return 1
	}
	fmt.Println(string(respBody))
	return 0
}
