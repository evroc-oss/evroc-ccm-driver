// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	"github.com/evroc-oss/evroc-go-sdk/loadbalancer"
)

const managedByLabel = "managed-by"

type resourceOwner string

func (o resourceOwner) labels() map[string]string {
	return map[string]string{managedByLabel: string(o)}
}

func (o resourceOwner) Apply(values url.Values) {
	values.Set("labelSelector", managedByLabel+"="+string(o))
}

func resourceLabels[T ~map[string]string](labels *T) map[string]string {
	if labels == nil {
		return nil
	}
	return map[string]string(*labels)
}

func (o resourceOwner) check(name string, labels map[string]string) error {
	if o == "" || labels[managedByLabel] != string(o) {
		return fmt.Errorf("refusing to manage resource %q: managed-by is %q, expected %q", name, labels[managedByLabel], o)
	}
	return nil
}

// checkLB reads ownership before a mutation. Unlabeled resources are never adopted.
func (o resourceOwner) checkLB(ctx context.Context, client *loadbalancer.Client, kind, name string) error {
	switch kind {
	case "loadBalancer":
		r, err := client.LoadBalancers().Get(ctx, name)
		if err != nil {
			return err
		}
		return o.check(name, resourceLabels(r.Metadata.UserLabels))
	case "backendPool":
		r, err := client.BackendPools().Get(ctx, name)
		if err != nil {
			return err
		}
		return o.check(name, resourceLabels(r.Metadata.UserLabels))
	case "backendService":
		r, err := client.BackendServices().Get(ctx, name)
		if err != nil {
			return err
		}
		return o.check(name, resourceLabels(r.Metadata.UserLabels))
	case "l4Route":
		r, err := client.L4Routes().Get(ctx, name)
		if err != nil {
			return err
		}
		return o.check(name, resourceLabels(r.Metadata.UserLabels))
	default:
		return fmt.Errorf("unknown load balancer resource kind %q", kind)
	}
}

func (o resourceOwner) deleteLB(ctx context.Context, client *loadbalancer.Client, kind, name string) error {
	if err := o.checkLB(ctx, client, kind, name); err != nil {
		if errors.Is(err, evroc.ErrNotFound) {
			return nil
		}
		return err
	}
	switch kind {
	case "loadBalancer":
		return client.LoadBalancers().Delete(ctx, name)
	case "backendPool":
		return client.BackendPools().Delete(ctx, name)
	case "backendService":
		return client.BackendServices().Delete(ctx, name)
	case "l4Route":
		return client.L4Routes().Delete(ctx, name)
	default:
		return fmt.Errorf("unknown load balancer resource kind %q", kind)
	}
}
