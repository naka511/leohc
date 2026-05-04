package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Manager handles application configuration from JSON file and environment variables.
type Manager struct {
	mu       sync.RWMutex
	data     map[string]interface{}
	filePath string
}

var (
	instance *Manager
	once     sync.Once
)

// Global returns the singleton Manager instance.
func Global() *Manager {
	once.Do(func() {
		instance = &Manager{data: make(map[string]interface{})}
	})
	return instance
}

// Load reads configuration from a JSON file.
func (m *Manager) Load(filePath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.filePath = filePath

	raw, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	m.data = data
	return nil
}

// Save writes current configuration to the JSON file.
func (m *Manager) Save() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.filePath == "" {
		return nil
	}
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.filePath, raw, 0o644)
}

// Get returns a config value by key, with optional default.
func (m *Manager) Get(key string, defaultVal ...interface{}) interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.data[key]; ok {
		return v
	}
	if len(defaultVal) > 0 {
		return defaultVal[0]
	}
	return nil
}

// GetString returns a config string value.
func (m *Manager) GetString(key string, defaultVal ...string) string {
	v := m.Get(key)
	if v == nil {
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return ""
	}
	return strings.TrimSpace(toString(v))
}

// GetInt returns a config integer value.
func (m *Manager) GetInt(key string, defaultVal ...int) int {
	v := m.Get(key)
	if v == nil {
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil {
			if len(defaultVal) > 0 {
				return defaultVal[0]
			}
			return 0
		}
		return n
	default:
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return 0
	}
}

// GetBool returns a config boolean value.
func (m *Manager) GetBool(key string, defaultVal ...bool) bool {
	v := m.Get(key)
	if v == nil {
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	case string:
		return strings.EqualFold(val, "true") || val == "1"
	default:
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return false
	}
}

// GetStringSlice returns a config string slice value.
func (m *Manager) GetStringSlice(key string, defaultVal ...[]string) []string {
	v := m.Get(key)
	if v == nil {
		if len(defaultVal) > 0 {
			return append([]string(nil), defaultVal[0]...)
		}
		return nil
	}

	appendString := func(out []string, raw string) []string {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return out
		}
		return append(out, raw)
	}

	var out []string
	switch val := v.(type) {
	case []string:
		for _, item := range val {
			out = appendString(out, item)
		}
	case []interface{}:
		for _, item := range val {
			out = appendString(out, toString(item))
		}
	case string:
		for _, item := range strings.Split(val, ",") {
			out = appendString(out, item)
		}
	default:
		if len(defaultVal) > 0 {
			return append([]string(nil), defaultVal[0]...)
		}
	}
	return out
}

// GetIntSlice returns a config integer slice value.
func (m *Manager) GetIntSlice(key string, defaultVal ...[]int) []int {
	v := m.Get(key)
	if v == nil {
		if len(defaultVal) > 0 {
			return append([]int(nil), defaultVal[0]...)
		}
		return nil
	}

	appendInt := func(out []int, raw string) []int {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return out
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return out
		}
		return append(out, n)
	}

	var out []int
	switch val := v.(type) {
	case []int:
		out = append(out, val...)
	case []interface{}:
		for _, item := range val {
			switch itemVal := item.(type) {
			case float64:
				out = append(out, int(itemVal))
			case int:
				out = append(out, itemVal)
			case string:
				out = appendInt(out, itemVal)
			}
		}
	case string:
		for _, item := range strings.Split(val, ",") {
			out = appendInt(out, item)
		}
	default:
		if len(defaultVal) > 0 {
			return append([]int(nil), defaultVal[0]...)
		}
	}
	return out
}

// GetFloat returns a config float64 value.
func (m *Manager) GetFloat(key string, defaultVal ...float64) float64 {
	v := m.Get(key)
	if v == nil {
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			if len(defaultVal) > 0 {
				return defaultVal[0]
			}
			return 0
		}
		return f
	default:
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return 0
	}
}

// GetAll returns a copy of the full config map.
func (m *Manager) GetAll() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]interface{}, len(m.data))
	for k, v := range m.data {
		out[k] = v
	}
	return out
}

// Set sets a config value.
func (m *Manager) Set(key string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
}

// SetAll replaces the entire config map.
func (m *Manager) SetAll(data map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = data
}

// FilePath returns the loaded config file path.
func (m *Manager) FilePath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.filePath
}

func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}
