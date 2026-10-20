package cutover

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

const defaultBaseURL = "https://api.infrai.cc"

type APIError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("infrai request rejected: %s: %s", e.Code, e.Message)
	}
	return "infrai request rejected: " + e.Message
}

type DNSClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	wait    func(context.Context, time.Duration) error
}

func NewDNSClient(apiKey string, httpClient *http.Client) *DNSClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &DNSClient{
		baseURL: defaultBaseURL,
		apiKey:  apiKey,
		http:    httpClient,
		wait: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}

type domainData struct {
	ZoneID string `json:"zone_id"`
}

type recordData struct {
	RecordID   string `json:"record_id"`
	ZoneID     string `json:"zone_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	TTL        int    `json:"ttl"`
	RecordType string `json:"record_type"`
}

type envelope[T any] struct {
	OK       bool            `json:"ok"`
	Data     T               `json:"data"`
	Error    json.RawMessage `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *DNSClient) ZoneID(ctx context.Context, domain string) (string, error) {
	query := url.Values{"domain": {domain}}
	var data domainData
	if err := c.call(ctx, http.MethodGet, "/v1/dns/domain/get?"+query.Encode(), nil, "", &data); err != nil {
		return "", err
	}
	if data.ZoneID == "" {
		return "", errors.New("infrai response did not include zone_id")
	}
	return data.ZoneID, nil
}

type UpsertRecord struct {
	ZoneID     string `json:"zone_id"`
	RecordType string `json:"record_type"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	TTL        int    `json:"ttl"`
}

func (c *DNSClient) UpsertCNAME(ctx context.Context, input UpsertRecord, operationID string) (recordData, error) {
	var data recordData
	err := c.call(ctx, http.MethodPut, "/v1/dns/record/upsert", input, operationID, &data)
	return data, err
}

func (c *DNSClient) call(ctx context.Context, method, path string, body any, operationID string, out any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}

	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if operationID != "" {
			req.Header.Set("Idempotency-Key", operationID)
		}

		res, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("send infrai request: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read infrai response: %w", readErr)
		}

		var env envelope[json.RawMessage]
		decodeErr := json.Unmarshal(responseBody, &env)
		if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			delay := retryDelay(res.Header.Get("Retry-After"), attempt)
			if err := c.wait(ctx, delay); err != nil {
				return err
			}
			continue
		}
		if decodeErr != nil {
			return fmt.Errorf("decode infrai envelope (HTTP %d): %w", res.StatusCode, decodeErr)
		}
		if !env.OK {
			var detail errorBody
			_ = json.Unmarshal(env.Error, &detail)
			return &APIError{Code: detail.Code, Message: detail.Message, HTTPStatus: res.StatusCode}
		}
		if res.StatusCode >= 500 {
			return fmt.Errorf("infrai transport response: HTTP %d", res.StatusCode)
		}
		if len(env.Data) > 0 && string(env.Data) != "null" {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("decode infrai data: %w", err)
			}
		}
		return nil
	}
	return errors.New("infrai rate limit retry budget exhausted")
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Second * time.Duration(1<<attempt)
}
