package migration

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ocv1 "github.com/operator-framework/operator-controller/api/v1"
)

// catalogMeta represents a single entry from the catalog JSONL response.
type catalogMeta struct {
	Schema         string          `json:"schema"`
	Name           string          `json:"name"`
	Package        string          `json:"package"`
	DefaultChannel string          `json:"defaultChannel,omitempty"`
	Props          json.RawMessage `json:"properties,omitempty"`
	Entries        []channelEntry  `json:"entries,omitempty"`
}

type channelEntry struct {
	Name string `json:"name"`
}

// CatalogPackageInfo holds the results of querying a catalog for a package.
type CatalogPackageInfo struct {
	Found             bool
	DefaultChannel    string // the package's declared defaultChannel from the FBC
	AvailableVersions []string
	AvailableChannels []string
	VersionFound      bool
	ChannelFound      bool
}

// QueryCatalogForPackage queries a ClusterCatalog's content to check if the
// specified package, version, and channel are available.
func (m *Migrator) QueryCatalogForPackage(ctx context.Context, catalog *ocv1.ClusterCatalog, packageName, version, channel string, restConfig *rest.Config) (*CatalogPackageInfo, error) {
	if catalog.Status.URLs == nil {
		return nil, fmt.Errorf("catalog %s has no URLs in status", catalog.Name)
	}

	endpoint, stop, inClusterConfig, err := catalogEndpoint(ctx, catalog, restConfig)
	if err != nil {
		return nil, err
	}
	defer stop()
	transport, err := catalogHTTPTransport(inClusterConfig)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog returned status %d", resp.StatusCode)
	}

	return parseCatalogResponse(resp.Body, packageName, version, channel)
}

// FBC schema type constants for parsing catalog JSONL responses.
const (
	fbcSchemaPackage = "olm.package"
	fbcSchemaBundle  = "olm.bundle"
	fbcSchemaChannel = "olm.channel"
)

func catalogHTTPTransport(inClusterConfig *rest.Config) (http.RoundTripper, error) {
	if inClusterConfig != nil {
		transport, err := rest.TransportFor(inClusterConfig)
		if err != nil {
			return nil, fmt.Errorf("create in-cluster catalog transport: %w", err)
		}
		return transport, nil
	}
	return &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}, nil // #nosec G402 -- the port-forward endpoint is bound to loopback.
}

func catalogEndpoint(ctx context.Context, catalog *ocv1.ClusterCatalog, config *rest.Config) (string, func(), *rest.Config, error) {
	if inClusterConfig, err := rest.InClusterConfig(); err == nil {
		return catalog.Status.URLs.Base + "/api/v1/all", func() {}, inClusterConfig, nil
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", nil, nil, fmt.Errorf("create Kubernetes client for catalog port-forward: %w", err)
	}
	pods, err := clientset.CoreV1().Pods("olmv1-system").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=catalogd"})
	if err != nil {
		return "", nil, nil, fmt.Errorf("list catalogd pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return "", nil, nil, fmt.Errorf("no catalogd pods found")
	}
	// catalogd serves content only from its leader's cache. The Service can route
	// to a ready follower that has no cache and returns 404, so use the leader
	// recorded by catalogd's controller-runtime Lease when accessing from outside
	// the cluster via a pod port-forward.
	podName := pods.Items[0].Name
	if lease, leaseErr := clientset.CoordinationV1().Leases("olmv1-system").Get(ctx, "catalogd-operator-lock", metav1.GetOptions{}); leaseErr == nil && lease.Spec.HolderIdentity != nil {
		leaderName := strings.SplitN(*lease.Spec.HolderIdentity, "_", 2)[0]
		for _, pod := range pods.Items {
			if pod.Name == leaderName {
				podName = pod.Name
				break
			}
		}
	}
	u, err := url.Parse(config.Host)
	if err != nil {
		return "", nil, nil, err
	}
	u.Path = path.Join(u.Path, "api", "v1", "namespaces", "olmv1-system", "pods", podName, "portforward")
	rt, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return "", nil, nil, fmt.Errorf("create catalogd port-forward: %w", err)
	}
	stop, ready := make(chan struct{}), make(chan struct{})
	fw, err := portforward.NewOnAddresses(spdy.NewDialer(upgrader, &http.Client{Transport: rt}, http.MethodPost, u), []string{"127.0.0.1"}, []string{"0:8443"}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return "", nil, nil, err
	}
	forwardErr := make(chan error, 1)
	go func() { forwardErr <- fw.ForwardPorts() }()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case <-ready:
	case err := <-forwardErr:
		close(stop)
		if err != nil {
			return "", nil, nil, fmt.Errorf("start catalogd port-forward: %w", err)
		}
		return "", nil, nil, fmt.Errorf("catalogd port-forward stopped before becoming ready")
	case <-waitCtx.Done():
		close(stop)
		return "", nil, nil, fmt.Errorf("wait for catalogd port-forward: %w", waitCtx.Err())
	}
	ports, err := fw.GetPorts()
	if err != nil {
		close(stop)
		return "", nil, nil, err
	}
	return fmt.Sprintf("https://127.0.0.1:%d/catalogs/%s/api/v1/all", ports[0].Local, catalog.Name), func() { close(stop) }, nil, nil
}

