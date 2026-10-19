package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHigressConsoleClientPutRoute(t *testing.T) {
	var gotAuth string
	var gotRoute map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/routes/ani-route" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"name": "ani-route"},
			})
			return
		}
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content type = %q", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRoute); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	if err := client.PutRoute(context.Background(), "ani-route", map[string]any{"name": "ani-route"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:secret")) {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotRoute["name"] != "ani-route" {
		t.Fatalf("route = %#v", gotRoute)
	}
}

func TestHigressConsoleClientPutRouteCreatesMissingRoute(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/routes" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	if err := client.PutRoute(context.Background(), "new-route", map[string]any{"name": "new-route"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /v1/routes/new-route", "POST /v1/routes"}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("requests = %#v, want %#v", methods, want)
	}
}

func TestHigressConsoleClientGetRoute404PreservesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	_, err := client.GetRoute(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetRoute() error = nil, want 404")
	}
	var statusErr *higressConsoleStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != http.StatusNotFound {
		t.Fatalf("GetRoute() error = %v, want typed 404", err)
	}
}

func TestHigressConsoleClientGetAndListRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/routes/one" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"message": "success",
				"data":    map[string]any{"name": "one"},
			})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/routes" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":  true,
				"message":  "success",
				"data":     []map[string]any{{"name": "one"}, {"name": "two"}},
				"total":    2,
				"pageNum":  1,
				"pageSize": 10,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	got, err := client.GetRoute(context.Background(), "one")
	if err != nil || got["name"] != "one" {
		t.Fatalf("get route = %#v, err = %v", got, err)
	}
	list, err := client.ListRoutes(context.Background())
	if err != nil || len(list) != 2 || list[1]["name"] != "two" {
		t.Fatalf("list routes = %#v, err = %v", list, err)
	}
}

func TestHigressConsoleClientServiceSourceAndDelete404(t *testing.T) {
	var gotSource map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/service-sources/source" {
			if err := json.NewDecoder(r.Body).Decode(&gotSource); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/service-sources/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	if err := client.PutServiceSource(context.Background(), "source", map[string]any{"address": "svc"}); err != nil {
		t.Fatal(err)
	}
	if gotSource["address"] != "svc" {
		t.Fatalf("source = %#v", gotSource)
	}
	if err := client.DeleteServiceSource(context.Background(), "missing"); err != nil {
		t.Fatal(err)
	}
}

func TestHigressConsoleClientNon2xxErrorDoesNotLeakSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("password=secret response detail"))
	}))
	defer server.Close()

	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	err := client.PutRoute(context.Background(), "private-route", map[string]any{"password": "secret"})
	if err == nil {
		t.Fatal("expected error")
	}
	message := err.Error()
	for _, part := range []string{"GET", "/v1/routes/private-route", "502"} {
		if !strings.Contains(message, part) {
			t.Fatalf("error %q does not contain %q", message, part)
		}
	}
	if strings.Contains(message, "secret") || strings.Contains(message, "password") {
		t.Fatalf("error leaks secret: %q", message)
	}
}

func TestHigressConsoleClientEnvelopeFailureDoesNotLeakSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"message": "password=secret response detail",
		})
	}))
	defer server.Close()

	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	err := client.PutRoute(context.Background(), "private-route", map[string]any{"password": "secret"})
	if err == nil {
		t.Fatal("expected error")
	}
	message := err.Error()
	for _, part := range []string{"GET", "/v1/routes/private-route", "200"} {
		if !strings.Contains(message, part) {
			t.Fatalf("error %q does not contain %q", message, part)
		}
	}
	if strings.Contains(message, "secret") || strings.Contains(message, "password") {
		t.Fatalf("error leaks secret: %q", message)
	}
}
