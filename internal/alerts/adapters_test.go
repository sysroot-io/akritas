package alerts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAlertmanagerAdapterNormalizesEachAlert(t *testing.T) {
	adapter, err := NewAdapter("alertmanager")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"version":"4","groupKey":"group","status":"firing","receiver":"akritas","truncatedAlerts":0,
		"groupLabels":{"alertname":"HighCPU"},"commonLabels":{"environment":"production"},
		"commonAnnotations":{"summary":"CPU high"},"routeLabels":{},"externalURL":"https://alerts.example",
		"alerts":[{"status":"firing","labels":{"alertname":"HighCPU","instance":"pg01","severity":"critical"},
		"annotations":{"description":"CPU above 95%"},"startsAt":"2026-09-04T08:00:00Z","endsAt":"0001-01-01T00:00:00Z",
		"generatorURL":"https://prometheus.example/graph","fingerprint":"abc123"}]
	}`)
	events, err := DecodeWithAdapter(adapter, "alertmanager", "production", body, time.Date(2026, 9, 4, 8, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Name != "HighCPU" || events[0].Entity.ID != "pg01" ||
		events[0].State != StateFiring || events[0].Severity != SeverityCritical ||
		events[0].CorrelationKey == "" || events[0].DeduplicationKey == "" {
		t.Fatalf("unexpected canonical event: %+v", events)
	}
}

func TestUptimeKumaAndPingdomAdapters(t *testing.T) {
	now := time.Date(2026, 9, 4, 8, 1, 0, 0, time.UTC)
	uptime, _ := NewAdapter("uptime-kuma")
	uptimeEvents, err := DecodeWithAdapter(uptime, "uptime-kuma", "home", []byte(`{
		"heartbeat":{"monitorID":10,"status":0,"time":"2026-09-04T08:00:00Z","msg":"timeout","ping":null,"important":true,"duration":60},
		"monitor":{"id":10,"name":"Payments API","type":"http","url":"https://payments.example","hostname":"payments.example","port":443},
		"msg":"Payments API is down"
	}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(uptimeEvents) != 1 || uptimeEvents[0].State != StateFiring || uptimeEvents[0].Entity.ID != "10" {
		t.Fatalf("unexpected Uptime Kuma event: %+v", uptimeEvents)
	}

	pingdom, _ := NewAdapter("pingdom")
	pingdomEvents, err := DecodeWithAdapter(pingdom, "pingdom", "external", []byte(`{
		"check_id":12345,"check_name":"Public API","check_type":"HTTP","check_params":{"full_url":"https://api.example/health","hostname":"api.example","port":443},
		"tags":["production"],"previous_state":"UP","current_state":"DOWN","importance_level":"HIGH",
		"state_changed_timestamp":1788508800,"state_changed_utc_time":"2026-09-04T08:00:00",
		"long_description":"timeout","description":"Connection timeout","first_probe":{},"second_probe":{}
	}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pingdomEvents) != 1 || pingdomEvents[0].State != StateFiring || pingdomEvents[0].Entity.ID != "12345" {
		t.Fatalf("unexpected Pingdom event: %+v", pingdomEvents)
	}
}

func TestRegistryAuthenticatesAndDecodesGenericContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"sources":[{"name":"custom","type":"generic","bearer_token_env":"SOURCE_TOKEN"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := LoadRegistry(path, func(name string) (string, bool) {
		if name == "SOURCE_TOKEN" {
			return "secret", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{"schema_version":1,"alerts":[{
		"schema_version":1,"event_id":"","source":{"type":"","name":"","provider_event_id":"provider-1"},
		"state":"firing","name":"CustomAlert","severity":"warning","summary":"Something happened",
		"entity":{"kind":"service","id":"api"},"started_at":"2026-09-04T08:00:00Z","observed_at":"%s",
		"deduplication_key":"delivery-1","correlation_key":"service-api"
	}]}`, time.Now().UTC().Format(time.RFC3339Nano)))
	header := make(http.Header)
	if _, err := registry.Decode("custom", header, body, time.Now()); err != ErrUnauthorized {
		t.Fatalf("unexpected authentication error: %v", err)
	}
	header.Set("Authorization", "Bearer secret")
	events, err := registry.Decode("custom", header, body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Source.Name != "custom" || events[0].Source.Type != "generic" || events[0].ID != "" {
		t.Fatalf("unexpected generic event: %+v", events[0])
	}
}

func TestShippedAlertSourceConfigurationLoads(t *testing.T) {
	registry, err := LoadRegistry("../../configs/akritas/alerts.example.json", func(name string) (string, bool) {
		if name == "AKRITAS_ALERTMANAGER_TOKEN" {
			return "test-token", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 1 || registry.Names()[0] != "alertmanager-main" {
		t.Fatalf("unexpected source registry: %+v", registry.Names())
	}
}

func TestRegistryVerifiesHMACBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"sources":[{"name":"signed","type":"generic","hmac_secret_env":"SIGNING_SECRET"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := LoadRegistry(path, func(name string) (string, bool) { return "signing-key", name == "SIGNING_SECRET" })
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema_version":1,"alerts":[{"schema_version":1,"state":"resolved","name":"Recovered","severity":"info","summary":"Service recovered","entity":{"kind":"service","id":"api"},"started_at":"2026-09-04T08:00:00Z","observed_at":"2026-09-04T08:01:00Z"}]}`)
	mac := hmac.New(sha256.New, []byte("signing-key"))
	_, _ = mac.Write(body)
	header := make(http.Header)
	header.Set("X-Akritas-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if _, err := registry.Decode("signed", header, body, time.Now()); err != nil {
		t.Fatal(err)
	}
	header.Set("X-Akritas-Signature", "sha256=00")
	if _, err := registry.Decode("signed", header, body, time.Now()); err != ErrUnauthorized {
		t.Fatalf("invalid signature error=%v", err)
	}
}
