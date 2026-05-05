package store

// TokenStore is the persistence contract for token records.
type TokenStore interface {
	LoadTokens() ([]map[string]interface{}, error)
	ReplaceTokens(tokens []map[string]interface{}) error
}

// JSONStore is a small key/value JSON persistence contract.
type JSONStore interface {
	LoadJSON(key string, dst interface{}) error
	SaveJSON(key string, value interface{}) error
}
