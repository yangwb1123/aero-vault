package aerovault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
	r.Header.Set("Accept", "text/event-stream")
	resp, err := c.do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var response ChatResponse
	err = scanSSE(resp.Body, func(event, data string) error {
		switch event {
		case "token":
			if onToken != nil {
				onToken(unquote(data))
			}
		case "done":
			if err := json.Unmarshal([]byte(data), &response); err != nil {
				return err
			}
			return errStopSSE
		case "error":
			return streamError(data)
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopSSE) {
		return nil, err
	}
	return &response, nil
}

func streamError(data string) error {
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(data), &payload) == nil && payload.Message != "" {
		if payload.Code == "" {
			payload.Code = "StreamError"
		}
		status := http.StatusOK
		if payload.Code == "BudgetExceeded" {
			status = http.StatusPaymentRequired
		}
		return &Error{Status: status, Code: payload.Code, Message: payload.Message}
	}
	return &Error{Status: http.StatusOK, Code: "StreamError", Message: unquote(data)}
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
