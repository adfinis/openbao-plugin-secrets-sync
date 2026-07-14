package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/openbao/openbao/sdk/v2/logical"
)

func metadataForAssociationActivation(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
) (*metadataRecord, *logical.Response, error) {
	metadata, err := getMetadata(ctx, storage, record.Path)
	if err != nil {
		return nil, nil, err
	}
	if metadata == nil || metadata.CurrentVersion == 0 {
		return nil, logical.ErrorResponse("source path does not exist"), nil
	}
	return metadata, nil, nil
}

func validateAssociationDestination(
	ctx context.Context,
	storage logical.Storage,
	record associationRecord,
	cfg globalConfig,
) error {
	destination, err := getDestination(ctx, storage, record.DestinationType, record.DestinationName)
	if err != nil {
		return err
	}
	if destination == nil {
		return fmt.Errorf("destination %s does not exist", record.DestinationRef)
	}
	if destination.Disabled {
		return fmt.Errorf("destination %s is disabled", record.DestinationRef)
	}
	_, version, err := currentSourceVersion(ctx, storage, record.Path)
	if err != nil {
		return err
	}
	if err := validateAssociationDestinationPolicy(*destination, record, version.Data, cfg); err != nil {
		return err
	}
	return nil
}

func validateAssociationDestinationPolicy(
	destination destinationRecord,
	record associationRecord,
	data secretPayload,
	cfg globalConfig,
) error {
	if err := validateDestinationDelegationConstraints(destination, record, cfg); err != nil {
		return err
	}
	if !sourcePathAllowed(record.Path, destination.AllowedSourcePathPrefixes) {
		return fmt.Errorf(
			"destination %s does not allow source path %q",
			record.DestinationRef,
			record.Path,
		)
	}
	objectIDs, err := associationObjectIDs(record, data)
	if err != nil {
		return err
	}
	for _, objectID := range objectIDs {
		resolvedName, err := associationResolvedNameForObject(record, objectID)
		if err != nil {
			return err
		}
		if err := validateDestinationPolicyForObject(destination, record, objectID, resolvedName, cfg); err != nil {
			return err
		}
	}
	return nil
}

func validateDestinationPolicyForObject(
	destination destinationRecord,
	record associationRecord,
	objectID string,
	resolvedName string,
	cfg globalConfig,
) error {
	if err := validateDestinationDelegationConstraints(destination, record, cfg); err != nil {
		return err
	}
	if !sourcePathAllowed(record.Path, destination.AllowedSourcePathPrefixes) {
		return fmt.Errorf(
			"destination %s does not allow source path %q",
			record.DestinationRef,
			record.Path,
		)
	}
	if !resolvedNameAllowed(resolvedName, destination.AllowedResolvedNamePrefixes) {
		return fmt.Errorf(
			"destination %s does not allow resolved name %q for object %q",
			record.DestinationRef,
			resolvedName,
			objectID,
		)
	}
	return nil
}

const destinationUnconstrainedBlocker = "destination_unconstrained"

func destinationDelegationConstraintBlockers(destination destinationRecord, cfg globalConfig) []string {
	if !destinationConstraintsRequired(cfg) || destinationHasDelegationConstraints(destination) {
		return nil
	}
	return []string{destinationUnconstrainedBlocker}
}

func destinationHasDelegationConstraints(destination destinationRecord) bool {
	return len(destination.AllowedSourcePathPrefixes) > 0 &&
		len(destination.AllowedResolvedNamePrefixes) > 0
}

func validateDestinationDelegationConstraints(
	destination destinationRecord,
	record associationRecord,
	cfg globalConfig,
) error {
	if len(destinationDelegationConstraintBlockers(destination, cfg)) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s: security_posture=hardened requires destination %s to set "+
			"allowed_source_path_prefixes and allowed_resolved_name_prefixes",
		destinationUnconstrainedBlocker,
		record.DestinationRef,
	)
}

func sourcePathAllowed(path string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func resolvedNameAllowed(name string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	name = strings.TrimLeft(name, "/")
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(prefix, "/")
		if prefix == "" {
			continue
		}
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}
