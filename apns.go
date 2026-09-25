package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type APNsConfig struct {
	TeamID      string
	KeyID       string
	BundleID    string
	PrivateKey  *ecdsa.PrivateKey
	Environment string
	PushType    string
	Priority    string
	Endpoint    string
	HTTPClient  *http.Client
}

func NewAPNsConfigFromEnv() (APNsConfig, error) {
	keyFile := os.Getenv("APNS_PRIVATE_KEY_FILE")
	if keyFile == "" {
		return APNsConfig{}, fmt.Errorf("APNS_PRIVATE_KEY_FILE is required when APNS_ENABLED=true")
	}
	keyData, err := os.ReadFile(keyFile)
	if err != nil {
		return APNsConfig{}, fmt.Errorf("read APNs private key: %w", err)
	}
	privateKey, err := parseAPNsPrivateKey(keyData)
	if err != nil {
		return APNsConfig{}, err
	}
	config := APNsConfig{
		TeamID:      os.Getenv("APNS_TEAM_ID"),
		KeyID:       os.Getenv("APNS_KEY_ID"),
		BundleID:    os.Getenv("APNS_BUNDLE_ID"),
		PrivateKey:  privateKey,
		Environment: envOrDefault("APNS_ENVIRONMENT", "production"),
		PushType:    envOrDefault("APNS_PUSH_TYPE", "alert"),
		Priority:    envOrDefault("APNS_PRIORITY", "10"),
		Endpoint:    os.Getenv("APNS_ENDPOINT"),
		HTTPClient:  &http.Client{Timeout: 15 * time.Second},
	}
	if config.Endpoint == "" {
		if config.Environment == "development" {
			config.Endpoint = "https://api.development.push.apple.com"
		} else {
			config.Endpoint = "https://api.push.apple.com"
		}
	}
	switch {
	case config.TeamID == "":
		return APNsConfig{}, fmt.Errorf("APNS_TEAM_ID is required when APNS_ENABLED=true")
	case config.KeyID == "":
		return APNsConfig{}, fmt.Errorf("APNS_KEY_ID is required when APNS_ENABLED=true")
	case config.BundleID == "":
		return APNsConfig{}, fmt.Errorf("APNS_BUNDLE_ID is required when APNS_ENABLED=true")
	case config.PushType != "alert":
		return APNsConfig{}, fmt.Errorf("unsupported APNS_PUSH_TYPE %q; alert is currently supported", config.PushType)
	}
	return config, nil
}

func parseAPNsPrivateKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("APNs private key is not PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse APNs private key: %w", err)
	}
	privateKey, ok := key.(*ecdsa.PrivateKey)
	if !ok || privateKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("APNs private key must be an ES256 P-256 key")
	}
	return privateKey, nil
}

type APNsNotifier struct {
	config APNsConfig
	mu     sync.Mutex
	token  string
	issued time.Time
}

func NewAPNsNotifier(config APNsConfig) *APNsNotifier {
	return &APNsNotifier{config: config}
}

func (n *APNsNotifier) Send(event journeyNotification) error {
	if event.Platform != "ios" {
		return fmt.Errorf("APNs cannot deliver platform %q", event.Platform)
	}
	deviceToken := normalizeAPNsDeviceToken(event.DeviceToken)
	if deviceToken == "" {
		return fmt.Errorf("APNs device token is empty")
	}
	payload, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{"title": event.Title, "body": event.Body},
			"sound": "default",
		},
		"event_type": event.EventType,
		"journey_id": event.JourneyID,
		"data":       event.Data,
	})
	if err != nil {
		return fmt.Errorf("marshal APNs payload: %w", err)
	}
	jwt, err := n.jwt()
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(n.config.Endpoint, "/")+"/3/device/"+deviceToken, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create APNs request: %w", err)
	}
	request.Header.Set("authorization", "bearer "+jwt)
	request.Header.Set("apns-topic", n.config.BundleID)
	request.Header.Set("apns-push-type", n.config.PushType)
	request.Header.Set("apns-priority", n.config.Priority)
	request.Header.Set("content-type", "application/json")

	response, err := n.config.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("send APNs notification: %w", err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 16*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(responseBody, &failure)
		if failure.Reason == "" {
			failure.Reason = strings.TrimSpace(string(responseBody))
		}
		return fmt.Errorf("APNs returned HTTP %d: %s", response.StatusCode, failure.Reason)
	}
	return nil
}

func (n *APNsNotifier) jwt() (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.token != "" && time.Since(n.issued) < 50*time.Minute {
		return n.token, nil
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","kid":"` + n.config.KeyID + `"}`))
	claims, err := json.Marshal(map[string]any{"iss": n.config.TeamID, "iat": time.Now().Unix()})
	if err != nil {
		return "", fmt.Errorf("marshal APNs JWT claims: %w", err)
	}
	encodedClaims := base64.RawURLEncoding.EncodeToString(claims)
	signingInput := header + "." + encodedClaims
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, n.config.PrivateKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign APNs JWT: %w", err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	n.token = signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
	n.issued = time.Now()
	return n.token, nil
}

func normalizeAPNsDeviceToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "<")
	token = strings.TrimSuffix(token, ">")
	return strings.ReplaceAll(token, " ", "")
}

func NewConfiguredNotifier() (Notifier, error) {
	if strings.EqualFold(os.Getenv("APNS_ENABLED"), "true") {
		config, err := NewAPNsConfigFromEnv()
		if err != nil {
			return nil, err
		}
		return NewAPNsNotifier(config), nil
	}
	return NewLogNotifier(), nil
}
