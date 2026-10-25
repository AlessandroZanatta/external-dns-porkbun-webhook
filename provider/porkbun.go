package porkbun

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	pb "github.com/nrdcg/porkbun"
	"github.com/rs/zerolog"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
)

// minTTL is the lowest TTL Porkbun accepts; lower values are raised to it.
const minTTL = 600

// PorkbunProvider is an implementation of Provider for Porkbun DNS.
type PorkbunProvider struct {
	provider.BaseProvider
	client       *pb.Client
	domainFilter *endpoint.DomainFilter
	logger       zerolog.Logger
}

// Option customizes a PorkbunProvider.
type Option func(*PorkbunProvider)

// WithBaseURL points the provider at a different Porkbun API endpoint.
func WithBaseURL(u *url.URL) Option {
	return func(p *PorkbunProvider) {
		p.client.BaseURL = u
	}
}

// NewPorkbunProvider creates a new provider for the Porkbun API
func NewPorkbunProvider(domainFilterList []string, apiKey string, secretKey string, logger zerolog.Logger, opts ...Option) (*PorkbunProvider, error) {
	domainFilter := endpoint.NewDomainFilter(domainFilterList)

	if !domainFilter.IsConfigured() {
		return nil, errors.New("porkbun provider requires at least one configured domain in the domainFilter")
	}

	if apiKey == "" {
		return nil, errors.New("porkbun provider requires an API Key")
	}

	if secretKey == "" {
		return nil, errors.New("porkbun provider requires a secret Key")
	}

	p := &PorkbunProvider{
		client:       pb.New(secretKey, apiKey),
		domainFilter: domainFilter,
		logger:       logger,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// GetDomainFilter returns the configured domain filter, which external-dns
// receives during webhook negotiation.
func (p *PorkbunProvider) GetDomainFilter() endpoint.DomainFilterInterface {
	return p.domainFilter
}

// AdjustEndpoints raises TTLs below the Porkbun minimum, so that the TTL
// external-dns plans with matches the one Porkbun stores.
func (p *PorkbunProvider) AdjustEndpoints(endpoints []*endpoint.Endpoint) ([]*endpoint.Endpoint, error) {
	for _, ep := range endpoints {
		if ep.RecordTTL.IsConfigured() && ep.RecordTTL < minTTL {
			ep.RecordTTL = minTTL
		}
	}
	return endpoints, nil
}

// recordKey identifies all Porkbun records that make up a single endpoint.
type recordKey struct {
	name       string
	recordType string
}

// Records delivers the list of Endpoint records for all zones. Porkbun
// records sharing a name and type are merged into one endpoint with
// multiple targets.
func (p *PorkbunProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	var endpoints []*endpoint.Endpoint

	for _, zone := range p.domainFilter.Filters {
		p.logger.Debug().Str("zone", zone).Msg("Retrieving records for zone")
		recs, err := p.client.RetrieveRecords(ctx, zone)
		if err != nil {
			return nil, fmt.Errorf("unable to query DNS zone info for domain '%v': %w", zone, err)
		}

		byKey := map[recordKey]*endpoint.Endpoint{}
		for _, rec := range recs {
			key := recordKey{normalizeName(rec.Name), rec.Type}
			if ep, ok := byKey[key]; ok {
				ep.Targets = append(ep.Targets, rec.Content)
				continue
			}

			ttl, err := strconv.Atoi(rec.TTL)
			if err != nil {
				return nil, fmt.Errorf("unable to parse TTL value %q of record %s: %w", rec.TTL, rec.ID, err)
			}
			ep := endpoint.NewEndpointWithTTL(key.name, rec.Type, endpoint.TTL(ttl), rec.Content)
			byKey[key] = ep
			endpoints = append(endpoints, ep)
		}
	}

	for _, ep := range endpoints {
		p.logger.Debug().Str("endpoint", ep.String()).Msg("Endpoint collected")
	}
	return endpoints, nil
}

// zoneChanges holds the changes that fall into a single zone.
type zoneChanges struct {
	create, update, delete []*endpoint.Endpoint
}

// ApplyChanges applies a given set of changes in a given zone.
func (p *PorkbunProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	if !changes.HasChanges() {
		p.logger.Debug().Msg("No changes detected - nothing to do")
		return nil
	}

	perZone := map[string]*zoneChanges{}
	group := func(kind string, eps []*endpoint.Endpoint, add func(*zoneChanges, *endpoint.Endpoint)) {
		for _, ep := range eps {
			zone := endpointZoneName(ep.DNSName, p.domainFilter.Filters)
			if zone == "" {
				p.logger.Debug().Str("type", kind).Str("endpoint", ep.String()).Msg("Ignoring change since it did not match any zone")
				continue
			}
			p.logger.Debug().Str("type", kind).Str("endpoint", ep.String()).Str("zone", zone).Msg("Planning")
			if perZone[zone] == nil {
				perZone[zone] = &zoneChanges{}
			}
			add(perZone[zone], ep)
		}
	}
	group("create", changes.Create, func(c *zoneChanges, ep *endpoint.Endpoint) { c.create = append(c.create, ep) })
	group("update", changes.UpdateNew, func(c *zoneChanges, ep *endpoint.Endpoint) { c.update = append(c.update, ep) })
	group("delete", changes.Delete, func(c *zoneChanges, ep *endpoint.Endpoint) { c.delete = append(c.delete, ep) })

	// Process zones in a stable order; zones without changes are never queried.
	zones := make([]string, 0, len(perZone))
	for zone := range perZone {
		zones = append(zones, zone)
	}
	slices.Sort(zones)

	for _, zone := range zones {
		if err := p.applyZoneChanges(ctx, zone, perZone[zone]); err != nil {
			return err
		}
	}

	p.logger.Debug().Msg("Update completed")
	return nil
}

// applyZoneChanges reconciles a zone's changes against the records Porkbun
// currently holds. Updates and deletes look records up by name, type and
// content, so they do not depend on the old endpoint matching Porkbun exactly.
func (p *PorkbunProvider) applyZoneChanges(ctx context.Context, zone string, c *zoneChanges) error {
	recs, err := p.client.RetrieveRecords(ctx, zone)
	if err != nil {
		return fmt.Errorf("unable to get DNS records for domain '%v': %w", zone, err)
	}
	live := map[recordKey][]pb.Record{}
	for _, rec := range recs {
		key := recordKey{normalizeName(rec.Name), rec.Type}
		live[key] = append(live[key], rec)
	}

	for _, ep := range c.delete {
		key := endpointKey(ep)
		for _, target := range ep.Targets {
			content := recordContent(ep.RecordType, target)
			i := slices.IndexFunc(live[key], func(r pb.Record) bool { return sameContent(ep.RecordType, r.Content, content) })
			if i < 0 {
				p.logger.Warn().Str("zone", zone).Str("endpoint", ep.String()).Str("content", content).Msg("Record to delete not found, skipping")
				continue
			}
			if err := p.deleteRecord(ctx, zone, live[key][i]); err != nil {
				return err
			}
			live[key] = slices.Delete(live[key], i, i+1)
		}
	}

	for _, ep := range c.update {
		key := endpointKey(ep)
		if err := p.updateEndpoint(ctx, zone, ep, live[key]); err != nil {
			return err
		}
		delete(live, key)
	}

	for _, ep := range c.create {
		key := endpointKey(ep)
		for _, target := range ep.Targets {
			content := recordContent(ep.RecordType, target)
			if slices.ContainsFunc(live[key], func(r pb.Record) bool { return sameContent(ep.RecordType, r.Content, content) }) {
				p.logger.Debug().Str("zone", zone).Str("endpoint", ep.String()).Msg("Record already exists, skipping create")
				continue
			}
			rec := pb.Record{
				Name:    recordName(ep.DNSName, zone),
				Type:    ep.RecordType,
				Content: content,
				TTL:     recordTTL(ep),
			}
			if err := p.createRecord(ctx, zone, rec); err != nil {
				return err
			}
		}
	}

	return nil
}

// updateEndpoint makes the live records of one name and type match ep.
// Records whose content is still wanted are kept (their TTL fixed if needed),
// remaining records are edited in place to the remaining targets, and any
// surplus on either side is created or deleted.
func (p *PorkbunProvider) updateEndpoint(ctx context.Context, zone string, ep *endpoint.Endpoint, live []pb.Record) error {
	ttl := recordTTL(ep)
	name := recordName(ep.DNSName, zone)

	var wanted []string
	for _, target := range ep.Targets {
		wanted = append(wanted, recordContent(ep.RecordType, target))
	}

	var stale []pb.Record
	for _, rec := range live {
		i := slices.IndexFunc(wanted, func(content string) bool { return sameContent(ep.RecordType, rec.Content, content) })
		if i < 0 {
			stale = append(stale, rec)
			continue
		}
		wanted = slices.Delete(wanted, i, i+1)
		if ttl != "" && rec.TTL != ttl {
			if err := p.editRecord(ctx, zone, rec, pb.Record{Name: name, Type: ep.RecordType, Content: rec.Content, TTL: ttl, Prio: rec.Prio}); err != nil {
				return err
			}
		}
	}

	for len(stale) > 0 && len(wanted) > 0 {
		rec := stale[0]
		if err := p.editRecord(ctx, zone, rec, pb.Record{Name: name, Type: ep.RecordType, Content: wanted[0], TTL: ttl, Prio: rec.Prio}); err != nil {
			return err
		}
		stale, wanted = stale[1:], wanted[1:]
	}

	for _, content := range wanted {
		if err := p.createRecord(ctx, zone, pb.Record{Name: name, Type: ep.RecordType, Content: content, TTL: ttl}); err != nil {
			return err
		}
	}

	for _, rec := range stale {
		if err := p.deleteRecord(ctx, zone, rec); err != nil {
			return err
		}
	}

	return nil
}

func (p *PorkbunProvider) createRecord(ctx context.Context, zone string, rec pb.Record) error {
	p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", rec)).Msg("Creating new record")
	if _, err := p.client.CreateRecord(ctx, zone, rec); err != nil {
		p.logger.Error().Err(err).Str("zone", zone).Str("record", fmt.Sprintf("%+v", rec)).Msg("Failed to create record")
		return fmt.Errorf("unable to create record: %w", err)
	}
	p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", rec)).Msg("Record created successfully")
	return nil
}

func (p *PorkbunProvider) editRecord(ctx context.Context, zone string, old pb.Record, rec pb.Record) error {
	p.logger.Debug().Str("zone", zone).Str("id", old.ID).Str("record", fmt.Sprintf("%+v", rec)).Msg("Updating record")
	id, err := strconv.Atoi(old.ID)
	if err != nil {
		return fmt.Errorf("unable to parse record ID %q: %w", old.ID, err)
	}
	if err := p.client.EditRecord(ctx, zone, id, rec); err != nil {
		p.logger.Error().Err(err).Str("zone", zone).Int("id", id).Str("record", fmt.Sprintf("%+v", rec)).Msg("Failed to update record")
		return fmt.Errorf("unable to update record: %w", err)
	}
	p.logger.Debug().Str("zone", zone).Int("id", id).Msg("Record updated successfully")
	return nil
}

func (p *PorkbunProvider) deleteRecord(ctx context.Context, zone string, rec pb.Record) error {
	p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", rec)).Msg("Deleting record")
	id, err := strconv.Atoi(rec.ID)
	if err != nil {
		return fmt.Errorf("unable to parse record ID %q: %w", rec.ID, err)
	}
	if err := p.client.DeleteRecord(ctx, zone, id); err != nil {
		p.logger.Error().Err(err).Str("zone", zone).Int("id", id).Str("record", fmt.Sprintf("%+v", rec)).Msg("Failed to delete record")
		return fmt.Errorf("unable to delete record: %w", err)
	}
	p.logger.Debug().Str("zone", zone).Int("id", id).Msg("Record deleted successfully")
	return nil
}

func endpointKey(ep *endpoint.Endpoint) recordKey {
	return recordKey{normalizeName(ep.DNSName), ep.RecordType}
}

// normalizeName lowercases a DNS name and strips any trailing dot.
func normalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), ".")
}

