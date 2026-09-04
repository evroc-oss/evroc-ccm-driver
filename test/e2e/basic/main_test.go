// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package basic

import (
	"fmt"
	"os"
	"testing"
)

// TestMain checks the prerequisites shared by every e2e test. The Go test
// harness invokes it before any TestXxx function, and the tests run only when
// TestMain calls m.Run.
//
// The cluster itself is provisioned outside the suite by run-e2e.sh, which
// also deploys the CCM. These tests assume the kubelets run with
// --cloud-provider=external, since otherwise nodes are initialized by the
// distribution's own cloud provider and never reach the CCM.
func TestMain(m *testing.M) {
	// Requiring an explicit opt-in prevents an ordinary "go test ./..." from
	// provisioning paid cloud resources merely because the developer happens to
	// have KUBECONFIG set in their shell.
	if os.Getenv("EVROC_E2E") != "1" {
		fmt.Println("skipping e2e tests: set EVROC_E2E=1 to opt in")
		os.Exit(0)
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		fmt.Println("skipping e2e tests: KUBECONFIG is not set")
		os.Exit(0)
	}
	if _, err := os.Stat(kubeconfig); err != nil {
		fmt.Printf("kubeconfig %s is not readable: %v\n", kubeconfig, err)
		os.Exit(1)
	}
	fmt.Printf("using kubeconfig %s\n", kubeconfig)

	os.Exit(m.Run())
}
