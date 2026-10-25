package porkbun

import (
	"context"
	"slices"
	"strings"
	"testing"

	pb "github.com/nrdcg/porkbun"
	"github.com/rs/zerolog"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"

	"github.com/AlessandroZanatta/external-dns-porkbun-webhook/internal/porkbuntest"
)

func newTestProvider(t *testing.T, zones ...string) (*PorkbunProvider, *porkbuntest.Server) {
	t.Helper()
	api := porkbuntest.New(t, zones...)
	p, err := NewPorkbunProvider(zones, "key", "secret", zerolog.Nop(), WithBaseURL(api.URL()))
	if err != nil {
		t.Fatalf("NewPorkbunProvider: %v", err)
	}
	return p, api
}

// summary renders records as sorted "name type content ttl" lines for comparison.
func summary(recs []pb.Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, strings.Join([]string{r.Name, r.Type, r.Content, r.TTL}, " "))
	}
	slices.Sort(out)
	return out
}

func actions(calls []porkbuntest.Call) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Action+" "+c.Zone)
	}
	return out
}

func TestNewPorkbunProvider(t *testing.T) {
	tests := []struct {
		name    string
		domains []string
		apiKey  string
		secret  string
		wantErr string
	}{
		{name: "valid", domains: []string{"example.com"}, apiKey: "k", secret: "s"},
		{name: "no domains", apiKey: "k", secret: "s", wantErr: "configured domain"},
		{name: "no api key", domains: []string{"example.com"}, secret: "s", wantErr: "API Key"},
		{name: "no secret", domains: []string{"example.com"}, apiKey: "k", wantErr: "secret Key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPorkbunProvider(tt.domains, tt.apiKey, tt.secret, zerolog.Nop())
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestGetDomainFilter(t *testing.T) {
	p, _ := newTestProvider(t, "example.com", "example.org")
	df := p.GetDomainFilter()
	if !df.Match("www.example.com") || !df.Match("example.org") {
		t.Errorf("domain filter does not match configured zones")
	}
	if df.Match("example.net") {
		t.Errorf("domain filter matches unconfigured zone")
	}
}

func TestAdjustEndpoints(t *testing.T) {
	p, _ := newTestProvider(t, "example.com")
	eps := []*endpoint.Endpoint{
		endpoint.NewEndpointWithTTL("low.example.com", "A", 300, "1.1.1.1"),
		endpoint.NewEndpoint("unset.example.com", "A", "1.1.1.1"),
		endpoint.NewEndpointWithTTL("high.example.com", "A", 3600, "1.1.1.1"),
	}
	got, err := p.AdjustEndpoints(eps)
	if err != nil {
		t.Fatal(err)
	}
	want := []endpoint.TTL{600, 0, 3600}
	for i, ep := range got {
		if ep.RecordTTL != want[i] {
			t.Errorf("%s TTL = %d, want %d", ep.DNSName, ep.RecordTTL, want[i])
		}
	}
}

func TestRecords(t *testing.T) {
	p, api := newTestProvider(t, "example.com", "example.org")
	api.Seed("example.com",
		pb.Record{Name: "", Type: "A", Content: "1.1.1.1", TTL: "600"},
		pb.Record{Name: "www", Type: "A", Content: "1.1.1.1", TTL: "3600"},
		pb.Record{Name: "www", Type: "A", Content: "2.2.2.2", TTL: "3600"},
		pb.Record{Name: "www", Type: "TXT", Content: "hello", TTL: "600"},
	)
	api.Seed("example.org", pb.Record{Name: "mail", Type: "CNAME", Content: "example.com", TTL: "600"})

	got, err := p.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []*endpoint.Endpoint{
		endpoint.NewEndpointWithTTL("example.com", "A", 600, "1.1.1.1"),
		endpoint.NewEndpointWithTTL("www.example.com", "A", 3600, "1.1.1.1", "2.2.2.2"),
		endpoint.NewEndpointWithTTL("www.example.com", "TXT", 600, "hello"),
		endpoint.NewEndpointWithTTL("mail.example.org", "CNAME", 600, "example.com"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d endpoints %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].DNSName != want[i].DNSName || got[i].RecordType != want[i].RecordType ||
			got[i].RecordTTL != want[i].RecordTTL || !got[i].Targets.Same(want[i].Targets) {
			t.Errorf("endpoint %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRecordsAPIError(t *testing.T) {
	p, api := newTestProvider(t, "example.com")
	api.Fail("retrieve", "boom")
	if _, err := p.Records(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want API error", err)
	}
}

func TestApplyChanges(t *testing.T) {
	tests := []struct {
		name    string
		zones   []string // defaults to example.com
		seed    []pb.Record
		changes plan.Changes
		want    []string // records in example.com after applying
		// keepIDs lists seeded record indexes whose ID must survive (in-place edits)
		keepIDs []int
		calls   []string // when set, the exact API calls expected
	}{
		{
			name:    "create with TTL",
			changes: plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "A", 3600, "1.1.1.1")}},
			want:    []string{"www.example.com A 1.1.1.1 3600"},
		},
		{
			name:    "create raises TTL below minimum",
			changes: plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "A", 60, "1.1.1.1")}},
			want:    []string{"www.example.com A 1.1.1.1 600"},
		},
		{
			name:    "create at zone apex",
			changes: plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("example.com", "A", "1.1.1.1")}},
			want:    []string{"example.com A 1.1.1.1 600"},
		},
		{
			name:    "create wildcard",
			changes: plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("*.example.com", "A", "1.1.1.1")}},
			want:    []string{"*.example.com A 1.1.1.1 600"},
		},
		{
			name:    "create all targets",
			changes: plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1", "2.2.2.2")}},
			want:    []string{"www.example.com A 1.1.1.1 600", "www.example.com A 2.2.2.2 600"},
		},
		{
			name:    "create skips existing record",
			seed:    []pb.Record{{Name: "www", Type: "A", Content: "1.1.1.1"}},
			changes: plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1", "2.2.2.2")}},
			want:    []string{"www.example.com A 1.1.1.1 600", "www.example.com A 2.2.2.2 600"},
			keepIDs: []int{0},
		},
		{
			name:    "create TXT registry record without quotes",
			changes: plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("a-www.example.com", "TXT", `"heritage=external-dns,external-dns/owner=default"`)}},
			want:    []string{"a-www.example.com TXT heritage=external-dns,external-dns/owner=default 600"},
		},
		{
			name: "update edits record in place",
			seed: []pb.Record{{Name: "www", Type: "A", Content: "1.1.1.1"}},
			changes: plan.Changes{
				UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1")},
				UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "2.2.2.2")},
			},
			want:    []string{"www.example.com A 2.2.2.2 600"},
			keepIDs: []int{0},
		},
		{
			name: "update TTL only",
			seed: []pb.Record{{Name: "www", Type: "A", Content: "1.1.1.1", TTL: "600"}},
			changes: plan.Changes{
				UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "A", 600, "1.1.1.1")},
				UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpointWithTTL("www.example.com", "A", 3600, "1.1.1.1")},
			},
			want:    []string{"www.example.com A 1.1.1.1 3600"},
			keepIDs: []int{0},
		},
		{
			name: "update at zone apex",
			seed: []pb.Record{{Name: "", Type: "A", Content: "1.1.1.1"}},
			changes: plan.Changes{
				UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpoint("example.com", "A", "1.1.1.1")},
				UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("example.com", "A", "2.2.2.2")},
			},
			want:    []string{"example.com A 2.2.2.2 600"},
			keepIDs: []int{0},
		},
		{
			name: "update swaps one of several targets",
			seed: []pb.Record{
				{Name: "www", Type: "A", Content: "1.1.1.1"},
				{Name: "www", Type: "A", Content: "2.2.2.2"},
			},
			changes: plan.Changes{
				UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1", "2.2.2.2")},
				UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "2.2.2.2", "3.3.3.3")},
			},
			want:    []string{"www.example.com A 2.2.2.2 600", "www.example.com A 3.3.3.3 600"},
			keepIDs: []int{0, 1},
			calls:   []string{"retrieve example.com", "edit example.com"},
		},
		{
			name: "update adds targets",
			seed: []pb.Record{{Name: "www", Type: "A", Content: "1.1.1.1"}},
			changes: plan.Changes{
				UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1")},
				UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1", "2.2.2.2")},
			},
			want: []string{"www.example.com A 1.1.1.1 600", "www.example.com A 2.2.2.2 600"},
		},
		{
			name: "update removes targets",
			seed: []pb.Record{
				{Name: "www", Type: "A", Content: "1.1.1.1"},
				{Name: "www", Type: "A", Content: "2.2.2.2"},
			},
			changes: plan.Changes{
				UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1", "2.2.2.2")},
				UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "2.2.2.2")},
			},
			want: []string{"www.example.com A 2.2.2.2 600"},
		},
		{
			name: "update when old record content no longer matches",
			seed: []pb.Record{{Name: "www", Type: "A", Content: "9.9.9.9"}},
			changes: plan.Changes{
				UpdateOld: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1")},
				UpdateNew: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "2.2.2.2")},
			},
			want:    []string{"www.example.com A 2.2.2.2 600"},
			keepIDs: []int{0},
		},
		{
			name: "delete all targets",
			seed: []pb.Record{
				{Name: "www", Type: "A", Content: "1.1.1.1"},
				{Name: "www", Type: "A", Content: "2.2.2.2"},
				{Name: "other", Type: "A", Content: "1.1.1.1"},
			},
			changes: plan.Changes{Delete: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1", "2.2.2.2")}},
			want:    []string{"other.example.com A 1.1.1.1 600"},
		},
		{
			name: "delete quoted TXT registry record",
			seed: []pb.Record{{Name: "a-www", Type: "TXT", Content: "heritage=external-dns,external-dns/owner=default"}},
			changes: plan.Changes{Delete: []*endpoint.Endpoint{
				endpoint.NewEndpoint("a-www.example.com", "TXT", `"heritage=external-dns,external-dns/owner=default"`),
			}},
			want: []string{},
		},
		{
			name: "delete of missing record does not abort other changes",
			seed: []pb.Record{{Name: "www", Type: "A", Content: "1.1.1.1"}},
			changes: plan.Changes{
				Delete: []*endpoint.Endpoint{
					endpoint.NewEndpoint("gone.example.com", "A", "1.1.1.1"),
					endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1"),
				},
				Create: []*endpoint.Endpoint{endpoint.NewEndpoint("new.example.com", "A", "2.2.2.2")},
			},
			want: []string{"new.example.com A 2.2.2.2 600"},
		},
		{
			name:    "names outside the zone are ignored",
			changes: plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("www.notexample.com", "A", "1.1.1.1")}},
			want:    []string{},
			calls:   []string{},
		},
		{
			name:  "only zones with changes are queried",
			zones: []string{"example.com", "example.org"},
			changes: plan.Changes{
				Create: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1")},
			},
			want:  []string{"www.example.com A 1.1.1.1 600"},
			calls: []string{"retrieve example.com", "create example.com"},
		},
		{
			name:    "no changes makes no API calls",
			changes: plan.Changes{},
			want:    []string{},
			calls:   []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zones := tt.zones
			if zones == nil {
				zones = []string{"example.com"}
			}
			p, api := newTestProvider(t, zones...)
			ids := api.Seed("example.com", tt.seed...)
			api.ResetCalls()

			if err := p.ApplyChanges(context.Background(), &tt.changes); err != nil {
				t.Fatalf("ApplyChanges: %v", err)
			}

			recs := api.Records("example.com")
			if got := summary(recs); !slices.Equal(got, tt.want) {
				t.Errorf("records = %q, want %q", got, tt.want)
			}
			for _, i := range tt.keepIDs {
				if !slices.ContainsFunc(recs, func(r pb.Record) bool { return r.ID == ids[i] }) {
					t.Errorf("record %s (seed %d) was replaced instead of edited in place", ids[i], i)
				}
			}
			if tt.calls != nil {
				if got := actions(api.Calls()); !slices.Equal(got, tt.calls) {
					t.Errorf("calls = %q, want %q", got, tt.calls)
				}
			}
		})
	}
}

