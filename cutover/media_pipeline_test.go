package cutover

import (
	"context"
	"testing"
)

type dnsCall struct {
	input       UpsertRecord
	operationID string
}

type fakeDNS struct {
	zoneID string
	calls  []dnsCall
}

func (f *fakeDNS) ZoneID(context.Context, string) (string, error) { return f.zoneID, nil }
func (f *fakeDNS) UpsertCNAME(_ context.Context, input UpsertRecord, operationID string) (recordData, error) {
	f.calls = append(f.calls, dnsCall{input: input, operationID: operationID})
	return recordData{}, nil
}

func TestDeliveryDecision(t *testing.T) {
	tests := []struct {
		name      string
		ready     bool
		rollback  bool
		wantHost  string
		wantState string
		wantCalls int
		wantErr   bool
		wantOp    string
	}{
		{name: "blocks an unfinished processing job", wantErr: true},
		{name: "cuts creator traffic to processed media", ready: true, wantHost: "edge.new.example", wantState: "creator_delivery_active", wantCalls: 1, wantOp: "job-42:cutover"},
		{name: "restores the captured delivery host", ready: true, rollback: true, wantHost: "edge.old.example", wantState: "previous_delivery_restored", wantCalls: 1, wantOp: "job-42:rollback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dns := &fakeDNS{zoneID: "zone-7"}
			pipeline := NewPipeline(dns)
			asset := Asset{ID: "asset-42", Source: "s3://incoming/clip.mp4", ProcessedHost: "edge.new.example"}
			if err := pipeline.Ingest(asset); err != nil {
				t.Fatal(err)
			}
			if tt.ready {
				if err := pipeline.MarkReady(asset.ID); err != nil {
					t.Fatal(err)
				}
			}
			req := CutoverRequest{AssetID: asset.ID, Domain: "creators.example", RecordName: "media", PreviousHost: "edge.old.example", OperationID: "job-42"}
			var result CutoverResult
			var err error
			if tt.rollback {
				result, err = pipeline.RollBack(context.Background(), req)
			} else {
				result, err = pipeline.CutOver(context.Background(), req)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if result.Host != tt.wantHost || result.State != tt.wantState {
				t.Fatalf("result = %+v", result)
			}
			if len(dns.calls) != tt.wantCalls {
				t.Fatalf("DNS calls = %d, want %d", len(dns.calls), tt.wantCalls)
			}
			if tt.wantCalls == 1 {
				call := dns.calls[0]
				if call.input.ZoneID != "zone-7" || call.input.RecordType != "CNAME" || call.input.TTL != CutoverTTL {
					t.Fatalf("DNS input = %+v", call.input)
				}
				if call.operationID != tt.wantOp {
					t.Fatalf("operation ID = %q, want %q", call.operationID, tt.wantOp)
				}
			}
		})
	}
}
