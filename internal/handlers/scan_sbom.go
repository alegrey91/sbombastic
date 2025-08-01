package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"

	"go.yaml.in/yaml/v3"
	_ "modernc.org/sqlite" // sqlite driver for RPM DB and Java DB

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	trivyCommands "github.com/aquasecurity/trivy/pkg/commands"
	vexrepo "github.com/aquasecurity/trivy/pkg/vex/repo"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/rancher/sbombastic/api"
	storagev1alpha1 "github.com/rancher/sbombastic/api/storage/v1alpha1"
	"github.com/rancher/sbombastic/api/v1alpha1"
)

const (
	// ScanSBOMSubject is the subject for messages that trigger SBOM scanning.
	ScanSBOMSubject = "sbombastic.sbom.scan"
	// trivyVEXSubPath is the directory used by trivy to hold VEX repositories.
	trivyVEXSubPath = ".trivy/vex"
	// trivyVEXRepoFile is the file used by trivy to hold VEX repositories.
	trivyVEXRepoFile = "repository.yaml"
)

// ScanSBOMMessage represents the request message for scanning a SBOM.
type ScanSBOMMessage struct {
	SBOMName      string `json:"sbomName"`
	SBOMNamespace string `json:"sbomNamespace"`
	ScanJobName   string `json:"scanJobName"`
}

// ScanSBOMHandler is responsible for handling SBOM scan requests.
type ScanSBOMHandler struct {
	k8sClient client.Client
	scheme    *runtime.Scheme
	workDir   string
	logger    *slog.Logger
}

// NewScanSBOMHandler creates a new instance of ScanSBOMHandler.
func NewScanSBOMHandler(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	workDir string,
	logger *slog.Logger,
) *ScanSBOMHandler {
	return &ScanSBOMHandler{
		k8sClient: k8sClient,
		scheme:    scheme,
		workDir:   workDir,
		logger:    logger.With("handler", "scan_sbom_handler"),
	}
}