func parseCatalogResponse(body io.Reader, packageName, version, channel string) (*CatalogPackageInfo, error) {
	info := &CatalogPackageInfo{}
	versionSet := map[string]bool{}
	channelSet := map[string]bool{}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		var meta catalogMeta
		if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
			continue
		}

		switch meta.Schema {
		case fbcSchemaPackage:
			if meta.Name == packageName {
				info.Found = true
				if meta.DefaultChannel != "" {
					info.DefaultChannel = meta.DefaultChannel
				}
			}
		case fbcSchemaBundle:
			if meta.Package != packageName {
				continue
			}
			bundleVersion := extractBundleVersion(meta.Props)
			if bundleVersion != "" {
				versionSet[bundleVersion] = true
			}
		case fbcSchemaChannel:
			if meta.Package != packageName {
				continue
			}
			channelSet[meta.Name] = true
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading catalog response: %w", err)
	}

	for v := range versionSet {
		info.AvailableVersions = append(info.AvailableVersions, v)
	}
	for ch := range channelSet {
		info.AvailableChannels = append(info.AvailableChannels, ch)
	}

	info.VersionFound = versionSet[version]
	info.ChannelFound = channel == "" || channelSet[channel]

	return info, nil
}

func extractBundleVersion(propsRaw json.RawMessage) string {
	if propsRaw == nil {
		return ""
	}
	var props []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(propsRaw, &props); err != nil {
		return ""
	}
	for _, p := range props {
		if p.Type == "olm.package" {
			var pkg struct {
				Version string `json:"version"`
			}
			if err := json.Unmarshal(p.Value, &pkg); err == nil {
				return pkg.Version
			}
		}
	}
	return ""
}

// ResolveClusterCatalog finds a ClusterCatalog that serves the package at the installed version.
func (m *Migrator) ResolveClusterCatalog(ctx context.Context, info *MigrationInfo, restConfig *rest.Config) (string, error) {
	var catalogList ocv1.ClusterCatalogList
	if err := m.Client.List(ctx, &catalogList); err != nil {
		return "", fmt.Errorf("failed to list ClusterCatalogs: %w", err)
	}

	type catalogCandidate struct {
		name     string
		priority int32
		pkgInfo  *CatalogPackageInfo
	}
	var candidates []catalogCandidate
	var queriedCatalogs []string

	for i := range catalogList.Items {
		catalog := &catalogList.Items[i]

		if catalog.Spec.AvailabilityMode == ocv1.AvailabilityModeUnavailable {
			continue
		}

		serving := false
		for _, c := range catalog.Status.Conditions {
			if c.Type == "Serving" && c.Status == metav1.ConditionTrue {
				serving = true
				break
			}
		}
		if !serving {
			continue
		}

		queriedCatalogs = append(queriedCatalogs, catalog.Name)
		m.progress(fmt.Sprintf("Querying catalog %s for package %s@%s...", catalog.Name, info.PackageName, info.Version))

		pkgInfo, err := m.QueryCatalogForPackage(ctx, catalog, info.PackageName, info.Version, info.Channel, restConfig)
		if err != nil {
			m.progress(fmt.Sprintf("Could not query catalog %s: %v", catalog.Name, err))
			continue
		}

		if pkgInfo.Found && pkgInfo.VersionFound && pkgInfo.ChannelFound {
			candidates = append(candidates, catalogCandidate{
				name:     catalog.Name,
				priority: catalog.Spec.Priority,
				pkgInfo:  pkgInfo,
			})
		}
	}

	if len(candidates) == 0 {
		return "", &PackageNotFoundError{
			PackageName:     info.PackageName,
			Version:         info.Version,
			Channel:         info.Channel,
			QueriedCatalogs: queriedCatalogs,
		}
	}

	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.priority > best.priority {
			best = c
		}
	}

	return best.name, nil
}

// PackageNotFoundError is returned when no ClusterCatalog contains the required package.
type PackageNotFoundError struct {
	PackageName     string
	Version         string
	Channel         string
	QueriedCatalogs []string
}

func (e *PackageNotFoundError) Error() string {
	msg := fmt.Sprintf("package %q at version %q", e.PackageName, e.Version)
	if e.Channel != "" {
		msg += fmt.Sprintf(" in channel %q", e.Channel)
	}
	msg += " not found in any serving ClusterCatalog"
	if len(e.QueriedCatalogs) > 0 {
		msg += fmt.Sprintf(" (queried: %v)", e.QueriedCatalogs)
	}
	return msg
}

// CreateClusterCatalog creates a ClusterCatalog from a CatalogSource image reference
// and waits for it to reach a serving state.
func (m *Migrator) CreateClusterCatalog(ctx context.Context, name, imageRef string) error {
	catalog := &ocv1.ClusterCatalog{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: ocv1.ClusterCatalogSpec{
			Source: ocv1.CatalogSource{
				Type: ocv1.SourceTypeImage,
				Image: &ocv1.ImageSource{
					Ref: imageRef,
				},
			},
		},
	}

	if err := m.Client.Create(ctx, catalog); err != nil {
		return fmt.Errorf("failed to create ClusterCatalog: %w", err)
	}

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		var cat ocv1.ClusterCatalog
		if err := m.Client.Get(ctx, client.ObjectKeyFromObject(catalog), &cat); err != nil {
			return false, err
		}
		for _, c := range cat.Status.Conditions {
			if c.Type == "Serving" && c.Status == metav1.ConditionTrue {
				return true, nil
			}
		}
		m.progress(fmt.Sprintf("Waiting for ClusterCatalog %s to become ready...", name))
		return false, nil
	})
}
