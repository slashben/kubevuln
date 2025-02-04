package v1

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/armosec/armoapi-go/armotypes"
	cs "github.com/armosec/armoapi-go/containerscan"
	v1 "github.com/armosec/armoapi-go/containerscan/v1"
	"github.com/armosec/armoapi-go/identifiers"
	"github.com/armosec/utils-go/httputils"
	pkgcautils "github.com/armosec/utils-k8s-go/armometadata"
	wlidpkg "github.com/armosec/utils-k8s-go/wlid"
	"github.com/hashicorp/go-multierror"
	backendClientV1 "github.com/kubescape/backend/pkg/client/v1"
	sysreport "github.com/kubescape/backend/pkg/server/v1/systemreports"
	"github.com/kubescape/go-logger"
	"github.com/kubescape/go-logger/helpers"
	"github.com/kubescape/kubevuln/core/domain"
	"github.com/kubescape/kubevuln/core/ports"
	"go.opentelemetry.io/otel"
)

type BackendAdapter struct {
	eventReceiverRestURL string
	apiServerRestURL     string
	clusterConfig        pkgcautils.ClusterConfig
	getCVEExceptionsFunc func(string, string, *identifiers.PortalDesignator) ([]armotypes.VulnerabilityExceptionPolicy, error)
	httpPostFunc         func(httputils.IHttpClient, string, map[string]string, []byte) (*http.Response, error)
	sendStatusFunc       func(*backendClientV1.BaseReportSender, string, bool, chan<- error)
}

var _ ports.Platform = (*BackendAdapter)(nil)

func NewBackendAdapter(accountID, apiServerRestURL, eventReceiverRestURL string) *BackendAdapter {
	return &BackendAdapter{
		clusterConfig: pkgcautils.ClusterConfig{
			AccountID: accountID,
		},
		eventReceiverRestURL: eventReceiverRestURL,
		apiServerRestURL:     apiServerRestURL,
		getCVEExceptionsFunc: backendClientV1.GetCVEExceptionByDesignator,
		httpPostFunc:         httputils.HttpPost,
		sendStatusFunc: func(sender *backendClientV1.BaseReportSender, status string, sendReport bool, errChan chan<- error) {
			sender.SendStatus(status, sendReport, errChan) // TODO - update this function to use from kubescape/backend
		},
	}
}

const ActionName = "vuln scan"
const ReporterName = "ca-vuln-scan"
const maxBodySize int = 30000

var details = []string{
	sysreport.JobStarted,
	sysreport.JobStarted,
	sysreport.JobSuccess,
	sysreport.JobDone,
}
var statuses = []string{
	"Inqueueing",
	"Dequeueing",
	"Dequeueing",
	"Dequeueing",
}

func (a *BackendAdapter) GetCVEExceptions(ctx context.Context) (domain.CVEExceptions, error) {
	ctx, span := otel.Tracer("").Start(ctx, "BackendAdapter.GetCVEExceptions")
	defer span.End()

	// retrieve workload from context
	workload, ok := ctx.Value(domain.WorkloadKey{}).(domain.ScanCommand)
	if !ok {
		return nil, domain.ErrCastingWorkload
	}

	designator := identifiers.PortalDesignator{
		DesignatorType: identifiers.DesignatorAttribute,
		Attributes: map[string]string{
			"customerGUID":        a.clusterConfig.AccountID,
			"scope.cluster":       wlidpkg.GetClusterFromWlid(workload.Wlid),
			"scope.namespace":     wlidpkg.GetNamespaceFromWlid(workload.Wlid),
			"scope.kind":          strings.ToLower(wlidpkg.GetKindFromWlid(workload.Wlid)),
			"scope.name":          wlidpkg.GetNameFromWlid(workload.Wlid),
			"scope.containerName": workload.ContainerName,
		},
	}

	vulnExceptionList, err := a.getCVEExceptionsFunc(a.apiServerRestURL, a.clusterConfig.AccountID, &designator)
	if err != nil {
		return nil, err
	}
	return vulnExceptionList, nil
}

// SendStatus sends the given status and details to the platform
func (a *BackendAdapter) SendStatus(ctx context.Context, step int) error {
	ctx, span := otel.Tracer("").Start(ctx, "BackendAdapter.SendStatus")
	defer span.End()
	// retrieve workload from context
	workload, ok := ctx.Value(domain.WorkloadKey{}).(domain.ScanCommand)
	if !ok {
		return domain.ErrCastingWorkload
	}

	lastAction := workload.LastAction + 1
	report := sysreport.NewBaseReport(
		a.clusterConfig.AccountID,
		ReporterName,
	)
	report.Status = statuses[step]
	report.Target = fmt.Sprintf("vuln scan:: scanning wlid: %v , container: %v imageTag: %v imageHash: %s",
		workload.Wlid, workload.ContainerName, workload.ImageTagNormalized, workload.ImageHash)
	report.ActionID = strconv.Itoa(lastAction)
	report.ActionIDN = lastAction
	report.ActionName = ActionName
	report.JobID = workload.JobID
	report.ParentAction = workload.ParentJobID
	report.Details = details[step]

	ReportErrorsChan := make(chan error)
	sender := backendClientV1.NewBaseReportSender(a.eventReceiverRestURL, &http.Client{}, report)
	a.sendStatusFunc(sender, sysreport.JobSuccess, true, ReportErrorsChan)
	err := <-ReportErrorsChan
	return err
}

