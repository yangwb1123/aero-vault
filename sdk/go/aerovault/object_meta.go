package aerovault

import (
	"context"
	"net/http"
)

type tagsEnvelope struct {
	Tags map[string]string `json:"tags"`
}

func (c *Client) GetTags(ctx context.Context, key string) (map[string]string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, "/tags"), nil)
	if err != nil {
		return nil, err
	}
	var env tagsEnvelope
	if err := c.doJSON(req, &env); err != nil {
		return nil, err
	}
	return env.Tags, nil
}

func (c *Client) PutTags(ctx context.Context, key string, tags map[string]string) (map[string]string, error) {
	body, jOpt, err := jsonBody(tags)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPut, filesPath(key, "/tags"), body, jOpt)
	if err != nil {
		return nil, err
	}
	var env tagsEnvelope
	if err := c.doJSON(req, &env); err != nil {
		return nil, err
	}
	return env.Tags, nil
}

func (c *Client) DeleteTags(ctx context.Context, key string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, filesPath(key, "/tags"), nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}

func (c *Client) ListVersions(ctx context.Context, key string) ([]ObjectVersion, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, "/versions"), nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Versions []ObjectVersion `json:"versions"`
	}
	if err := c.doJSON(req, &out); err != nil {
		return nil, err
	}
	return out.Versions, nil
}

func (c *Client) GetACL(ctx context.Context, key string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, "/acl"), nil)
	if err != nil {
		return "", err
	}
	var out struct {
		ACL string `json:"acl"`
	}
	if err := c.doJSON(req, &out); err != nil {
		return "", err
	}
	return out.ACL, nil
}

func (c *Client) SetACL(ctx context.Context, key, acl string) error {
	body, jOpt, err := jsonBody(map[string]string{"acl": acl})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPut, filesPath(key, "/acl"), body, jOpt)
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}

func (c *Client) GetBucketACL(ctx context.Context, bucket string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/buckets/"+bucket+"/acl", nil)
	if err != nil {
		return "", err
	}
	var out struct {
		ACL string `json:"acl"`
	}
	if err := c.doJSON(req, &out); err != nil {
		return "", err
	}
	return out.ACL, nil
}

func (c *Client) SetBucketACL(ctx context.Context, bucket, acl string) error {
	body, jOpt, err := jsonBody(map[string]string{"acl": acl})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPut, "/v1/buckets/"+bucket+"/acl", body, jOpt)
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}
