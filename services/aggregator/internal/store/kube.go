package store

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
	"time"
)

const (
	partitionMapName  = "store-partition-map"
	serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// kubeMaps keeps the partition map in a ConfigMap, CAS on metadata.resourceVersion.
// Plain net/http instead of client-go: two verbs do not justify the dependency.
type kubeMaps struct {
	client *http.Client
	url    string // .../namespaces/<ns>/configmaps
	token  string
}

// newMapStore returns the ConfigMap store inside Kubernetes and a memory store elsewhere.
func newMapStore() (mapStore, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return &memoryMaps{}, nil
	}
	token, err := os.ReadFile(serviceAccountDir + "/token")
	if err != nil {
		return nil, fmt.Errorf("read service account token: %w", err)
	}
	namespace, err := os.ReadFile(serviceAccountDir + "/namespace")
	if err != nil {
		return nil, fmt.Errorf("read service account namespace: %w", err)
	}
	ca, err := os.ReadFile(serviceAccountDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("cluster CA is not valid PEM")
	}
	return &kubeMaps{
		client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
		url: "https://" + net.JoinHostPort(host, port) + "/api/v1/namespaces/" +
			strings.TrimSpace(string(namespace)) + "/configmaps",
		token: strings.TrimSpace(string(token)),
	}, nil
}

type configMap struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   configMapMeta     `json:"metadata"`
	Data       map[string]string `json:"data"`
}

type configMapMeta struct {
	Name            string `json:"name"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

func (k *kubeMaps) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+k.token)
	request.Header.Set("Content-Type", "application/json")
	return k.client.Do(request)
}

func (k *kubeMaps) load(ctx context.Context) (partitionMap, string, bool, error) {
	response, err := k.do(ctx, http.MethodGet, k.url+"/"+partitionMapName, nil)
	if err != nil {
		return partitionMap{}, "", false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return partitionMap{}, "", false, nil
	}
	if response.StatusCode != http.StatusOK {
		return partitionMap{}, "", false, fmt.Errorf("get %s: HTTP %d", partitionMapName, response.StatusCode)
	}
	var cm configMap
	if err := json.NewDecoder(response.Body).Decode(&cm); err != nil {
		return partitionMap{}, "", false, err
	}
	var m partitionMap
	if err := json.Unmarshal([]byte(cm.Data["map.json"]), &m); err != nil || len(m.Partitions) != Partitions {
		return partitionMap{}, "", false, fmt.Errorf("%s holds no valid map.json", partitionMapName)
	}
	if m.Incarnations == nil {
		m.Incarnations = map[string]string{}
	}
	return m, cm.Metadata.ResourceVersion, true, nil
}

func (k *kubeMaps) save(ctx context.Context, m partitionMap, version string) (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	cm := configMap{APIVersion: "v1", Kind: "ConfigMap",
		Metadata: configMapMeta{Name: partitionMapName, ResourceVersion: version},
		Data:     map[string]string{"map.json": string(raw)}}
	method, url := http.MethodPut, k.url+"/"+partitionMapName
	if version == "" {
		method, url = http.MethodPost, k.url
	}
	response, err := k.do(ctx, method, url, cm)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusConflict:
		return "", errMapConflict
	default:
		return "", fmt.Errorf("save %s: HTTP %d", partitionMapName, response.StatusCode)
	}
	var saved configMap
	if err := json.NewDecoder(response.Body).Decode(&saved); err != nil {
		return "", err
	}
	return saved.Metadata.ResourceVersion, nil
}
