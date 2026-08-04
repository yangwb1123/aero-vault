package aerovault

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

func (c *Client) Thumbnail(ctx context.Context, key string, w, h int) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, fmt.Sprintf("/thumbnail?w=%d&h=%d", w, h)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/jpeg")
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) Presign(ctx context.Context, key, op string, expires int) (*Presigned, error) {
	p := filesPath(key, "/presign")
	req, err := c.newRequest(ctx, http.MethodPost, p, nil,
		withQuery(map[string]string{"op": op, "expires": strconv.Itoa(expires)}))
	if err != nil {
		return nil, err
	}
	var pr Presigned
	if err := c.doJSON(req, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}
