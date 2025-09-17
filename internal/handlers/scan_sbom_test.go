package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	storagev1alpha1 "github.com/rancher/sbombastic/api/storage/v1alpha1"
	"github.com/rancher/sbombastic/api/v1alpha1"
	"github.com/rancher/sbombastic/pkg/generated/clientset/versioned/scheme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	_ "modernc.org/sqlite"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testTrivyDBRepository     = "ghcr.io/rancher-sandbox/sbombastic/test-assets/trivy-db:2"
	testTrivyJavaDBRepository = "ghcr.io/rancher-sandbox/sbombastic/test-assets/trivy-java-db:2"
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
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-amd64.sbombastic.json"),
		},
		{
			platform:           "linux/arm/v6",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm-v6.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm-v6.sbombastic.json"),
		},
		{
			platform:           "linux/arm/v7",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm-v7.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm-v7.sbombastic.json"),
		},
		{
			platform:           "linux/arm64/v8",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm64-v8.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-arm64-v8.sbombastic.json"),
		},
		{
			platform:           "linux/386",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-386.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-386.sbombastic.json"),
		},
		{
			platform:           "linux/ppc64le",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-ppc64le.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-ppc64le.sbombastic.json"),
		},
		{
			platform:           "linux/s390x",
			vexHubList:         []v1alpha1.VEXHub{},
			sourceSBOMJSON:     filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.spdx.json"),
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.sbombastic.json"),
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
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.sbombastic.json"),
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
			expectedReportJSON: filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-s390x.sbombastic.json"),
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

	scanJob := &v1alpha1.ScanJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-scanjob",
			Namespace: "default",
			UID:       "test-scanjob-uid",
		},
		Spec: v1alpha1.ScanJobSpec{
			Registry: "test-registry",
		},
	}

	sbom := &storagev1alpha1.SBOM{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sbom",
			Namespace: "default",
		},
		SPDX: runtime.RawExtension{Raw: spdxData},
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
		WithRuntimeObjects(scanJob).
		WithRuntimeObjects(sbom).
		WithRuntimeObjects(vexHubs).
		Build()

	reportData, err := os.ReadFile(expectedReportJSON)
	require.NoError(t, err, "failed to read expected report file %s", expectedReportJSON)

	expectedReport := &storagev1alpha1.Report{}
	err = json.Unmarshal(reportData, expectedReport)
	require.NoError(t, err, "failed to unmarshal expected report file %s", expectedReportJSON)

	handler := NewScanSBOMHandler(k8sClient, scheme, cacheDir, testTrivyDBRepository, testTrivyJavaDBRepository, slog.Default())

	message, err := json.Marshal(&ScanSBOMMessage{
		BaseMessage: BaseMessage{
			ScanJob: ObjectRef{
				Name:      "test-scanjob",
				Namespace: "default",
			},
		},
		SBOM: ObjectRef{
			Name:      sbom.Name,
			Namespace: sbom.Namespace,
		},
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
	assert.Equal(t, string(scanJob.UID), vulnerabilityReport.Labels[v1alpha1.LabelScanJobUIDKey])

	report := &vulnerabilityReport.Report
	require.NotEmpty(t, report)

	// override report field since trivy uses the sbom name as Target,
	// which changes at every test run.
	report.Results[0].Target = expectedReport.Results[0].Target
	assert.Equal(t, expectedReport, report)
}

func fakeVEXHubRepository(t *testing.T) *httptest.Server {
	handler := http.FileServer(http.Dir("../../test/fixtures/vexhub"))
	server := httptest.NewUnstartedServer(handler)
	listener, err := net.Listen("tcp", ":1337")
	require.NoError(t, err)
	server.Listener = listener
	return server
}

func TestScanSBOMHandler_Handle_StopProcessing(t *testing.T) {
	spdxData, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "golang-1.12-alpine-amd64.spdx.json"))
	require.NoError(t, err)

	scanJob := &v1alpha1.ScanJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-scanjob",
			Namespace: "default",
			UID:       "test-scanjob-uid",
		},
		Spec: v1alpha1.ScanJobSpec{
			Registry: "test-registry",
		},
	}

	sbom := &storagev1alpha1.SBOM{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sbom",
			Namespace: "default",
		},
		SPDX: runtime.RawExtension{Raw: spdxData},
	}

	vexHubs := &v1alpha1.VEXHubList{
		Items: []v1alpha1.VEXHub{},
	}

	tests := []struct {
		name            string
		existingObjects []runtime.Object
	}{
		{
			name:            "scanjob not found",
			existingObjects: []runtime.Object{sbom, vexHubs},
		},
		{
			name:            "sbom not found",
			existingObjects: []runtime.Object{scanJob, vexHubs},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scheme := scheme.Scheme
			err := storagev1alpha1.AddToScheme(scheme)
			require.NoError(t, err)
			err = v1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(test.existingObjects...).
				Build()

			cacheDir := t.TempDir()
			handler := NewScanSBOMHandler(k8sClient, scheme, cacheDir, testTrivyDBRepository, testTrivyJavaDBRepository, slog.Default())

			message, err := json.Marshal(&ScanSBOMMessage{
				BaseMessage: BaseMessage{
					ScanJob: ObjectRef{
						Name:      scanJob.Name,
						Namespace: "default",
					},
				},
				SBOM: ObjectRef{
					Name:      sbom.Name,
					Namespace: "default",
				},
			})
			require.NoError(t, err)

			// Should return nil (no error) when resource doesn't exist
			err = handler.Handle(context.Background(), message)
			require.NoError(t, err)

			// Verify no VulnerabilityReport was created
			vulnerabilityReport := &storagev1alpha1.VulnerabilityReport{}
			err = k8sClient.Get(context.Background(), types.NamespacedName{
				Name:      sbom.Name,
				Namespace: "default",
			}, vulnerabilityReport)
			assert.True(t, apierrors.IsNotFound(err), "VulnerabilityReport should not exist")
		})
	}
}
