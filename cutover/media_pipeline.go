package cutover

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const CutoverTTL = 60

type DNSWriter interface {
	ZoneID(context.Context, string) (string, error)
	UpsertCNAME(context.Context, UpsertRecord, string) (recordData, error)
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(message string) error { return &ValidationError{Message: message} }

type Asset struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	ProcessedHost string `json:"processed_host"`
	Ready         bool   `json:"ready"`
}

type CutoverRequest struct {
	AssetID      string `json:"asset_id"`
	Domain       string `json:"domain"`
	RecordName   string `json:"record_name"`
	PreviousHost string `json:"previous_host"`
	OperationID  string `json:"operation_id"`
}

type CutoverResult struct {
	AssetID string `json:"asset_id"`
	Host    string `json:"host"`
	TTL     int    `json:"ttl"`
	State   string `json:"state"`
}

type Pipeline struct {
	dns    DNSWriter
	mu     sync.RWMutex
	assets map[string]Asset
}

func NewPipeline(dns DNSWriter) *Pipeline {
	return &Pipeline{dns: dns, assets: make(map[string]Asset)}
}

func (p *Pipeline) Ingest(asset Asset) error {
	if asset.ID == "" || asset.Source == "" || asset.ProcessedHost == "" {
		return invalid("id, source, and processed_host are required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.assets[asset.ID] = asset
	return nil
}

func (p *Pipeline) MarkReady(assetID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	asset, ok := p.assets[assetID]
	if !ok {
		return invalid("asset not found")
	}
	asset.Ready = true
	p.assets[assetID] = asset
	return nil
}

func (p *Pipeline) CutOver(ctx context.Context, req CutoverRequest) (CutoverResult, error) {
	p.mu.RLock()
	asset, ok := p.assets[req.AssetID]
	p.mu.RUnlock()
	if !ok {
		return CutoverResult{}, invalid("asset not found")
	}
	if !asset.Ready {
		return CutoverResult{}, invalid("processing job is not ready")
	}
	if req.Domain == "" || req.RecordName == "" || req.PreviousHost == "" || req.OperationID == "" {
		return CutoverResult{}, invalid("domain, record_name, previous_host, and operation_id are required")
	}
	zoneID, err := p.dns.ZoneID(ctx, req.Domain)
	if err != nil {
		return CutoverResult{}, err
	}
	_, err = p.dns.UpsertCNAME(ctx, UpsertRecord{
		ZoneID: zoneID, RecordType: "CNAME", Name: req.RecordName,
		Content: asset.ProcessedHost, TTL: CutoverTTL,
	}, req.OperationID+":cutover")
	if err != nil {
		return CutoverResult{}, err
	}
	return CutoverResult{AssetID: asset.ID, Host: asset.ProcessedHost, TTL: CutoverTTL, State: "creator_delivery_active"}, nil
}

func (p *Pipeline) RollBack(ctx context.Context, req CutoverRequest) (CutoverResult, error) {
	if req.Domain == "" || req.RecordName == "" || req.PreviousHost == "" || req.OperationID == "" {
		return CutoverResult{}, invalid("domain, record_name, previous_host, and operation_id are required")
	}
	zoneID, err := p.dns.ZoneID(ctx, req.Domain)
	if err != nil {
		return CutoverResult{}, err
	}
	_, err = p.dns.UpsertCNAME(ctx, UpsertRecord{
		ZoneID: zoneID, RecordType: "CNAME", Name: req.RecordName,
		Content: req.PreviousHost, TTL: CutoverTTL,
	}, req.OperationID+":rollback")
	if err != nil {
		return CutoverResult{}, err
	}
	return CutoverResult{AssetID: req.AssetID, Host: req.PreviousHost, TTL: CutoverTTL, State: "previous_delivery_restored"}, nil
}

func MapError(err error) (int, string) {
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return 400, fmt.Sprintf("pipeline request rejected: %s", validationErr)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		status := apiErr.HTTPStatus
		if status < 400 || status >= 500 {
			status = 502
		}
		return status, apiErr.Error()
	}
	return 502, "upstream request could not be completed"
}