// recordName converts a fully qualified name into the subdomain Porkbun
// expects; the zone apex is the empty string.
func recordName(dnsName string, zone string) string {
	name := normalizeName(dnsName)
	if name == zone {
		return ""
	}
	return strings.TrimSuffix(name, "."+zone)
}

// recordContent converts an endpoint target into Porkbun record content.
func recordContent(recordType string, target string) string {
	if recordType == endpoint.RecordTypeTXT && strings.HasPrefix(target, "\"heritage=") {
		return strings.Trim(target, "\"")
	}
	return target
}

// sameContent compares record contents. TXT content is case-sensitive but its
// surrounding quotes are ignored; other types compare case-insensitively.
func sameContent(recordType string, a, b string) bool {
	if recordType == endpoint.RecordTypeTXT {
		return strings.Trim(a, "\"") == strings.Trim(b, "\"")
	}
	return strings.EqualFold(a, b)
}

// recordTTL returns the TTL to send to Porkbun, or "" for the Porkbun default.
func recordTTL(ep *endpoint.Endpoint) string {
	if !ep.RecordTTL.IsConfigured() {
		return ""
	}
	return strconv.FormatInt(max(int64(ep.RecordTTL), minTTL), 10)
}

// endpointZoneName determines the zone for a DNS name by taking the longest
// zone that is either the name itself or a parent domain of it.
// returns empty string if no match found
func endpointZoneName(dnsName string, zones []string) string {
	name := normalizeName(dnsName)
	match := ""
	for _, zone := range zones {
		if (name == zone || strings.HasSuffix(name, "."+zone)) && len(zone) > len(match) {
			match = zone
		}
	}
	return match
}
