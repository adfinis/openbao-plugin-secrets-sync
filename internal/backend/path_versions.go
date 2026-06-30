package backend

import (
	"context"
	"fmt"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func pathVersionMutations(_ *secretSyncBackend) []*framework.Path {
	fields := map[string]*framework.FieldSchema{
		"path": {
			Type:        framework.TypeString,
			Description: "Source secret path.",
		},
		"versions": {
			Type:        framework.TypeCommaIntSlice,
			Description: "Secret versions to mutate.",
		},
	}
	return []*framework.Path{
		{
			Pattern: "delete/" + framework.MatchAllRegex("path"),
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathDeleteVersionsWrite,
					Summary:  "Soft-delete local source secret versions.",
				},
			},
			HelpSynopsis:    "Delete local versions.",
			HelpDescription: "Sets deletion time on selected local source secret versions.",
		},
		{
			Pattern: "undelete/" + framework.MatchAllRegex("path"),
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathUndeleteWrite,
					Summary:  "Undelete local source secret versions.",
				},
			},
			HelpSynopsis:    "Undelete local versions.",
			HelpDescription: "Clears deletion time on selected local source secret versions.",
		},
		{
			Pattern: "destroy/" + framework.MatchAllRegex("path"),
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{
					Callback: pathDestroyWrite,
					Summary:  "Destroy local source secret versions.",
				},
			},
			HelpSynopsis:    "Destroy local versions.",
			HelpDescription: "Permanently removes payload data from selected local source secret versions.",
		},
	}
}

func pathDeleteVersionsWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	return runVersionMutation(ctx, req, data, softDeleteVersion)
}

func pathUndeleteWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	return runVersionMutation(ctx, req, data, undeleteVersion)
}

func pathDestroyWrite(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (*logical.Response, error) {
	return runVersionMutation(ctx, req, data, destroyVersion)
}

type versionMutationFunc func(
	context.Context,
	logical.Storage,
	*metadataRecord,
	string,
	int,
	string,
) error

func runVersionMutation(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
	mutate versionMutationFunc,
) (*logical.Response, error) {
	path, versions, err := versionMutationRequest(data)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, nil
	}
	now := nowUTC().Format(timeFormatRFC3339)
	for _, version := range versions {
		if err := mutate(ctx, req.Storage, metadata, path, version, now); err != nil {
			return logical.ErrorResponse(err.Error()), nil
		}
	}
	if err := putMetadata(ctx, req.Storage, path, *metadata); err != nil {
		return nil, err
	}
	return nil, nil
}

func versionMutationRequest(data *framework.FieldData) (string, []int, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return "", nil, err
	}
	versions := data.Get("versions").([]int)
	if len(versions) == 0 {
		return "", nil, fmt.Errorf("versions must contain at least one version")
	}
	for _, version := range versions {
		if version <= 0 {
			return "", nil, fmt.Errorf("versions must contain positive integers")
		}
	}
	return path, versions, nil
}

func softDeleteVersion(
	ctx context.Context,
	storage logical.Storage,
	metadata *metadataRecord,
	path string,
	version int,
	now string,
) error {
	record, err := getVersion(ctx, storage, path, version)
	if err != nil {
		return err
	}
	if record == nil || record.Destroyed || record.DeletionTime != "" {
		return nil
	}
	record.DeletionTime = now
	if err := putVersion(ctx, storage, path, *record); err != nil {
		return err
	}
	versionKey := versionMetadataKey(record.Version)
	versionMetadata := metadata.Versions[versionKey]
	versionMetadata.DeletionTime = now
	metadata.Versions[versionKey] = versionMetadata
	metadata.UpdatedTime = now
	return nil
}

func undeleteVersion(
	ctx context.Context,
	storage logical.Storage,
	metadata *metadataRecord,
	path string,
	version int,
	now string,
) error {
	record, err := getVersion(ctx, storage, path, version)
	if err != nil {
		return err
	}
	if record == nil || record.Destroyed {
		return nil
	}
	record.DeletionTime = ""
	if err := putVersion(ctx, storage, path, *record); err != nil {
		return err
	}
	versionKey := versionMetadataKey(version)
	versionMetadata := metadata.Versions[versionKey]
	versionMetadata.DeletionTime = ""
	metadata.Versions[versionKey] = versionMetadata
	metadata.UpdatedTime = now
	return nil
}

func destroyVersion(
	ctx context.Context,
	storage logical.Storage,
	metadata *metadataRecord,
	path string,
	version int,
	now string,
) error {
	record, err := getVersion(ctx, storage, path, version)
	if err != nil {
		return err
	}
	if record == nil || record.Destroyed {
		return nil
	}
	record.Data = nil
	record.Destroyed = true
	record.DeletionTime = ""
	if err := putVersion(ctx, storage, path, *record); err != nil {
		return err
	}
	versionKey := versionMetadataKey(version)
	versionMetadata := metadata.Versions[versionKey]
	versionMetadata.Destroyed = true
	versionMetadata.DeletionTime = ""
	metadata.Versions[versionKey] = versionMetadata
	metadata.UpdatedTime = now
	return nil
}
