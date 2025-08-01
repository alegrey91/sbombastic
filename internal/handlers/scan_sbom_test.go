package handlers

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/owenrumney/go-sarif/v2/sarif"
	"github.com/rancher/sbombastic/api"
	storagev1alpha1 "github.com/rancher/sbombastic/api/storage/v1alpha1"
	"github.com/rancher/sbombastic/api/v1alpha1"
	"github.com/rancher/sbombastic/pkg/generated/clientset/versioned/scheme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestScanSBOMHandler_Handle(t *testing.T) {
	vexHubServer := fakeVEXHubRepository(t)
	vexHubServer.Start()
	defer vexHubServer.Close()

	cacheDir := t.TempDir()

	for _, test := range []struct {
		platform           string
		vexHubList         []v1alpha1.VEXHub
		sourceSBOMJSON     string
		expectedReportJSON string
	}{
		{
			platform:           "linux/amd64",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-amd64.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-amd64.sarif.json"),
		},
		{
			platform:           "linux/arm/v6",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm-v6.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm-v6.sarif.json"),
		},
		{
			platform:           "linux/arm/v7",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm-v7.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm-v7.sarif.json"),
		},
		{
			platform:           "linux/arm64/v8",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm64-v8.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm64-v8.sarif.json"),
		},
		{
			platform:           "linux/386",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-386.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-386.sarif.json"),
		},
		{
			platform:           "linux/ppc64le",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-ppc64le.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-ppc64le.sarif.json"),
		},
		{
			platform:           "linux/s390x",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.sarif.json"),
		},
		{
			platform: "linux/s390x with VEX repo enabled",
			vexHubList: []v1alpha1.VEXHub{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: v1alpha1.VEXHubSpec{
						URL:     vexHubServer.URL,
						Enabled: true,
					},
				},
			},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.sarif.json"),
		},
		{
			platform: "linux/s390x with VEX repo not enabled",
			vexHubList: []v1alpha1.VEXHub{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: v1alpha1.VEXHubSpec{
						URL:     vexHubServer.URL,
						Enabled: false,
					},
				},
			},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.sarif.json"),
		},
	} {
		t.Run(test.platform, func(t *testing.T) {
			testScanSBOM(t, cacheDir, test.platform, test.sourceSBOMJSON, test.expectedReportJSON, test.vexHubList)
		})
	}
}

func testScanSBOM(t *testing.T, cacheDir, platform, sourceSBOMJSON, expectedReportJSON string, vexHubList []v1alpha1.VEXHub) {
	spdxData, err := os.ReadFile(sourceSBOMJSON)
	require.NoError(t, err, "failed to read source SBOM file %s", sourceSBOMJSON)

	sbom := &storagev1alpha1.SBOM{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sbom",
			Namespace: "default",
		},
		Spec: storagev1alpha1.SBOMSpec{
			SPDX: runtime.RawExtension{Raw: spdxData},
		},
	}
	vexHubs := &v1alpha1.VEXHubList{
		Items: vexHubList,
	}

	scheme := scheme.Scheme
	err = storagev1alpha1.AddToScheme(scheme)
	require.NoError(t, err)
	err = v1alpha1.AddToScheme(scheme)
	require.NoError(t, err)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(sbom).
		WithRuntimeObjects(vexHubs).
		Build()

	reportData, err := os.ReadFile(expectedReportJSON)
	require.NoError(t, err, "failed to read expected report file %s", expectedReportJSON)

	expectedReport := &sarif.Report{}
	err = json.Unmarshal(reportData, expectedReport)
	require.NoError(t, err, "failed to unmarshal expected report file %s", expectedReportJSON)

	handler := NewScanSBOMHandler(k8sClient, scheme, cacheDir, slog.Default())

	message, err := json.Marshal(&ScanSBOMMessage{
		SBOMName:      sbom.Name,
		SBOMNamespace: sbom.Namespace,
		ScanJobName:   "test-scanjob",
	})
	require.NoError(t, err)

	err = handler.Handle(t.Context(), message)
	require.NoError(t, err, "failed to scan SBOM, with platform %s", platform)

	vulnerabilityReport := &storagev1alpha1.VulnerabilityReport{}
	err = k8sClient.Get(t.Context(), client.ObjectKey{
		Name:      sbom.Name,
		Namespace: sbom.Namespace,
	}, vulnerabilityReport)
	require.NoError(t, err, "failed to get vulnerability report, with platform %s", platform)

	assert.Equal(t, sbom.GetImageMetadata(), vulnerabilityReport.GetImageMetadata())
	assert.Equal(t, sbom.UID, vulnerabilityReport.GetOwnerReferences()[0].UID)
	assert.Equal(t, "test-scanjob", vulnerabilityReport.Labels[api.LabelScanJob])

	report := &sarif.Report{}
	err = json.Unmarshal(vulnerabilityReport.Spec.SARIF.Raw, report)
	require.NoError(t, err, "failed to unmarshal vulnerability report, with platform %s", platform)

	// Filter out fields containing the file path from the comparison
	filter := cmp.FilterPath(func(path cmp.Path) bool {
		lastField := path.Last().String()
		return lastField == ".URI" || lastField == ".Text"
	}, cmp.Comparer(func(a, b *string) bool {
		if strings.Contains(*a, ".json") && strings.Contains(*b, ".json") {
			return true
		}

		return cmp.Equal(a, b)
	}))
	diff := cmp.Diff(expectedReport, report, filter)

	assert.Empty(t, diff, "diff mismatch on platform %s\nDiff:\n%s", platform, diff)
}

func fakeVEXHubRepository(t *testing.T) *httptest.Server {
	handler := http.FileServer(http.Dir("../../test/fixtures/vexhub"))
	server := httptest.NewUnstartedServer(handler)
	listener, err := net.Listen("tcp", ":1337")
	require.NoError(t, err)
	server.Listener = listener
	return server
}
