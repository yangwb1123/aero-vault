package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxBillingResponseBytes = 64 << 10

type Client struct {
	baseURL string
	http    *http.Client
	tokens  *tokenSource
}

func newClient(baseURL string, httpClient *http.Client, tokens *tokenSource) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, tokens: tokens}
}

func (c *Client) Entitlement(ctx context.Context) (entitlementSnapshot, error) {
	var envelope entitlementEnvelope
	if err := c.request(ctx, http.MethodGet, pathEntitlement, nil, "", &envelope); err != nil {
		return entitlementSnapshot{}, err
	}
	return envelope.Entitlement, nil
}

func (c *Client) AppendUsage(
	ctx context.Context, factID, dimension string, quantity int64,
	occurredAt time.Time, metadata map[string]string,
) error {
	request := usageRequest{
		ID: factID, Dimension: dimension, Quantity: quantity,
		OccurredAt: occurredAt, Metadata: metadata,
	}
	return c.request(ctx, http.MethodPost, pathUsage, request, factID, nil)
}

func (c *Client) Reserve(
	ctx context.Context, id, dimension string, quantity int64,
	ttl time.Duration, idempotencyKey string,
) (string, error) {
	request := reservationRequest{
		ID: id, Dimension: dimension, Quantity: quantity,
		TTLSeconds: int64(ttl / time.Second),
	}
	var response reservationEnvelope
	err := c.request(ctx, http.MethodPost, pathReservations, request, idempotencyKey, &response)
	return response.Reservation.ID, err
}

func (c *Client) CommitReservation(
	ctx context.Context, reservationID, factID, idempotencyKey string,
	metadata map[string]string,
) error {
	path := pathReservations + "/" + url.PathEscape(reservationID) + "/commit"
	return c.request(ctx, http.MethodPost, path,
		commitRequest{FactID: factID, Metadata: metadata}, idempotencyKey, nil)
}

func (c *Client) ReleaseReservation(ctx context.Context, reservationID string) error {
	return c.request(ctx, http.MethodDelete, pathReservations+"/"+url.PathEscape(reservationID), nil, "", nil)
}

func (c *Client) request(
	ctx context.Context, method, path string, body any, idempotencyKey string, target any,
) error {
	encoded, err := encodeRequest(body)
	if err != nil {
		return err
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return errors.New("snaplink billing token acquisition failed")
	}
	response, err := c.do(ctx, method, path, encoded, token, idempotencyKey)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		c.tokens.Invalidate(token)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeAPIError(response)
	}
	if target == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, maxBillingResponseBytes))
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBillingResponseBytes))
	if err := decoder.Decode(target); err != nil {
		return errors.New("snaplink billing response is invalid")
	}
	return nil
}

func encodeRequest(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode snaplink billing request: %w", err)
	}
	return encoded, nil
}

func (c *Client) do(
	ctx context.Context, method, path string, body []byte, token, idempotencyKey string,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build snaplink billing request failed")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", "aero-vault/billing")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, errors.New("snaplink billing transport failed")
	}
	return response, nil
}

func decodeAPIError(response *http.Response) error {
	wire := struct {
		Code string `json:"error"`
	}{}
	_ = json.NewDecoder(io.LimitReader(response.Body, maxBillingResponseBytes)).Decode(&wire)
	if wire.Code == "" {
		wire.Code = "server_error"
	}
	return &apiError{Status: response.StatusCode, Code: wire.Code}
}

func statusText(status int) string { return strconv.Itoa(status) }
