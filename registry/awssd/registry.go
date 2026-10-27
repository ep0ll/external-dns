/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package awssd

import (
	"context"
	"errors"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/pkg/apis/externaldns"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
	"sigs.k8s.io/external-dns/registry"
)

// AWSSDRegistry implements registry interface with ownership information associated via the Description field of SD Service
type AWSSDRegistry struct {
	provider provider.Provider
	ownerID  string
}

// New creates an AWSSDRegistry from the given configuration.
func New(cfg *externaldns.Config, p provider.Provider) (registry.Registry, error) {
	return newRegistry(p, cfg.TXTOwnerID)
}

// newRegistry returns implementation of registry for AWS SD
func newRegistry(provider provider.Provider, ownerID string) (*AWSSDRegistry, error) {
	if ownerID == "" {
		return nil, errors.New("owner id cannot be empty")
	}
	return &AWSSDRegistry{
		provider: provider,
		ownerID:  ownerID,
	}, nil
}

func (sdr *AWSSDRegistry) GetDomainFilter() endpoint.DomainFilterInterface {
	return sdr.provider.GetDomainFilter()
}

func (sdr *AWSSDRegistry) OwnerID() string {
	return sdr.ownerID
}

// Records calls AWS SD API and expects AWS SD provider to provider Owner/Resource information as a serialized
// value in the AWSSDDescriptionLabel value in the Labels map
func (sdr *AWSSDRegistry) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	records, err := sdr.provider.Records(ctx)
	if err != nil {
		return nil, err
	}

	for _, record := range records {
		labels, err := endpoint.NewLabelsFromStringPlain(record.Labels[endpoint.AWSSDDescriptionLabel])
		if err != nil {
			// if we fail to parse the output then simply assume the endpoint is not managed by any instance of External DNS
			record.Labels = endpoint.NewLabels()
			continue
		}
		record.Labels = labels
	}

	return records, nil
}

// ApplyChanges filters out records not owned by External-DNS, additionally it adds the required label
// inserted in the AWS SD instance as a CreateID field.
// Updates are only forwarded when the resource label actually changed between the
// current (old) and desired (new) endpoint.
func (sdr *AWSSDRegistry) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	updateNew, updateOld := sdr.filterUpdates(changes.UpdateNew, changes.UpdateOld)

	filteredChanges := &plan.Changes{
		Create:    changes.Create,
		UpdateNew: updateNew,
		UpdateOld: updateOld,
		Delete:    endpoint.FilterEndpointsByOwnerID(sdr.ownerID, changes.Delete),
	}

	sdr.updateLabels(filteredChanges.Create)
	sdr.updateLabels(filteredChanges.UpdateNew)
	sdr.updateLabels(filteredChanges.UpdateOld)
	sdr.updateLabels(filteredChanges.Delete)

	return sdr.provider.ApplyChanges(ctx, filteredChanges)
}

// filterUpdates keeps only the (new, old) pairs that are owned by this registry
// and whose resource label differs between current and desired state.
// The returned slices stay index-aligned.
func (sdr *AWSSDRegistry) filterUpdates(newEps, oldEps []*endpoint.Endpoint) ([]*endpoint.Endpoint, []*endpoint.Endpoint) {
	ownedNew := endpoint.FilterEndpointsByOwnerID(sdr.ownerID, newEps)
	ownedOld := endpoint.FilterEndpointsByOwnerID(sdr.ownerID, oldEps)

	oldByKey := make(map[endpoint.EndpointKey]*endpoint.Endpoint, len(ownedOld))
	for _, ep := range ownedOld {
		oldByKey[ep.Key()] = ep
	}

	filteredNew := make([]*endpoint.Endpoint, 0, len(ownedNew))
	filteredOld := make([]*endpoint.Endpoint, 0, len(ownedNew))

	for _, desired := range ownedNew {
		current, ok := oldByKey[desired.Key()]
		if !ok || !updateNeeded(current, desired) {
			continue
		}

		filteredNew = append(filteredNew, desired)
		filteredOld = append(filteredOld, current)
	}

	return filteredNew, filteredOld
}

// updateNeeded reports whether desired differs from current in a way that
// requires pushing an update to AWS SD.
func updateNeeded(current, desired *endpoint.Endpoint) bool {
	return resourceLabelChanged(
		current.Labels.SerializePlain(false),
		desired.Labels.SerializePlain(false),
	) ||
		current.RecordTTL != desired.RecordTTL ||
		!current.Targets.Same(desired.Targets)
}

func resourceLabelChanged(current, desired string) bool {
	currentLabels, currentErr := endpoint.NewLabelsFromStringPlain(current)
	desiredLabels, desiredErr := endpoint.NewLabelsFromStringPlain(desired)

	if desiredErr != nil {
		return false
	}
	if currentErr != nil {
		return true
	}

	return currentLabels[endpoint.ResourceLabelKey] !=
		desiredLabels[endpoint.ResourceLabelKey]
}

func (sdr *AWSSDRegistry) updateLabels(endpoints []*endpoint.Endpoint) {
	for _, ep := range endpoints {
		if ep.Labels == nil {
			ep.Labels = make(map[string]string)
		}
		ep.Labels[endpoint.OwnerLabelKey] = sdr.ownerID
		ep.Labels[endpoint.AWSSDDescriptionLabel] = ep.Labels.SerializePlain(false)
	}
}

// AdjustEndpoints modifies the endpoints as needed by the specific provider
func (sdr *AWSSDRegistry) AdjustEndpoints(endpoints []*endpoint.Endpoint) ([]*endpoint.Endpoint, error) {
	return sdr.provider.AdjustEndpoints(endpoints)
}
