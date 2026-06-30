package backend

type responseEntry struct {
	key   string
	value interface{} //nolint:forbidigo // OpenBao SDK responses require map[string]interface{} values.
}

func responseField(key string, value interface{}) responseEntry { //nolint:forbidigo
	return responseEntry{key: key, value: value}
}

func newResponseData(fields ...responseEntry) map[string]interface{} { //nolint:forbidigo
	data := make(map[string]interface{}, len(fields)) //nolint:forbidigo
	for _, field := range fields {
		data[field.key] = field.value
	}
	return data
}
