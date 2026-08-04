package aerovault

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// UploadOptions configures an Upload.
type UploadOptions struct {
	ContentType string
	Metadata    map[string]string
	Size        int64
}

func (c *Client) Upload(ctx context.Context, key string, r io.Reader, opts UploadOptions) (*Object, error) {
	req, err := c.newRequest(ctx, http.MethodPut, filesPath(key, ""), r)
	if err != nil {
		return nil, err
	}
	if opts.ContentType != "" {
		req.Header.Set("Content-Type", opts.ContentType)
	} else {
		req.Header.Set("Content-Type", guessContentType(key))
	}
	if opts.Size > 0 {
		req.Header.Set("Content-Length", strconv.FormatInt(opts.Size, 10))
	}
	for k, v := range opts.Metadata {
		req.Header.Set("X-Meta-"+k, v)
	}
	return c.doObject(req)
}

func guessContentType(key string) string {
	ext := strings.ToLower(filepath.Ext(key))
	switch ext {
	case ".txt", ".md", ".csv", ".yaml", ".yml", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".html", ".htm":
		return "text/html"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	default:
		ct := mime.TypeByExtension(ext)
		if ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}

func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, *Object, error) {
	return c.getWith(ctx, key, "")
}

func (c *Client) GetVersion(ctx context.Context, key, version string) (io.ReadCloser, *Object, error) {
	return c.getWith(ctx, key, version)
}

func (c *Client) getWith(ctx context.Context, key, version string) (io.ReadCloser, *Object, error) {
	p := filesPath(key, "")
	if version != "" {
		p += "?version=" + version
	}
	req, err := c.newRequest(ctx, http.MethodGet, p, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, nil, err
	}
	obj := objectFromHeaders(key, resp)
	return resp.Body, obj, nil
}

func (c *Client) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, *Object, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, ""), nil)
	if err != nil {
		return nil, nil, err
	}
	rangeValue := fmt.Sprintf("bytes=%d-", offset)
	if length > 0 {
		rangeValue = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	}
	req.Header.Set("Range", rangeValue)
	resp, err := c.do(req)
	if err != nil {
		return nil, nil, err
	}
	obj := objectFromHeaders(key, resp)
	if resp.StatusCode == http.StatusPartialContent && length > 0 {
		obj.Size = length
	}
	return resp.Body, obj, nil
}

func (c *Client) Download(ctx context.Context, key string, dst io.Writer) (int64, error) {
	rc, _, err := c.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	return io.Copy(dst, rc)
}

func objectFromHeaders(key string, resp *http.Response) *Object {
	obj := &Object{Key: key}
	if v := resp.Header.Get("Content-Type"); v != "" {
		obj.ContentType = v
	}
	if v := resp.Header.Get("Content-Length"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			obj.Size = n
		}
	}
	if v := resp.Header.Get("ETag"); len(v) > 2 {
		obj.ETag = v[1 : len(v)-1]
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		if parsed, err := http.ParseTime(v); err == nil {
			obj.UpdatedAt = parsed
		}
	}
	obj.Metadata = map[string]string{}
	for k, v := range resp.Header {
		if len(v) == 0 {
			continue
		}
		if len(k) > 8 && (k[:8] == "X-Meta-" || strings.EqualFold(k[:8], "X-Meta-")) {
			obj.Metadata[strings.TrimPrefix(k, "X-Meta-")] = v[0]
		}
	}
	return obj
}

func (c *Client) Stat(ctx context.Context, key string) (*Object, error) {
	req, err := c.newRequest(ctx, http.MethodHead, filesPath(key, ""), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return objectFromHeaders(key, resp), nil
}

func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	obj, err := c.Stat(ctx, key)
	if err != nil {
		var e *Error
		if AsError(err, &e) && (e.Status == http.StatusNotFound || e.Code == "NotFound") {
			return false, nil
		}
		return false, err
	}
	return obj != nil, nil
}

// ListOptions configures List.
type ListOptions struct {
	Prefix  string
	Marker  string
	Limit   int
	Deleted bool
}

func (c *Client) List(ctx context.Context, opts ListOptions) (*ListPage, error) {
	params := map[string]string{}
	if opts.Prefix != "" {
		params["prefix"] = opts.Prefix
	}
	if opts.Marker != "" {
		params["marker"] = opts.Marker
	}
	if opts.Limit > 0 {
		params["limit"] = strconv.Itoa(opts.Limit)
	}
	if opts.Deleted {
		params["deleted"] = "true"
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/files?"+urlValues(params).Encode(), nil)
	if err != nil {
		return nil, err
	}
	var page ListPage
	if err := c.doJSON(req, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func urlValues(params map[string]string) url.Values {
	v := url.Values{}
	for k, val := range params {
		v.Set(k, val)
	}
	return v
}

func (c *Client) IterObjects(ctx context.Context, prefix string, pageSize int, fn func(Object) error) error {
	marker := ""
	for {
		page, err := c.List(ctx, ListOptions{Prefix: prefix, Marker: marker, Limit: pageSize})
		if err != nil {
			return err
		}
		for _, obj := range page.Objects {
			if err := fn(obj); err != nil {
				return err
			}
		}
		if !page.HasMore {
			break
		}
		marker = page.NextMarker
	}
	return nil
}

func (c *Client) Delete(ctx context.Context, key string, hard bool) error {
	path := filesPath(key, "")
	if hard {
		path += "?hard=1"
	}
	req, err := c.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}

// doObject sends the request and parses the Object from response headers.
func (c *Client) doObject(req *http.Request) (*Object, error) {
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var obj Object
	if err := json.NewDecoder(resp.Body).Decode(&obj); err == nil {
		return &obj, nil
	}
	// Extract the object key from the URL path (/v1/files/<key>).
	path := strings.TrimPrefix(req.URL.Path, "/v1/files/")
	return objectFromHeaders(path, resp), nil
}
