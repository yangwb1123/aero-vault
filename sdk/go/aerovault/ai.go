package aerovault

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	body, jOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/search", body, jOpt)
	if err != nil {
		return nil, err
	}
	var res SearchResponse
	if err := c.doJSON(r, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, jOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/chat", body, jOpt)
	if err != nil {
		return nil, err
	}
	var res ChatResponse
	if err := c.doJSON(r, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) ChatStream(ctx context.Context, req ChatRequest, onToken func(token string)) (*ChatResponse, error) {
	body, jOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/chat/stream", body, jOpt)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var response ChatResponse
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if strings.HasPrefix(data, `{"token":"`) {
			var token struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal([]byte(data), &token); err == nil && token.Token != "" {
				onToken(token.Token)
			}
			continue
		}
		if strings.HasPrefix(data, `{"answer":`) {
			if err := json.Unmarshal([]byte(data), &response); err == nil {
				break
			}
		}
	}
	return &response, scanner.Err()
}

func (c *Client) Agent(ctx context.Context, query string) (*AgentResponse, error) {
	body, jOpt, err := jsonBody(map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/agent", body, jOpt)
	if err != nil {
		return nil, err
	}
	var res AgentResponse
	if err := c.doJSON(r, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) Lineage(ctx context.Context, objectID int64, limit int) (*LineageResponse, error) {
	path := fmt.Sprintf("/v1/lineage/objects/%d", objectID)
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	r, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var res LineageResponse
	if err := c.doJSON(r, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