func TestApplyChangesUsesLongestZone(t *testing.T) {
	p, api := newTestProvider(t, "example.com", "sub.example.com")
	changes := &plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("www.sub.example.com", "A", "1.1.1.1")}}
	if err := p.ApplyChanges(context.Background(), changes); err != nil {
		t.Fatal(err)
	}
	if got := summary(api.Records("sub.example.com")); !slices.Equal(got, []string{"www.sub.example.com A 1.1.1.1 600"}) {
		t.Errorf("sub.example.com records = %q", got)
	}
	if got := api.Records("example.com"); len(got) != 0 {
		t.Errorf("example.com records = %v, want none", got)
	}
}

func TestApplyChangesAPIError(t *testing.T) {
	p, api := newTestProvider(t, "example.com")
	api.Fail("create", "quota exceeded")
	changes := &plan.Changes{Create: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1")}}
	if err := p.ApplyChanges(context.Background(), changes); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("error = %v, want API error", err)
	}
}

func TestEndpointZoneName(t *testing.T) {
	zones := []string{"example.com", "sub.example.com"}
	tests := map[string]string{
		"example.com":          "example.com",
		"www.example.com":      "example.com",
		"WWW.Example.com.":     "example.com",
		"a.sub.example.com":    "sub.example.com",
		"sub.example.com":      "sub.example.com",
		"notexample.com":       "",
		"www.notexample.com":   "",
		"example.com.evil.net": "",
	}
	for name, want := range tests {
		if got := endpointZoneName(name, zones); got != want {
			t.Errorf("endpointZoneName(%q) = %q, want %q", name, got, want)
		}
	}
}
