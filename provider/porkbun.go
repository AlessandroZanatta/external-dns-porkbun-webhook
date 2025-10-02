package porkbun

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	pb "github.com/nrdcg/porkbun"
	"github.com/rs/zerolog"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
)

// PorkbunProvider is an implementation of Provider for Porkbun DNS.
type PorkbunProvider struct {
	provider.BaseProvider
	client       *pb.Client
	domainFilter *endpoint.DomainFilter
	logger       zerolog.Logger
}

// PorkbunChange includes the changesets that need to be applied to the Porkbun API
type PorkbunChange struct {
	Create    *[]pb.Record
	UpdateNew *[]pb.Record
	UpdateOld *[]pb.Record
	Delete    *[]pb.Record
}

// NewPorkbunProvider creates a new provider for the Porkbun API
func NewPorkbunProvider(domainFilterList *[]string, apiKey string, secretKey string, logger zerolog.Logger) (*PorkbunProvider, error) {
	domainFilter := endpoint.NewDomainFilter(*domainFilterList)

	if !domainFilter.IsConfigured() {
		return nil, fmt.Errorf("porkbun provider requires at least one configured domain in the domainFilter")
	}

	if apiKey == "" {
		return nil, fmt.Errorf("porkbun provider requires an API Key")
	}

	if secretKey == "" {
		return nil, fmt.Errorf("porkbun provider requires a secret Key")
	}

	client := pb.New(secretKey, apiKey)

	return &PorkbunProvider{
		client:       client,
		domainFilter: domainFilter,
		logger:       logger,
	}, nil
}

func (p *PorkbunProvider) CreateDnsRecords(ctx context.Context, zone string, records *[]pb.Record) (string, error) {
	for _, record := range *records {
		p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", record)).Msg("Creating new record")
		_, err := p.client.CreateRecord(ctx, zone, record)
		p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", record)).Msg("Record created successfully")
		if err != nil {
			p.logger.Error().Err(err).Str("zone", zone).Str("record", fmt.Sprintf("%+v", record)).Msg("Failed to create record")
			return "", fmt.Errorf("unable to create record: %v", err)
		}
	}
	return "", nil
}

func (p *PorkbunProvider) DeleteDnsRecords(ctx context.Context, zone string, records *[]pb.Record) (string, error) {
	for _, record := range *records {
		p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", record)).Msg("Deleting record")
		id, err := strconv.Atoi(record.ID)
		if err != nil {
			return "", fmt.Errorf("unable to parse record ID: %v", err)
		}
		err = p.client.DeleteRecord(ctx, zone, id)
		p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", record)).Msg("Record deleted successfully")
		if err != nil {
			p.logger.Error().Err(err).Str("zone", zone).Int("id", id).Str("record", fmt.Sprintf("%+v", record)).Msg("Failed to delete record")
			return "", fmt.Errorf("unable to delete record: %v", err)
		}
	}
	return "", nil
}

func (p *PorkbunProvider) UpdateDnsRecords(ctx context.Context, zone string, records *[]pb.Record) (string, error) {
	for _, record := range *records {
		p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", record)).Msg("Updating record")
		id, err := strconv.Atoi(record.ID)
		if err != nil {
			return "", fmt.Errorf("unable to parse record ID: %v", err)
		}
		err = p.client.EditRecord(ctx, zone, id, record)
		p.logger.Debug().Str("zone", zone).Str("record", fmt.Sprintf("%+v", record)).Msg("Record updated successfully")
		if err != nil {
			p.logger.Error().Err(err).Str("zone", zone).Int("id", id).Str("record", fmt.Sprintf("%+v", record)).Msg("Failed to update record")
			return "", fmt.Errorf("unable to update record: %v", err)
		}
	}
	return "", nil
}

// Records delivers the list of Endpoint records for all zones.
func (p *PorkbunProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	endpoints := make([]*endpoint.Endpoint, 0)

	for _, domain := range p.domainFilter.Filters {
		p.logger.Debug().Str("domain", domain).Msg("Retrieving records for domain")
		records, err := p.client.RetrieveRecords(ctx, domain)
		if err != nil {
			return nil, fmt.Errorf("unable to query DNS zone info for domain '%v': %v", domain, err)
		}
		p.logger.Info().Str("domain", domain).Msg("Got DNS records for domain")

		for _, rec := range records {
			p.logger.Debug().Str("record", fmt.Sprintf("%+v", rec)).Msg("Processing record")
			name := rec.Name
			if strings.Split(rec.Name, ".")[0] == "@" {
				name = domain
			}

			ttl, err := strconv.Atoi(rec.TTL)
			if err != nil {
				return nil, fmt.Errorf("unable to parse TTL value: %v", err)
			}

			ep := endpoint.NewEndpointWithTTL(name, rec.Type, endpoint.TTL(ttl), rec.Content)
			endpoints = append(endpoints, ep)
		}
	}

	for _, endpointItem := range endpoints {
		p.logger.Debug().Str("endpoints", endpointItem.String()).Msg("Endpoints collected")
	}
	return endpoints, nil
}

