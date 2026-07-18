package backend

import (
	"context"
	"errors"
	"strings"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func sourceMetadataExistenceCheck(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (bool, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return false, nil
	}
	metadata, err := getMetadata(ctx, req.Storage, path)
	return existenceCheckResult(metadata != nil, err)
}

func destinationExistenceCheck(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (bool, error) {
	destinationType := strings.TrimSpace(data.Get("type").(string))
	name := strings.TrimSpace(data.Get("name").(string))
	if destinationType == "" || name == "" {
		return false, nil
	}
	destination, err := getDestination(ctx, req.Storage, destinationType, name)
	return existenceCheckResult(destination != nil, err)
}

func (b *secretSyncBackend) associationExistenceCheck(
	ctx context.Context,
	req *logical.Request,
	data *framework.FieldData,
) (bool, error) {
	path, err := normalizeSourcePath(data.Get("path").(string))
	if err != nil {
		return false, nil
	}
	selection, err := b.associationSelectionForWrite(ctx, req.Storage, path, data)
	if isAssociationWriteSelectorError(err) {
		return false, nil
	}
	return existenceCheckResult(selection.matchCount > 0, err)
}

func existenceCheckResult(exists bool, err error) (bool, error) {
	if err == nil {
		return exists, nil
	}
	if errors.Is(err, logical.ErrReadOnly) ||
		errors.Is(err, logical.ErrSetupReadOnly) ||
		strings.Contains(err.Error(), logical.ErrReadOnly.Error()) ||
		strings.Contains(err.Error(), logical.ErrSetupReadOnly.Error()) {
		return false, nil
	}
	return false, err
}
