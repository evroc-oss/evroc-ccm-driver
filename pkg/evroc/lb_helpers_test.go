// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	sdkconfig "github.com/evroc-oss/evroc-go-sdk/config"
	"github.com/evroc-oss/evroc-go-sdk/loadbalancer"
)

type sdkRecorder struct {
	conflict            bool
	missingLoadBalancer bool
	lbName              string
	listener            string
	methods             []string
	patches             map[string]map[string]any
	deletes             map[string]bool
}

func (r *sdkRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.methods = append(r.methods, req.Method+" "+req.URL.Path)
	w.Header().Set("Content-Type", "application/json")

	if req.Method == http.MethodPost && r.conflict {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"reason":"already exists"}`))
		return
	}
	if req.Method == http.MethodPatch {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.patches[req.URL.Path] = body
	}
	if req.Method == http.MethodDelete {
		r.deletes[path.Base(req.URL.Path)] = true
		if r.missingLoadBalancer && strings.Contains(req.URL.Path, "/loadBalancers/") {
			writeJSON(w, http.StatusNotFound, map[string]any{"reason": "not found"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/l4Routes") {
		items := []any{}
		if r.conflict {
			items = []any{
				map[string]any{"metadata": map[string]any{"id": lbRouteName(r.lbName, r.listener)}},
				map[string]any{"metadata": map[string]any{"id": lbRouteName(r.lbName, "old-90")}},
				map[string]any{"metadata": map[string]any{"id": "another-lb-old-90-route"}},
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/backendServices") {
		items := []any{}
		if r.conflict {
			items = []any{
				map[string]any{"metadata": map[string]any{"id": lbBackendServiceName(r.lbName, r.listener)}},
				map[string]any{"metadata": map[string]any{"id": lbBackendServiceName(r.lbName, "old-90")}},
				map[string]any{"metadata": map[string]any{"id": "another-lb-old-90-svc"}},
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}

	name := path.Base(req.URL.Path)
	switch {
	case strings.Contains(req.URL.Path, "/backendPools"):
		writeJSON(w, successStatus(req.Method), map[string]any{
			"metadata": map[string]any{"id": name},
			"spec":     map[string]any{},
		})
	case strings.Contains(req.URL.Path, "/backendServices"):
		writeJSON(w, successStatus(req.Method), map[string]any{
			"metadata": map[string]any{"id": name},
			"spec":     map[string]any{"port": 30080},
		})
	case strings.Contains(req.URL.Path, "/l4Routes"):
		writeJSON(w, successStatus(req.Method), map[string]any{
			"metadata": map[string]any{"id": name},
			"spec":     map[string]any{"defaultBackendServiceRef": "ref"},
		})
	case strings.Contains(req.URL.Path, "/loadBalancers"):
		writeJSON(w, successStatus(req.Method), map[string]any{
			"metadata": map[string]any{"id": r.lbName},
			"spec":     map[string]any{"publicIPRef": "/networking/projects/p/regions/r/publicIPs/ip"},
		})
	default:
		http.NotFound(w, req)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func successStatus(method string) int {
	if method == http.MethodPost {
		return http.StatusCreated
	}
	return http.StatusOK
}

func testSDKLBClient(t *testing.T, recorder *sdkRecorder) *loadbalancer.Client {
	t.Helper()
	httpClient := &http.Client{Transport: roundTripperFunc(func(req *http.Request) *http.Response {
		response := httptest.NewRecorder()
		recorder.ServeHTTP(response, req)
		return response.Result()
	})}
	client, err := evroc.New(context.Background(), sdkconfig.Config{
		Auth: sdkconfig.AuthConfig{Token: "test-token", ClientID: "test", TokenURL: "https://auth.test/token"},
		API:  sdkconfig.APIConfig{BaseURL: "https://api.test"},
		Context: sdkconfig.ContextConfig{
			Organization: "test-org", Project: "test-project", Region: "se-sto",
		},
	}, evroc.WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("create SDK client: %v", err)
	}
	return client.LoadBalancer()
}

type roundTripperFunc func(*http.Request) *http.Response

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

func TestLBEnsureSDKContract(t *testing.T) {
	const lbName, listener = "ccm-0123456789abcdef01234567", "http-80"
	opts := lbEnsureOptions{
		Name:        lbName,
		PublicIPRef: "/networking/projects/p/regions/r/publicIPs/ip",
		Listeners: []listenerInput{
			{
				Name:         listener,
				FrontendPort: 80,
				BackendPort:  30080,
			},
		},
		BackendRefs: []string{"/compute/projects/p/regions/r/virtualMachines/node-a"},
	}

	t.Run("creates complete resource graph", func(t *testing.T) {
		recorder := &sdkRecorder{
			lbName:   lbName,
			listener: listener,
			patches:  map[string]map[string]any{},
			deletes:  map[string]bool{},
		}
		if _, err := lbEnsure(context.Background(), testSDKLBClient(t, recorder), opts); err != nil {
			t.Fatalf("lbEnsure() error = %v", err)
		}
		if got := countMethod(recorder.methods, http.MethodPost); got != 4 {
			t.Errorf("POST count = %d, want pool + service + route + LB", got)
		}
		if len(recorder.patches) != 0 || len(recorder.deletes) != 0 {
			t.Errorf("new graph unexpectedly patched/deleted resources: patches=%v deletes=%v", recorder.patches, recorder.deletes)
		}
	})

	t.Run("patches graph and removes stale listeners", func(t *testing.T) {
		recorder := &sdkRecorder{
			conflict: true,
			lbName:   lbName,
			listener: listener,
			patches:  map[string]map[string]any{},
			deletes:  map[string]bool{},
		}
		if _, err := lbEnsure(context.Background(), testSDKLBClient(t, recorder), opts); err != nil {
			t.Fatalf("lbEnsure() error = %v", err)
		}
		if len(recorder.patches) != 4 {
			t.Errorf("PATCH count = %d, want all four resource types", len(recorder.patches))
		}
		if !recorder.deletes[lbRouteName(lbName, "old-90")] || !recorder.deletes[lbBackendServiceName(lbName, "old-90")] {
			t.Errorf("stale listener resources were not deleted: %v", recorder.deletes)
		}
		if recorder.deletes["another-lb-old-90-route"] || recorder.deletes["another-lb-old-90-svc"] {
			t.Errorf("foreign resources were deleted: %v", recorder.deletes)
		}

		for requestPath, patch := range recorder.patches {
			if strings.Contains(requestPath, "/loadBalancers/") {
				spec := patch["spec"].(map[string]any)
				if _, ok := spec["backendNetwork"]; ok {
					t.Errorf("load balancer patch includes immutable backendNetwork: %v", spec)
				}
			}
		}
	})
}

func TestLBCleanupDiscoversResourcesWithoutLoadBalancer(t *testing.T) {
	const lbName = "ccm-0123456789abcdef01234567"
	// Simulate reconciliation stopping after the subordinate resources were
	// created but before the top-level load balancer was created.
	recorder := &sdkRecorder{
		conflict:            true,
		missingLoadBalancer: true,
		lbName:              lbName,
		listener:            "orphan-80",
		patches:             map[string]map[string]any{},
		deletes:             map[string]bool{},
	}
	if err := lbCleanup(context.Background(), testSDKLBClient(t, recorder), lbName, nil); err != nil {
		t.Fatalf("lbCleanup() error = %v", err)
	}
	for _, name := range []string{
		lbName,
		lbPoolName(lbName),
		lbRouteName(lbName, "orphan-80"),
		lbBackendServiceName(lbName, "orphan-80"),
	} {
		if !recorder.deletes[name] {
			t.Errorf("resource %q was not deleted; deletes=%v", name, recorder.deletes)
		}
	}
}

func countMethod(methods []string, method string) int {
	count := 0
	for _, entry := range methods {
		if strings.HasPrefix(entry, method+" ") {
			count++
		}
	}
	return count
}
