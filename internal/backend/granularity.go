package backend

import (
	"fmt"
	"sort"
	"strings"
)

func associationObjectIDs(association associationRecord, data secretPayload) ([]string, error) {
	switch association.Granularity {
	case syncGranularitySecretPath:
		return []string{syncObjectIDSecretPath}, nil
	case syncGranularitySecretKey:
		ids := make([]string, 0, len(data))
		for key := range data {
			if err := validateSecretKeyObjectID(key); err != nil {
				return nil, err
			}
			ids = append(ids, key)
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			return nil, fmt.Errorf("secret-key granularity requires at least one source key")
		}
		return ids, nil
	default:
		return nil, fmt.Errorf("unsupported granularity %q", association.Granularity)
	}
}

func validateSecretKeyObjectID(key string) error {
	if strings.TrimSpace(key) != key || key == "" {
		return fmt.Errorf("secret-key object key must not be empty or have surrounding whitespace")
	}
	if strings.Contains(key, "/") || key == "." || key == ".." {
		return fmt.Errorf("secret-key object key %q is not supported", key)
	}
	return nil
}

func defaultNameTemplateForGranularity(granularity string) string {
	if granularity == syncGranularitySecretKey {
		return defaultPerKeyNameTemplate
	}
	return defaultNameTemplate
}

func associationResolvedNameForObject(record associationRecord, objectID string) (string, error) {
	switch record.Granularity {
	case syncGranularitySecretPath:
		return record.ResolvedName, nil
	case syncGranularitySecretKey:
		if err := validateSecretKeyObjectID(objectID); err != nil {
			return "", err
		}
		return renderAssociationObjectName(
			record.NameTemplate,
			record.Path,
			record.DestinationType,
			record.DestinationName,
			objectID,
		)
	default:
		return "", fmt.Errorf("unsupported granularity %q", record.Granularity)
	}
}

func renderAssociationObjectName(
	template string,
	path string,
	destinationType string,
	destinationName string,
	key string,
) (string, error) {
	rendered := strings.NewReplacer(
		"{{ path }}", path,
		"{{ key }}", key,
		"{{ destination.type }}", destinationType,
		"{{ destination.name }}", destinationName,
	).Replace(template)
	if strings.Contains(rendered, "{{") || strings.Contains(rendered, "}}") {
		return "", fmt.Errorf("unsupported name_template %q", template)
	}
	rendered = strings.Trim(rendered, "/")
	if rendered == "" {
		return "", fmt.Errorf("resolved_name must not be empty")
	}
	return rendered, nil
}