// Handle processes the ScanSBOMMessage and scans the specified SBOM resource for vulnerabilities.
func (h *ScanSBOMHandler) Handle(ctx context.Context, message []byte) error { //nolint:funlen,gocognit
	scanSBOMMessage := &ScanSBOMMessage{}
	if err := json.Unmarshal(message, scanSBOMMessage); err != nil {
		return fmt.Errorf("failed to unmarshal scan job message: %w", err)
	}

	h.logger.DebugContext(ctx, "SBOM scan requested",
		"sbom", scanSBOMMessage.SBOMName,
		"namespace", scanSBOMMessage.SBOMNamespace,
	)

	sbom := &storagev1alpha1.SBOM{}
	err := h.k8sClient.Get(ctx, client.ObjectKey{
		Name:      scanSBOMMessage.SBOMName,
		Namespace: scanSBOMMessage.SBOMNamespace,
	}, sbom)
	if err != nil {
		return fmt.Errorf("failed to get SBOM: %w", err)
	}

	vexHubList := &v1alpha1.VEXHubList{}
	err = h.k8sClient.List(ctx, vexHubList, &client.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list VEXHub: %w", err)
	}

	sbomFile, err := os.CreateTemp(h.workDir, "trivy.sbom.*.json")
	if err != nil {
		return fmt.Errorf("failed to create temporary SBOM file: %w", err)
	}
	defer func() {
		if err = sbomFile.Close(); err != nil {
			h.logger.Error("failed to close temporary SBOM file", "error", err)
		}

		if err = os.Remove(sbomFile.Name()); err != nil {
			h.logger.Error("failed to remove temporary SBOM file", "error", err)
		}
	}()

	_, err = sbomFile.Write(sbom.Spec.SPDX.Raw)
	if err != nil {
		return fmt.Errorf("failed to write SBOM file: %w", err)
	}
	reportFile, err := os.CreateTemp(h.workDir, "trivy.report.*.json")
	if err != nil {
		return fmt.Errorf("failed to create temporary report file: %w", err)
	}
	defer func() {
		if err = reportFile.Close(); err != nil {
			h.logger.Error("failed to close temporary report file", "error", err)
		}

		if err = os.Remove(reportFile.Name()); err != nil {
			h.logger.Error("failed to remove temporary repoort file", "error", err)
		}
	}()

	trivyArgs := []string{
		"sbom",
		"--skip-version-check",
		"--disable-telemetry",
		"--cache-dir", h.workDir,
		"--format", "sarif",
		// Use the public ECR repository to bypass GitHub's rate limits.
		// Refer to https://github.com/orgs/community/discussions/139074 for details.
		"--db-repository", "public.ecr.aws/aquasecurity/trivy-db",
		"--java-db-repository", "public.ecr.aws/aquasecurity/trivy-java-db",
		"--output", reportFile.Name(),
	}
	// Set XDG_DATA_HOME environment variable to /tmp because trivy expects
	// the repository file in that location and there is no way to change it
	// through input flags:
	// https://trivy.dev/v0.64/docs/supply-chain/vex/repo/#default-configuration
	// TODO(alegrey91): fix upstream
	trivyHome, err := os.MkdirTemp("/tmp", "trivy-")
	if err != nil {
		return fmt.Errorf("failed to create temporary trivy home: %w", err)
	}
	err = os.Setenv("XDG_DATA_HOME", trivyHome)
	if err != nil {
		return fmt.Errorf("failed to set XDG_DATA_HOME to %s: %w", trivyHome, err)
	}

	if len(vexHubList.Items) > 0 {
		trivyVEXPath := path.Join(trivyHome, trivyVEXSubPath)
		vexRepoPath := path.Join(trivyVEXPath, trivyVEXRepoFile)
		if err = h.setupVEXHubRepositories(vexHubList, trivyVEXPath, vexRepoPath); err != nil {
			return fmt.Errorf("failed to setup VEX Hub repositories: %w", err)
		}
		// Clean up the trivy home directory after each handler execution to
		// ensure VEX repositories are refreshed on every run.
		defer func() {
			h.logger.Debug("Removing trivy home")
			if err = os.RemoveAll(trivyHome); err != nil {
				h.logger.Error("failed to remove temporary trivy home", "error", err)
			}
		}()

		// We explicitly set the `--vex` option only when needed
		// (VEXHub resources are found). This is because trivy automatically
		// fills the repository file with aquasecurity VEX files, when
		// `--vex` is specificed.
		trivyArgs = append(trivyArgs, "--vex", "repo", "--show-suppressed")
	}

	app := trivyCommands.NewApp()
	// add SBOM file name at the end.
	trivyArgs = append(trivyArgs, sbomFile.Name())
	app.SetArgs(trivyArgs)

	if err = app.ExecuteContext(ctx); err != nil {
		return fmt.Errorf("failed to execute trivy: %w", err)
	}

	h.logger.DebugContext(ctx, "SBOM scanned",
		"sbom", scanSBOMMessage.SBOMName,
		"namespace", scanSBOMMessage.SBOMNamespace,
	)

	reportBytes, err := io.ReadAll(reportFile)
	if err != nil {
		return fmt.Errorf("failed to read SBOM output: %w", err)
	}

	vulnerabilityReport := &storagev1alpha1.VulnerabilityReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sbom.Name,
			Namespace: sbom.Namespace,
		},
	}
	if err = controllerutil.SetControllerReference(sbom, vulnerabilityReport, h.scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	_, err = controllerutil.CreateOrUpdate(ctx, h.k8sClient, vulnerabilityReport, func() error {
		vulnerabilityReport.Labels = map[string]string{
			api.LabelScanJob:      scanSBOMMessage.ScanJobName,
			api.LabelManagedByKey: api.LabelManagedByValue,
			api.LabelPartOfKey:    api.LabelPartOfValue,
		}

		vulnerabilityReport.Spec = storagev1alpha1.VulnerabilityReportSpec{
			ImageMetadata: sbom.GetImageMetadata(),
			SARIF:         runtime.RawExtension{Raw: reportBytes},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create or update vulnerability report: %w", err)
	}

	return nil
}

// setupVEXHubRepositories creates all the necessary files and directories
// to use VEX Hub repositories.
func (h *ScanSBOMHandler) setupVEXHubRepositories(vexHubList *v1alpha1.VEXHubList, trivyVEXPath, vexRepoPath string) error {
	config := vexrepo.Config{}
	var err error
	for _, repo := range vexHubList.Items {
		repo := vexrepo.Repository{
			Name:    repo.Name,
			URL:     repo.Spec.URL,
			Enabled: repo.Spec.Enabled,
		}
		config.Repositories = append(config.Repositories, repo)
	}

	var repositories []byte
	repositories, err = yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal struct: %w", err)
	}

	h.logger.Debug("Creating VEX repository directory", "vexhub", trivyVEXPath)
	err = os.MkdirAll(trivyVEXPath, 0750)
	if err != nil {
		return fmt.Errorf("failed to create VEX configuration directory: %w", err)
	}

	h.logger.Debug("Creating VEX repository file", "vexhub", vexRepoPath)
	err = os.WriteFile(vexRepoPath, repositories, 0600)
	if err != nil {
		return fmt.Errorf("failed to create VEX repository file: %w", err)
	}

	return nil
}