// ApplyChanges applies a given set of changes in a given zone.
func (p *PorkbunProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	if !changes.HasChanges() {
		p.logger.Debug().Msg("No changes detected - nothing to do")
		return nil
	}

	perZoneChanges := map[string]*plan.Changes{}

	for _, zoneName := range p.domainFilter.Filters {
		p.logger.Debug().Str("zone", zoneName).Msg("Zone detected")

		perZoneChanges[zoneName] = &plan.Changes{}
	}

	for _, ep := range changes.Create {
		zoneName := p.endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug().Str("type", "create").Str("endpoint", ep.String()).Msg("Ignoring change since it did not match any zone")
			continue
		}
		p.logger.Debug().Str("type", "create").Str("endpoint", ep.String()).Str("zone", zoneName).Msg("Planning")

		perZoneChanges[zoneName].Create = append(perZoneChanges[zoneName].Create, ep)
	}

	for _, ep := range changes.UpdateOld {
		zoneName := p.endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug().Str("type", "updateOld").Str("endpoint", ep.String()).Msg("Ignoring change since it did not match any zone")
			continue
		}
		p.logger.Debug().Str("type", "updateOld").Str("endpoint", ep.String()).Str("zone", zoneName).Msg("Planning")

		perZoneChanges[zoneName].UpdateOld = append(perZoneChanges[zoneName].UpdateOld, ep)
	}

	for _, ep := range changes.UpdateNew {
		zoneName := p.endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug().Str("type", "updateNew").Str("endpoint", ep.String()).Msg("Ignoring change since it did not match any zone")
			continue
		}
		p.logger.Debug().Str("type", "updateNew").Str("endpoint", ep.String()).Str("zone", zoneName).Msg("Planning")
		perZoneChanges[zoneName].UpdateNew = append(perZoneChanges[zoneName].UpdateNew, ep)
	}

	for _, ep := range changes.Delete {
		zoneName := p.endpointZoneName(ep, p.domainFilter.Filters)
		if zoneName == "" {
			p.logger.Debug().Str("type", "delete").Str("endpoint", ep.String()).Msg("Ignoring change since it did not match any zone")
			continue
		}
		p.logger.Debug().Str("type", "delete").Str("endpoint", ep.String()).Str("zone", zoneName).Msg("Planning")
		perZoneChanges[zoneName].Delete = append(perZoneChanges[zoneName].Delete, ep)
	}

	// Assemble changes per zone and prepare it for the Porkbun API client
	for zoneName, c := range perZoneChanges {
		// Gather records from API to extract the record ID which is necessary for updating/deleting the record
		recs, err := p.client.RetrieveRecords(ctx, zoneName)
		if err != nil {
			p.logger.Error().Err(err).Str("zone", zoneName).Msg("unable to get DNS records for domain")
			return err
		}

		change := &PorkbunChange{
			Create:    p.convertToPorkbunRecord(&recs, c.Create, zoneName),
			UpdateNew: p.convertToPorkbunRecord(&recs, c.UpdateNew, zoneName),
			UpdateOld: p.convertToPorkbunRecord(&recs, c.UpdateOld, zoneName),
			Delete:    p.convertToPorkbunRecord(&recs, c.Delete, zoneName),
		}

		_, err = p.UpdateDnsRecords(ctx, zoneName, change.UpdateOld)
		if err != nil {
			return err
		}
		_, err = p.DeleteDnsRecords(ctx, zoneName, change.Delete)
		if err != nil {
			return err
		}
		_, err = p.CreateDnsRecords(ctx, zoneName, change.Create)
		if err != nil {
			return err
		}
		_, err = p.UpdateDnsRecords(ctx, zoneName, change.UpdateNew)
		if err != nil {
			return err
		}
	}

	p.logger.Debug().Msg("Update completed")

	return nil
}

// convertToPorkbunRecord transforms a list of endpoints into a list of Porkbun DNS Records
// returns a pointer to a list of DNS Records
func (p *PorkbunProvider) convertToPorkbunRecord(recs *[]pb.Record, endpoints []*endpoint.Endpoint, zoneName string) *[]pb.Record {
	records := make([]pb.Record, len(endpoints))

	for i, ep := range endpoints {
		p.logger.Debug().Str("endpoint", fmt.Sprintf("%+v", ep)).Msg("Converting endpoint to PorkbunRecord")
		recordName := strings.TrimSuffix(ep.DNSName, "."+zoneName)
		if recordName == zoneName {
			recordName = "@"
		}
		target := ep.Targets[0]
		if ep.RecordType == endpoint.RecordTypeTXT && strings.HasPrefix(target, "\"heritage=") {
			target = strings.Trim(ep.Targets[0], "\"")
		}

		records[i] = pb.Record{
			Type:    ep.RecordType,
			Name:    recordName,
			Content: target,
			ID:      p.getIDforRecord(ep.DNSName, target, ep.RecordType, recs),
		}
	}
	return &records
}

// getIDforRecord compares the endpoint with existing records to get the ID from Porkbun to ensure it can be safely removed.
// returns empty string if no match found
func (p *PorkbunProvider) getIDforRecord(recordName string, target string, recordType string, recs *[]pb.Record) string {
	p.logger.Debug().Str("recordName", recordName).Str("target", target).Str("recordType", recordType).Str("recs", fmt.Sprintf("%+v", recs)).Msg("Getting ID for record")
	for _, rec := range *recs {
		if recordType == rec.Type && target == rec.Content && rec.Name == recordName {
			return rec.ID
		}
	}

	return ""
}

// endpointZoneName determines zoneName for endpoint by taking longest suffix zoneName match in endpoint DNSName
// returns empty string if no match found
func (p *PorkbunProvider) endpointZoneName(endpoint *endpoint.Endpoint, zones []string) (zone string) {
	var matchZoneName string = ""
	for _, zoneName := range zones {
		if strings.HasSuffix(endpoint.DNSName, zoneName) && len(zoneName) > len(matchZoneName) {
			matchZoneName = zoneName
		}
	}
	return matchZoneName
}
