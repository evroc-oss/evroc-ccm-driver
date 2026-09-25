// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	networkingtypes "github.com/evroc-oss/evroc-go-sdk/types/networking"
)

// Exercise ownership through real SDK serialization, including conflicts and
// direct deletes. The recorder supplies the rest of the resource graph.
func ownershipHandler(t *testing.T, recorder *sdkRecorder, foreignKind, actualOwner string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			var body struct {
				Metadata struct {
					UserLabels map[string]string `json:"userLabels"`
				} `json:"metadata"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Metadata.UserLabels[managedByLabel] != "cluster-a" {
				t.Errorf("unlabelled create: %s", req.URL.Path)
			}
		}
		if req.Method == http.MethodGet && (strings.HasSuffix(req.URL.Path, "/l4Routes") || strings.HasSuffix(req.URL.Path, "/backendServices")) {
			if got := req.URL.Query().Get("labelSelector"); got != "managed-by=cluster-a" {
				t.Errorf("unscoped discovery: %s", req.URL)
			}
		}
		response := httptest.NewRecorder()
		recorder.ServeHTTP(response, req)
		var body map[string]any
		if json.Unmarshal(response.Body.Bytes(), &body) == nil {
			if metadata, ok := body["metadata"].(map[string]any); ok {
				owner := "cluster-a"
				if foreignKind != "" && strings.Contains(req.URL.Path, "/"+foreignKind+"/") {
					owner = actualOwner
				}
				metadata["userLabels"] = map[string]string{managedByLabel: owner}
			}
			writeJSON(w, response.Code, body)
		} else {
			w.WriteHeader(response.Code)
		}
	})
}

func TestOwnershipReconcileAndCleanup(t *testing.T) {
	const name = "ccm-0123456789abcdef01234567"
	for _, kind := range []string{"", "loadBalancers", "backendPools", "backendServices", "l4Routes"} {
		for _, actual := range []string{"cluster-b", ""} {
			t.Run(kind+"/"+actual, func(t *testing.T) {
				recorder := &sdkRecorder{conflict: true, lbName: name, listener: "http-80", patches: map[string]map[string]any{}, deletes: map[string]bool{}}
				client := testSDKLBClient(t, ownershipHandler(t, recorder, kind, actual))
				opts := lbEnsureOptions{Name: name, Owner: "cluster-a", PublicIPRef: "ref", Listeners: []listenerInput{{Name: "http-80", FrontendPort: 80, BackendPort: 30080}}}
				_, err := lbEnsure(context.Background(), client, opts)
				if (err != nil) != (kind != "") {
					t.Fatalf("ensure error=%v, foreign kind=%q", err, kind)
				}
				err = lbCleanup(context.Background(), client, name, []string{"http-80"}, "cluster-a")
				if (err != nil) != (kind != "") {
					t.Fatalf("cleanup error=%v, foreign kind=%q", err, kind)
				}
				if kind != "" {
					for _, request := range recorder.methods {
						if (strings.HasPrefix(request, "PATCH ") || strings.HasPrefix(request, "DELETE ")) && strings.Contains(request, "/"+kind+"/") {
							t.Errorf("modified foreign resource: %s", request)
						}
					}
				}
			})
		}
	}
}

func TestManagedPublicIPOwnership(t *testing.T) {
	for _, owner := range []string{"cluster-a", "cluster-b", ""} {
		t.Run(owner, func(t *testing.T) {
			ip := testPublicIP("managed-ip", "192.0.2.1")
			labels := networkingtypes.UserLabels{managedByLabel: owner}
			ip.Metadata.UserLabels = &labels
			ips := &recordingPublicIPs{ip: ip}
			lb := newTestLB(&recordingLBClient{}, ips)
			lb.config.CCM.Identifier = "cluster-a"
			_, err := lb.ensurePublicIP(context.Background(), "test-lb", testService("api", "default"))
			if (err != nil) != (owner != "cluster-a") {
				t.Fatalf("reuse error=%v owner=%q", err, owner)
			}
			err = lb.deleteManagedIP(context.Background(), "managed-ip")
			if (err != nil) != (owner != "cluster-a") || ips.deleted != (owner == "cluster-a") {
				t.Fatalf("delete error=%v deleted=%v owner=%q", err, ips.deleted, owner)
			}
		})
	}
	req := newPublicIPRequest("managed-ip", "cluster-a")
	if got := resourceLabels(req.Metadata.UserLabels)[managedByLabel]; got != "cluster-a" {
		t.Fatalf("public IP owner=%q", got)
	}
}

func TestEmptyOwnerCannotAdoptResources(t *testing.T) {
	for _, labels := range []map[string]string{nil, {managedByLabel: "another-installation"}} {
		if err := resourceOwner("").check("resource", labels); err == nil {
			t.Fatal("empty owner accepted resource")
		}
	}
}

func TestDefaultPublicIPOwnershipUsesProject(t *testing.T) {
	lb := newTestLB(&recordingLBClient{}, &recordingPublicIPs{})
	ip := testPublicIP("managed-ip", "192.0.2.1")
	if err := lb.checkManagedIP(ip); err != nil {
		t.Fatalf("project-owned IP rejected: %v", err)
	}
	ip.Metadata.UserLabels = nil
	if err := lb.checkManagedIP(ip); err == nil {
		t.Fatal("default owner adopted unlabeled IP")
	}
	req := newPublicIPRequest("managed-ip", resourceOwner(lb.config.CCMIdentifier()))
	if got := resourceLabels(req.Metadata.UserLabels)[managedByLabel]; got != "test-project" {
		t.Fatalf("created IP owner = %q", got)
	}
}
