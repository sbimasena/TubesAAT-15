// Package leader owns the Kubernetes lease; storage remains the fencing authority.
package leader

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const Name = "aggregator-leader"
const leaseDuration = 15 * time.Second
const renewDeadline = 10 * time.Second
const retryPeriod = 2 * time.Second

type lease struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string `json:"name"`
		ResourceVersion string `json:"resourceVersion,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Holder    string `json:"holderIdentity"`
		Epoch     int64  `json:"leaseTransitions"`
		Duration  int    `json:"leaseDurationSeconds"`
		RenewTime string `json:"renewTime"`
	} `json:"spec"`
}

type Leader struct {
	client               *http.Client
	url, token, identity string
	mu                   sync.RWMutex
	epoch                int64
	active               bool
	renewed              time.Time
}

// New uses epoch zero only outside Kubernetes (single-instance Compose).
func New() (*Leader, error) {
	l := &Leader{}
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	if host == "" {
		return l, nil
	}
	const dir = "/var/run/secrets/kubernetes.io/serviceaccount/"
	namespace, err := os.ReadFile(dir + "namespace")
	if err != nil {
		return nil, err
	}
	token, err := os.ReadFile(dir + "token")
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(dir + "ca.crt")
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("leader: invalid cluster CA")
	}
	l.client = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	l.url = "https://" + net.JoinHostPort(host, os.Getenv("KUBERNETES_SERVICE_PORT")) + "/apis/coordination.k8s.io/v1/namespaces/" + strings.TrimSpace(string(namespace)) + "/leases"
	l.token = strings.TrimSpace(string(token))
	l.identity = os.Getenv("POD_NAME") + "_" + uuid.NewString()
	return l, nil
}

func (l *Leader) Current() (int64, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.epoch, l.active
}
func (l *Leader) set(epoch int64, active bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.epoch, l.active, l.renewed = epoch, active, time.Now()
}

func (l *Leader) request(ctx context.Context, method string, body *lease) (lease, int, error) {
	url := l.url + "/" + Name
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return lease{}, 0, err
		}
		payload = bytes.NewReader(raw)
	}
	if method == http.MethodPost {
		url = l.url
	}
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return lease{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+l.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := l.client.Do(req)
	if err != nil {
		return lease{}, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 201 {
		return lease{}, response.StatusCode, fmt.Errorf("lease API HTTP %d", response.StatusCode)
	}
	var value lease
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value)
	return value, response.StatusCode, err
}

// Verify re-reads the API server before each upstream request and event write.
func (l *Leader) Verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.RLock()
	epoch, active, renewed := l.epoch, l.active, l.renewed
	l.mu.RUnlock()
	if !active {
		return errors.New("leader: no active term")
	}
	if l.client == nil {
		return nil
	}
	if time.Since(renewed) >= renewDeadline {
		return errors.New("leader: renew deadline elapsed")
	}
	value, _, err := l.request(ctx, http.MethodGet, nil)
	if err != nil {
		return err
	}
	if value.Spec.Holder != l.identity || value.Spec.Epoch != epoch {
		return errors.New("leader: lease holder or epoch changed")
	}
	if time.Since(renewed) >= renewDeadline {
		return errors.New("leader: renew deadline elapsed during verification")
	}
	return ctx.Err()
}

// Run blocks through worker cancellation. Callbacks fence storage before starting
// workers; loss of a term stops the process so every restart gets a fresh identity.
func (l *Leader) Run(ctx context.Context, onStart func(context.Context, int64), onStop func()) error {
	if l.client == nil {
		l.set(0, true)
		defer l.set(0, false)
		onStart(ctx, 0)
		return nil
	}
	ticker := time.NewTicker(retryPeriod)
	defer ticker.Stop()
	var observedVersion string
	var observedAt time.Time
	var won lease
	for {
		value, status, err := l.request(ctx, http.MethodGet, nil)
		if err == nil || status == http.StatusNotFound {
			if value.Metadata.ResourceVersion != observedVersion || observedAt.IsZero() {
				observedVersion = value.Metadata.ResourceVersion
				observedAt = time.Now()
			}
			// Expiration uses elapsed time since observing a record, never timestamps
			// written by a different node. resourceVersion CAS decides the winner.
			if status == http.StatusNotFound || time.Since(observedAt) >= leaseDuration {
				if value.Spec.Epoch >= 2147483647 {
					return errors.New("leader: lease epoch exhausted")
				}
				value.APIVersion, value.Kind = "coordination.k8s.io/v1", "Lease"
				value.Metadata.Name = Name
				value.Spec.Holder = l.identity
				value.Spec.Epoch++
				value.Spec.Duration = int(leaseDuration / time.Second)
				value.Spec.RenewTime = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
				method := http.MethodPut
				if status == http.StatusNotFound {
					method = http.MethodPost
				}
				if saved, _, err := l.request(ctx, method, &value); err == nil {
					won = saved
					break
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	term, cancel := context.WithCancel(ctx)
	l.set(won.Spec.Epoch, true)
	done := make(chan struct{})
	go func() { defer close(done); onStart(term, won.Spec.Epoch) }()
	defer func() { cancel(); l.set(won.Spec.Epoch, false); onStop(); <-done }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return errors.New("leader: workers exited")
		case <-ticker.C:
			if err := l.Verify(ctx); err == nil {
				value, _, err := l.request(ctx, http.MethodGet, nil)
				if err == nil && value.Spec.Holder == l.identity && value.Spec.Epoch == won.Spec.Epoch {
					value.Spec.RenewTime = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
					if _, _, err = l.request(ctx, http.MethodPut, &value); err == nil {
						l.set(won.Spec.Epoch, true)
						continue
					}
				}
			}
			l.mu.RLock()
			elapsed := time.Since(l.renewed)
			l.mu.RUnlock()
			if elapsed >= renewDeadline {
				return errors.New("leader: renewal lost")
			}
		}
	}
}
