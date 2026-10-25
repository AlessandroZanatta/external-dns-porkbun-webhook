package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider/webhook"

	"github.com/AlessandroZanatta/external-dns-porkbun-webhook/internal/porkbuntest"
	porkbun "github.com/AlessandroZanatta/external-dns-porkbun-webhook/provider"
)

// newWebhook serves the webhook backed by a fake Porkbun API and returns
// external-dns' own webhook client connected to it.
func newWebhook(t *testing.T) (*webhook.WebhookProvider, *porkbuntest.Server, *httptest.Server) {
	t.Helper()
	api := porkbuntest.New(t, "example.com")
	p, err := porkbun.NewPorkbunProvider([]string{"example.com"}, "key", "secret", zerolog.Nop(), porkbun.WithBaseURL(api.URL()))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(buildWebhookServer(p))
	t.Cleanup(srv.Close)

	client, err := webhook.NewWebhookProvider(srv.URL)
	if err != nil {
		t.Fatalf("NewWebhookProvider: %v", err)
	}
	return client, api, srv
}

func TestHealthz(t *testing.T) {
	_, _, srv := newWebhook(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "OK" {
		t.Errorf("healthz = %d %q", resp.StatusCode, body)
	}
}

func TestNegotiateAdvertisesDomainFilter(t *testing.T) {
	client, _, _ := newWebhook(t)
	df := client.GetDomainFilter()
	if !df.Match("www.example.com") || df.Match("www.example.org") {
		t.Errorf("negotiated domain filter = %+v, want example.com only", df)
	}
}

// sync runs one external-dns reconciliation loop against the webhook and
// returns the changes it applied.
func sync(t *testing.T, client *webhook.WebhookProvider, desired []*endpoint.Endpoint) *plan.Changes {
	t.Helper()
	ctx := context.Background()
	current, err := client.Records(ctx)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	desired, err = client.AdjustEndpoints(desired)
	if err != nil {
		t.Fatalf("AdjustEndpoints: %v", err)
	}
	p := (&plan.Plan{
		Current:        current,
		Desired:        desired,
		Policies:       []plan.Policy{&plan.SyncPolicy{}},
		ManagedRecords: []string{endpoint.RecordTypeA, endpoint.RecordTypeCNAME, endpoint.RecordTypeTXT},
	}).Calculate()
	if err := client.ApplyChanges(ctx, p.Changes); err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}
	return p.Changes
}

// TestSyncConverges drives the webhook the way external-dns does and checks
// that a second sync finds nothing left to change.
func TestSyncConverges(t *testing.T) {
	client, api, _ := newWebhook(t)

	steps := []struct {
		name    string
		desired []*endpoint.Endpoint
		want    []string
	}{
		{
			name: "initial",
			desired: []*endpoint.Endpoint{
				endpoint.NewEndpoint("example.com", "A", "1.1.1.1"),
				endpoint.NewEndpointWithTTL("www.example.com", "A", 300, "1.1.1.1", "2.2.2.2"),
				endpoint.NewEndpointWithTTL("app.example.com", "CNAME", 3600, "www.example.com"),
			},
		},
		{
			name: "change targets and TTL",
			desired: []*endpoint.Endpoint{
				endpoint.NewEndpoint("example.com", "A", "3.3.3.3"),
				endpoint.NewEndpointWithTTL("www.example.com", "A", 1200, "2.2.2.2", "4.4.4.4", "5.5.5.5"),
				endpoint.NewEndpointWithTTL("app.example.com", "CNAME", 3600, "www.example.com"),
			},
		},
		{
			name: "remove records",
			desired: []*endpoint.Endpoint{
				endpoint.NewEndpoint("example.com", "A", "3.3.3.3"),
			},
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			if changes := sync(t, client, step.desired); !changes.HasChanges() {
				t.Fatalf("first sync made no changes")
			}
			if changes := sync(t, client, step.desired); changes.HasChanges() {
				t.Errorf("second sync still has changes: create=%v updateNew=%v delete=%v (records %v)",
					changes.Create, changes.UpdateNew, changes.Delete, api.Records("example.com"))
			}
		})
	}
}
