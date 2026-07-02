package backend

import "github.com/openbao/openbao/sdk/v2/framework"

const (
	listAfterField = "after"
	listLimitField = "limit"
)

type listPagination struct {
	after string
	limit int
}

func paginationFields() map[string]*framework.FieldSchema {
	return map[string]*framework.FieldSchema{
		listAfterField: {
			Type:        framework.TypeString,
			Description: "Optional entry to begin listing after. The entry does not need to exist.",
		},
		listLimitField: {
			Type:        framework.TypeInt,
			Description: "Optional maximum number of entries to return. Non-positive values return all entries.",
		},
	}
}

func withPaginationFields(fields map[string]*framework.FieldSchema) map[string]*framework.FieldSchema {
	merged := make(map[string]*framework.FieldSchema, len(fields)+2)
	for key, field := range fields {
		merged[key] = field
	}
	for key, field := range paginationFields() {
		merged[key] = field
	}
	return merged
}

func listPaginationFromFieldData(data *framework.FieldData) listPagination {
	return listPagination{
		after: data.Get(listAfterField).(string),
		limit: data.Get(listLimitField).(int),
	}
}
