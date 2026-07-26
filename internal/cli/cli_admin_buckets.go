package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

func (c *Client) cmdAdminBuckets(action string, args []string) int {
	switch action {
	case "lifecycle":
		return c.adminBucketLifecycle(args)
	case "encryption":
		return c.adminBucketEncryption(args)
	case "website":
		return c.adminBucketWebsite(args)
	case "quota":
		return c.adminBucketQuota(args)
	case "delete":
		return c.adminBucketDelete(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown buckets action: %s\n", action)
		return 2
	}
}

func (c *Client) adminBucketLifecycle(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: admin buckets lifecycle <bucket> <days> [--action soft_delete|hard_delete]")
		return 2
	}
	bucket := args[0]
	var days int
	fmt.Sscanf(args[1], "%d", &days)
	action := "soft_delete"
	for i := 2; i < len(args); i++ {
		if args[i] == "--action" && i+1 < len(args) {
			action = args[i+1]
			i++
		}
	}
	body := map[string]any{"days": days, "action": action}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/buckets/"+url.PathEscape(bucket)+"/lifecycle", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminBucketEncryption(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: admin buckets encryption <bucket> <algorithm> [--kms-key-id <id>]")
		return 2
	}
	bucket := args[0]
	alg := args[1]
	kmsKeyID := ""
	for i := 2; i < len(args); i++ {
		if args[i] == "--kms-key-id" && i+1 < len(args) {
			kmsKeyID = args[i+1]
			i++
		}
	}
	body := map[string]any{"sse_algorithm": alg, "sse_kms_key_id": kmsKeyID}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/buckets/"+url.PathEscape(bucket)+"/encryption", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminBucketWebsite(args []string) int {
	bucket := ""
	var idxDoc string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--index":
			if i+1 < len(args) {
				idxDoc = args[i+1]
				i++
			}
		default:
			if bucket == "" {
				bucket = args[i]
			}
		}
	}
	if bucket == "" || idxDoc == "" {
		fmt.Fprintln(os.Stderr, "usage: admin buckets website <bucket> --index <suffix>")
		return 2
	}
	fmt.Fprintf(os.Stderr, "website config: not yet supported via admin API, use S3 endpoint directly\n")
	return 1
}

func (c *Client) adminBucketQuota(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: admin buckets quota <bucket> <max_bytes> <max_objects>")
		return 2
	}
	bucket := args[0]
	var maxBytes, maxObjects int64
	fmt.Sscanf(args[1], "%d", &maxBytes)
	fmt.Sscanf(args[2], "%d", &maxObjects)
	body := map[string]any{"max_bytes": maxBytes, "max_objects": maxObjects}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/admin/buckets/"+url.PathEscape(bucket)+"/quota", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminBucketDelete(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: admin buckets delete <bucket>")
		return 2
	}
	resp, err := c.do(http.MethodDelete, "/v1/buckets/"+url.PathEscape(args[0]), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}
