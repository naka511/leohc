package store

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRedisAddress = "127.0.0.1:6379"
	defaultRedisPrefix  = "leo-go"
	tokensRedisKey      = "tokens"
)

// RedisStore provides lightweight JSON persistence over Redis without an external dependency.
type RedisStore struct {
	mu          sync.Mutex
	addr        string
	username    string
	password    string
	db          int
	keyPrefix   string
	useTLS      bool
	dialTimeout time.Duration
}

// NewRedisStoreFromEnv builds a Redis store from environment variables.
func NewRedisStoreFromEnv() (*RedisStore, error) {
	redisURL := strings.TrimSpace(os.Getenv("REDIS_URL"))
	if redisURL != "" {
		return NewRedisStoreFromURL(redisURL)
	}

	host := strings.TrimSpace(os.Getenv("REDIS_HOST"))
	port := strings.TrimSpace(os.Getenv("REDIS_PORT"))
	password := strings.TrimSpace(os.Getenv("REDIS_PASSWORD"))
	username := strings.TrimSpace(os.Getenv("REDIS_USERNAME"))
	if host == "" && port == "" && password == "" && username == "" {
		return nil, nil
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "6379"
	}
	db := 0
	if rawDB := strings.TrimSpace(os.Getenv("REDIS_DB")); rawDB != "" {
		if parsed, err := strconv.Atoi(rawDB); err == nil && parsed >= 0 {
			db = parsed
		}
	}
	prefix := strings.TrimSpace(os.Getenv("REDIS_KEY_PREFIX"))
	if prefix == "" {
		prefix = defaultRedisPrefix
	}
	useTLS := strings.EqualFold(strings.TrimSpace(os.Getenv("REDIS_TLS")), "true") || strings.TrimSpace(os.Getenv("REDIS_TLS")) == "1"

	return &RedisStore{
		addr:        net.JoinHostPort(host, port),
		username:    username,
		password:    password,
		db:          db,
		keyPrefix:   prefix,
		useTLS:      useTLS,
		dialTimeout: 5 * time.Second,
	}, nil
}

// NewRedisStoreFromURL parses a redis:// or rediss:// URL.
func NewRedisStoreFromURL(rawURL string) (*RedisStore, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "redis", "rediss":
	default:
		return nil, fmt.Errorf("unsupported redis scheme %q", parsed.Scheme)
	}

	addr := parsed.Host
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "6379")
	}

	db := 0
	if rawPath := strings.Trim(strings.TrimSpace(parsed.Path), "/"); rawPath != "" {
		parsedDB, convErr := strconv.Atoi(rawPath)
		if convErr != nil || parsedDB < 0 {
			return nil, fmt.Errorf("invalid redis db %q", rawPath)
		}
		db = parsedDB
	}

	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}

	prefix := strings.TrimSpace(parsed.Query().Get("prefix"))
	if prefix == "" {
		prefix = strings.TrimSpace(os.Getenv("REDIS_KEY_PREFIX"))
	}
	if prefix == "" {
		prefix = defaultRedisPrefix
	}

	return &RedisStore{
		addr:        addr,
		username:    username,
		password:    password,
		db:          db,
		keyPrefix:   prefix,
		useTLS:      strings.EqualFold(parsed.Scheme, "rediss"),
		dialTimeout: 5 * time.Second,
	}, nil
}

// Address returns the configured host:port.
func (s *RedisStore) Address() string {
	if s == nil {
		return ""
	}
	return s.addr
}

// DB returns the configured logical database index.
func (s *RedisStore) DB() int {
	if s == nil {
		return 0
	}
	return s.db
}

// KeyPrefix returns the configured key namespace prefix.
func (s *RedisStore) KeyPrefix() string {
	if s == nil {
		return ""
	}
	return s.keyPrefix
}

// Ping validates connectivity and authentication.
func (s *RedisStore) Ping() error {
	raw, err := s.do("PING")
	if err != nil {
		return err
	}
	if strings.ToUpper(strings.TrimSpace(raw)) != "PONG" {
		return fmt.Errorf("unexpected ping response: %s", raw)
	}
	return nil
}