// SubmitCVE submits the given CVE to the platform
func (a *BackendAdapter) SubmitCVE(ctx context.Context, cve domain.CVEManifest, cvep domain.CVEManifest) error {
	ctx, span := otel.Tracer("").Start(ctx, "BackendAdapter.SubmitCVE")
	defer span.End()
	// retrieve timestamp from context
	timestamp, ok := ctx.Value(domain.TimestampKey{}).(int64)
	if !ok {
		return domain.ErrMissingTimestamp
	}
	// retrieve scanID from context
	scanID, ok := ctx.Value(domain.ScanIDKey{}).(string)
	if !ok {
		return domain.ErrMissingScanID
	}
	// retrieve workload from context
	workload, ok := ctx.Value(domain.WorkloadKey{}).(domain.ScanCommand)
	if !ok {
		return domain.ErrCastingWorkload
	}

	// validate one more time the scanID before sending it to the platform
	if !armotypes.ValidateContainerScanID(scanID) {
		return domain.ErrInvalidScanID
	}

	// get exceptions
	exceptions, err := a.GetCVEExceptions(ctx)
	if err != nil {
		return err
	}
	// convert to vulnerabilities
	vulnerabilities, err := domainToArmo(ctx, *cve.Content, exceptions)
	if err != nil {
		return err
	}
	// merge cve and cvep
	var hasRelevancy bool
	if cvep.Content != nil {
		hasRelevancy = true
		// convert to relevantVulnerabilities
		relevantVulnerabilities, err := domainToArmo(ctx, *cvep.Content, exceptions)
		if err != nil {
			return err
		}
		// index relevantVulnerabilities
		cvepIndices := map[string]struct{}{}
		for _, v := range relevantVulnerabilities {
			cvepIndices[v.Name] = struct{}{}
		}
		// mark common vulnerabilities as relevant
		for i, v := range vulnerabilities {
			_, isRelevant := cvepIndices[v.Name]
			vulnerabilities[i].IsRelevant = &isRelevant
		}
	}

	finalReport := v1.ScanResultReport{
		Designators:     *identifiers.AttributesDesignatorsFromWLID(workload.Wlid),
		Summary:         nil,
		ContainerScanID: scanID,
		Timestamp:       timestamp,
	}

	// fill designators
	finalReport.Designators.Attributes[identifiers.AttributeContainerName] = workload.ContainerName
	finalReport.Designators.Attributes[identifiers.AttributeWorkloadHash] = cs.GenerateWorkloadHash(finalReport.Designators.Attributes)
	finalReport.Designators.Attributes[identifiers.AttributeCustomerGUID] = a.clusterConfig.AccountID
	if val, ok := workload.Args[identifiers.AttributeRegistryName]; ok {
		finalReport.Designators.Attributes[identifiers.AttributeRegistryName] = val.(string)
	}
	if val, ok := workload.Args[identifiers.AttributeRepository]; ok {
		finalReport.Designators.Attributes[identifiers.AttributeRepository] = val.(string)
	}
	if val, ok := workload.Args[identifiers.AttributeTag]; ok {
		finalReport.Designators.Attributes[identifiers.AttributeTag] = val.(string)
	}
	if val, ok := workload.Args[identifiers.AttributeSensor]; ok {
		finalReport.Designators.Attributes[identifiers.AttributeSensor] = val.(string)
	}

	// fill context and designators into vulnerabilities
	armoContext := identifiers.DesignatorToArmoContext(&finalReport.Designators, "designators")
	for i := range vulnerabilities {
		vulnerabilities[i].Context = armoContext
		vulnerabilities[i].Designators = finalReport.Designators
	}

	// add summary
	finalReport.Summary, vulnerabilities = summarize(finalReport, vulnerabilities, workload, hasRelevancy)
	finalReport.Summary.Context = armoContext

	// split vulnerabilities to chunks
	chunksChan, totalVulnerabilities := httputils.SplitSlice2Chunks(vulnerabilities, maxBodySize, 10)

	// send report(s)
	sendWG := &sync.WaitGroup{}
	errChan := make(chan error, 10)
	// get the first chunk
	firstVulnerabilitiesChunk := <-chunksChan
	firstChunkVulnerabilitiesCount := len(firstVulnerabilitiesChunk)
	// send the summary and the first chunk in one or two reports according to the size
	nextPartNum := a.sendSummaryAndVulnerabilities(ctx, &finalReport, a.eventReceiverRestURL, totalVulnerabilities, scanID, firstVulnerabilitiesChunk, errChan, sendWG)
	// if not all vulnerabilities got into the first chunk
	if totalVulnerabilities != firstChunkVulnerabilitiesCount {
		//send the rest of the vulnerabilities - error channel will be closed when all vulnerabilities are sent
		a.sendVulnerabilitiesRoutine(ctx, chunksChan, a.eventReceiverRestURL, scanID, finalReport, errChan, sendWG, totalVulnerabilities, firstChunkVulnerabilitiesCount, nextPartNum)
	} else {
		//only one chunk will be sent so need to close the error channel when it is done
		go func(wg *sync.WaitGroup, errorChan chan error) {
			//wait for summary post request to end
			wg.Wait()
			//close the error channel
			close(errorChan)
		}(sendWG, errChan)
	}

	// collect post report errors if occurred
	for e := range errChan {
		err = multierror.Append(err, e)
	}
	return err
}

//lint:ignore U1000 Ignore unused function temporarily for debugging
func httpPostDebug(httpClient httputils.IHttpClient, fullURL string, headers map[string]string, body []byte) (*http.Response, error) {
	logger.L().Debug("httpPostDebug", helpers.String("fullURL", fullURL), helpers.Interface("headers", headers), helpers.String("body", string(body)))
	return httputils.HttpPostWithContext(context.Background(), httpClient, fullURL, headers, body)
}
