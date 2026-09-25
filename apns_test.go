package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPNsNotifierSendsAuthenticatedAlert(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/3/device/abcdef" {
			t.Fatalf("unexpected APNs request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("apns-topic") != "com.example.sbb" || r.Header.Get("apns-push-type") != "alert" {
			t.Fatalf("unexpected APNs headers: %#v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["event_type"] != "trip_canceled" {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewAPNsNotifier(APNsConfig{
		TeamID: "TEAM123", KeyID: "KEY123", BundleID: "com.example.sbb",
		PrivateKey: privateKey, PushType: "alert", Priority: "10",
		Endpoint: server.URL, HTTPClient: server.Client(),
	})
	err = notifier.Send(journeyNotification{
		Platform: "ios", DeviceToken: "<ab cd ef>", EventType: "trip_canceled",
		JourneyID: "journey-1", Title: "Canceled", Body: "Trip canceled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("expected one APNs request, got %d", requests)
	}
}

func TestAPNsNotifierReturnsProviderReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"reason":"BadDeviceToken"}`))
	}))
	defer server.Close()
	privateKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notifier := NewAPNsNotifier(APNsConfig{
		TeamID: "TEAM123", KeyID: "KEY123", BundleID: "com.example.sbb",
		PrivateKey: privateKey, PushType: "alert", Priority: "10",
		Endpoint: server.URL, HTTPClient: server.Client(),
	})
	err := notifier.Send(journeyNotification{Platform: "ios", DeviceToken: "abcdef", Title: "Test", Body: "Test"})
	if err == nil || !strings.Contains(err.Error(), "BadDeviceToken") {
		t.Fatalf("expected APNs provider reason, got %v", err)
	}
}

func TestParseAPNsPrivateKey(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseAPNsPrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Curve != elliptic.P256() {
		t.Fatalf("expected P-256 key, got %v", parsed.Curve)
	}
}

func TestParseTestMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "object", body: `{"message":"hello"}`, want: "hello"},
		{name: "json string", body: `"hello"`, want: "hello"},
		{name: "raw text", body: "hello", want: "hello"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseTestMessage([]byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestParseTestMessageRejectsEmptyMessage(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"message":""}`} {
		if _, err := parseTestMessage([]byte(body)); err == nil {
			t.Fatalf("expected empty message %q to fail", body)
		}
	}
}