// LoadTokens reads persisted tokens from Redis.
func (s *RedisStore) LoadTokens() ([]map[string]interface{}, error) {
	var tokens []map[string]interface{}
	err := s.LoadJSON(tokensRedisKey, &tokens)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return tokens, nil
}

// ReplaceTokens replaces the full token set in Redis.
func (s *RedisStore) ReplaceTokens(tokens []map[string]interface{}) error {
	return s.SaveJSON(tokensRedisKey, tokens)
}

// LoadJSON reads a JSON blob from Redis into dst.
func (s *RedisStore) LoadJSON(key string, dst interface{}) error {
	raw, err := s.do("GET", s.prefixedKey(key))
	if err != nil {
		if os.IsNotExist(err) {
			return os.ErrNotExist
		}
		return err
	}
	if strings.TrimSpace(raw) == "" {
		return os.ErrNotExist
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("decode redis json for %s: %w", key, err)
	}
	return nil
}

// SaveJSON writes a JSON blob to Redis.
func (s *RedisStore) SaveJSON(key string, value interface{}) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode redis json for %s: %w", key, err)
	}
	_, err = s.do("SET", s.prefixedKey(key), string(payload))
	return err
}

func (s *RedisStore) prefixedKey(key string) string {
	key = strings.TrimSpace(key)
	prefix := strings.TrimSpace(s.keyPrefix)
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}
	return prefix + ":" + key
}

func (s *RedisStore) do(args ...string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("redis store is nil")
	}
	if strings.TrimSpace(s.addr) == "" {
		return "", fmt.Errorf("redis address is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	conn, reader, writer, err := s.open()
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if err := writeRESPArray(writer, args); err != nil {
		return "", err
	}
	return readRESPValue(reader)
}

func (s *RedisStore) open() (net.Conn, *bufio.Reader, *bufio.Writer, error) {
	dialer := &net.Dialer{Timeout: s.dialTimeout}
	var (
		conn net.Conn
		err  error
	)
	if s.useTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", s.addr, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: splitRedisHost(s.addr),
		})
	} else {
		conn, err = dialer.Dial("tcp", s.addr)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial redis %s: %w", s.addr, err)
	}

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	if err := s.authenticate(reader, writer); err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	if s.db > 0 {
		if err := writeRESPArray(writer, []string{"SELECT", strconv.Itoa(s.db)}); err != nil {
			conn.Close()
			return nil, nil, nil, err
		}
		if _, err := readRESPValue(reader); err != nil {
			conn.Close()
			return nil, nil, nil, fmt.Errorf("select redis db %d: %w", s.db, err)
		}
	}
	return conn, reader, writer, nil
}

func (s *RedisStore) authenticate(reader *bufio.Reader, writer *bufio.Writer) error {
	if strings.TrimSpace(s.password) == "" {
		return nil
	}
	args := []string{"AUTH"}
	if strings.TrimSpace(s.username) != "" {
		args = append(args, s.username)
	}
	args = append(args, s.password)
	if err := writeRESPArray(writer, args); err != nil {
		return err
	}
	if _, err := readRESPValue(reader); err != nil {
		return fmt.Errorf("redis auth failed: %w", err)
	}
	return nil
}

func writeRESPArray(writer *bufio.Writer, args []string) error {
	if _, err := writer.WriteString(fmt.Sprintf("*%d\r\n", len(args))); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := writer.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(arg), arg)); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func readRESPValue(reader *bufio.Reader) (string, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")

	switch prefix {
	case '+', ':':
		return line, nil
	case '-':
		return "", errors.New(line)
	case '$':
		size, convErr := strconv.Atoi(line)
		if convErr != nil {
			return "", convErr
		}
		if size < 0 {
			return "", os.ErrNotExist
		}
		payload := make([]byte, size+2)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return "", err
		}
		return string(payload[:size]), nil
	default:
		return "", fmt.Errorf("unsupported redis response type %q", string(prefix))
	}
}

func splitRedisHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return addr
}
