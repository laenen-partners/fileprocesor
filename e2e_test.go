package fileprocesor_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	fpv1 "github.com/laenen-partners/fileprocesor/gen/fileprocessor/v1"
	"github.com/laenen-partners/fileprocesor/gen/fileprocessor/v1/fileprocessorv1connect"

	fileprocesor "github.com/laenen-partners/fileprocesor"
	"github.com/laenen-partners/objectstore"
	"github.com/laenen-partners/objectstore/tokenstore"

	objv1 "github.com/laenen-partners/objectstore/gen/objectstore/v1"
	"github.com/laenen-partners/objectstore/gen/objectstore/v1/objectstorev1connect"
)

const testBucket = "e2e-test"

// startE2E boots both an ObjectStore and FileProcessor backed by real
// docker-compose services (Postgres, Gotenberg, Docling, pdf2img).
// ClamAV is skipped (CLAMAV_ADDRESS left empty).
// Requires: task infra:up
func startE2E(t *testing.T) (
	fileprocessorv1connect.FileProcessorServiceClient,
	objectstorev1connect.ObjectStoreServiceClient,
	*httptest.Server,
) {
	t.Helper()

	dbURL := envOrSkip(t, "DBOS_DATABASE_URL")
	gotenbergURL := envOrSkip(t, "GOTENBERG_URL")
	doclingURL := envOrSkip(t, "DOCLING_URL")
	pdf2imgURL := envOrSkip(t, "PDF2IMG_URL")

	// --- ObjectStore (local filesystem) ---
	dir := t.TempDir()
	objCfg := objectstore.Config{
		Backend:        "file",
		BasePath:       dir,
		BaseURL:        "PLACEHOLDER",
		TokenValidator: newTestTokenValidator("test-secret"),
	}

	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	objCfg.BaseURL = ts.URL
	objHandler, store, err := objectstore.New(objCfg)
	if err != nil {
		t.Fatalf("objectstore.New: %v", err)
	}

	// --- FileProcessor ---
	fpCfg := fileprocesor.Config{
		DatabaseURL:  dbURL,
		GotenbergURL: gotenbergURL,
		DoclingURL:   doclingURL,
		PDF2ImgURL:   pdf2imgURL,
		ClamAVAddr:   "", // AV disabled
	}

	fpHandler, closer, err := fileprocesor.New(fpCfg, store)
	if err != nil {
		t.Fatalf("fileprocesor.New: %v", err)
	}
	t.Cleanup(closer)

	mux.Handle("/", objHandler)
	mux.Handle("/fileprocessor.v1.FileProcessorService/", fpHandler)

	objClient := objectstorev1connect.NewObjectStoreServiceClient(ts.Client(), ts.URL)
	fpClient := fileprocessorv1connect.NewFileProcessorServiceClient(ts.Client(), ts.URL)

	// Ensure test bucket exists.
	_, err = objClient.EnsureBucket(context.Background(), connect.NewRequest(&objv1.EnsureBucketRequest{
		Bucket: testBucket,
	}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	return fpClient, objClient, ts
}

func envOrSkip(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("skipping: %s not set (run task infra:up and source .env)", key)
	}
	return v
}

// uploadTestFile uploads a file via presigned URL and returns the key.
func uploadTestFile(t *testing.T, objClient objectstorev1connect.ObjectStoreServiceClient, ts *httptest.Server, key, contentType string, data []byte) {
	t.Helper()
	ctx := context.Background()

	putResp, err := objClient.PresignPut(ctx, connect.NewRequest(&objv1.PresignPutRequest{
		Bucket:      testBucket,
		Key:         key,
		ContentType: contentType,
	}))
	if err != nil {
		t.Fatalf("PresignPut %s: %v", key, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("create PUT request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", key, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s status = %d, want 200", key, resp.StatusCode)
	}
}

// downloadFile downloads a file via presigned URL.
func downloadFile(t *testing.T, objClient objectstorev1connect.ObjectStoreServiceClient, ts *httptest.Server, bucket, key string) []byte {
	t.Helper()
	ctx := context.Background()

	getResp, err := objClient.PresignGet(ctx, connect.NewRequest(&objv1.PresignGetRequest{
		Bucket: bucket,
		Key:    key,
	}))
	if err != nil {
		t.Fatalf("PresignGet %s/%s: %v", bucket, key, err)
	}

	resp, err := ts.Client().Get(getResp.Msg.Url)
	if err != nil {
		t.Fatalf("GET %s/%s: %v", bucket, key, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return data
}

// samplePDF returns a minimal valid PDF.
func samplePDF() []byte {
	return []byte(`%PDF-1.0
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/MediaBox[0 0 612 792]/Parent 2 0 R>>endobj
xref
0 4
0000000000 65535 f
0000000009 00000 n
0000000058 00000 n
0000000115 00000 n
trailer<</Size 4/Root 1 0 R>>
startxref
190
%%EOF`)
}

func TestE2E_ScanFile_Disabled(t *testing.T) {
	fpClient, objClient, ts := startE2E(t)
	ctx := context.Background()

	uploadTestFile(t, objClient, ts, "scan-test.txt", "text/plain", []byte("hello world"))

	resp, err := fpClient.ScanFile(ctx, connect.NewRequest(&fpv1.ScanFileRequest{
		Bucket: testBucket,
		Key:    "scan-test.txt",
	}))
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if !resp.Msg.Clean {
		t.Errorf("ScanFile.Clean = false, want true (scanning disabled)")
	}
	if resp.Msg.Detail != "scanning disabled" {
		t.Errorf("ScanFile.Detail = %q, want %q", resp.Msg.Detail, "scanning disabled")
	}
}

func TestE2E_ConvertToPDF(t *testing.T) {
	fpClient, objClient, ts := startE2E(t)
	ctx := context.Background()

	// Upload a text file to convert.
	uploadTestFile(t, objClient, ts, "doc.txt", "text/plain", []byte("Hello, Gotenberg!"))

	resp, err := fpClient.ConvertToPDF(ctx, connect.NewRequest(&fpv1.ConvertToPDFRequest{
		Bucket:      testBucket,
		Key:         "doc.txt",
		ContentType: "text/plain",
		Destination: &fpv1.FileRef{
			Bucket:      testBucket,
			Key:         "doc.pdf",
			ContentType: "application/pdf",
		},
	}))
	if err != nil {
		t.Fatalf("ConvertToPDF: %v", err)
	}
	if resp.Msg.SizeBytes <= 0 {
		t.Errorf("ConvertToPDF.SizeBytes = %d, want > 0", resp.Msg.SizeBytes)
	}
	if resp.Msg.Result == nil || resp.Msg.Result.Key != "doc.pdf" {
		t.Errorf("ConvertToPDF.Result.Key = %v, want doc.pdf", resp.Msg.Result)
	}

	// Verify the PDF was actually stored.
	pdfData := downloadFile(t, objClient, ts, testBucket, "doc.pdf")
	if !bytes.HasPrefix(pdfData, []byte("%PDF")) {
		t.Errorf("downloaded file is not a PDF, starts with %q", string(pdfData[:min(20, len(pdfData))]))
	}
}

func TestE2E_MergePDFs(t *testing.T) {
	fpClient, objClient, ts := startE2E(t)
	ctx := context.Background()

	// Upload two text files and convert them to PDF first.
	for _, name := range []string{"a.txt", "b.txt"} {
		uploadTestFile(t, objClient, ts, name, "text/plain", []byte("Content of "+name))
		_, err := fpClient.ConvertToPDF(ctx, connect.NewRequest(&fpv1.ConvertToPDFRequest{
			Bucket:      testBucket,
			Key:         name,
			ContentType: "text/plain",
			Destination: &fpv1.FileRef{
				Bucket:      testBucket,
				Key:         name + ".pdf",
				ContentType: "application/pdf",
			},
		}))
		if err != nil {
			t.Fatalf("ConvertToPDF %s: %v", name, err)
		}
	}

	// Merge the two PDFs.
	resp, err := fpClient.MergePDFs(ctx, connect.NewRequest(&fpv1.MergePDFsRequest{
		Files: []*fpv1.FileRef{
			{Bucket: testBucket, Key: "a.txt.pdf"},
			{Bucket: testBucket, Key: "b.txt.pdf"},
		},
		Destination: &fpv1.FileRef{
			Bucket:      testBucket,
			Key:         "merged.pdf",
			ContentType: "application/pdf",
		},
	}))
	if err != nil {
		t.Fatalf("MergePDFs: %v", err)
	}
	if resp.Msg.SizeBytes <= 0 {
		t.Errorf("MergePDFs.SizeBytes = %d, want > 0", resp.Msg.SizeBytes)
	}

	merged := downloadFile(t, objClient, ts, testBucket, "merged.pdf")
	if !bytes.HasPrefix(merged, []byte("%PDF")) {
		t.Errorf("merged file is not a PDF")
	}
}

func TestE2E_GenerateThumbnail(t *testing.T) {
	fpClient, objClient, ts := startE2E(t)
	ctx := context.Background()

	// Upload a text file and convert to PDF first (pdf2img needs a real PDF).
	uploadTestFile(t, objClient, ts, "thumb-src.txt", "text/plain", []byte("Thumbnail test"))
	_, err := fpClient.ConvertToPDF(ctx, connect.NewRequest(&fpv1.ConvertToPDFRequest{
		Bucket:      testBucket,
		Key:         "thumb-src.txt",
		ContentType: "text/plain",
		Destination: &fpv1.FileRef{
			Bucket:      testBucket,
			Key:         "thumb-src.pdf",
			ContentType: "application/pdf",
		},
	}))
	if err != nil {
		t.Fatalf("ConvertToPDF: %v", err)
	}

	resp, err := fpClient.GenerateThumbnail(ctx, connect.NewRequest(&fpv1.GenerateThumbnailRequest{
		Bucket: testBucket,
		Key:    "thumb-src.pdf",
		Destination: &fpv1.FileRef{
			Bucket:      testBucket,
			Key:         "thumb.jpg",
			ContentType: "image/jpeg",
		},
	}))
	if err != nil {
		t.Fatalf("GenerateThumbnail: %v", err)
	}
	if resp.Msg.SizeBytes <= 0 {
		t.Errorf("GenerateThumbnail.SizeBytes = %d, want > 0", resp.Msg.SizeBytes)
	}

	thumbData := downloadFile(t, objClient, ts, testBucket, "thumb.jpg")
	if len(thumbData) == 0 {
		t.Error("thumbnail file is empty")
	}
}

func TestE2E_ExtractMarkdown(t *testing.T) {
	fpClient, objClient, ts := startE2E(t)
	ctx := context.Background()

	// Upload a text file and convert to PDF (Docling works best with PDFs).
	uploadTestFile(t, objClient, ts, "extract-src.txt", "text/plain", []byte("Extract markdown test content"))
	_, err := fpClient.ConvertToPDF(ctx, connect.NewRequest(&fpv1.ConvertToPDFRequest{
		Bucket:      testBucket,
		Key:         "extract-src.txt",
		ContentType: "text/plain",
		Destination: &fpv1.FileRef{
			Bucket:      testBucket,
			Key:         "extract-src.pdf",
			ContentType: "application/pdf",
		},
	}))
	if err != nil {
		t.Fatalf("ConvertToPDF: %v", err)
	}

	resp, err := fpClient.ExtractMarkdown(ctx, connect.NewRequest(&fpv1.ExtractMarkdownRequest{
		Bucket: testBucket,
		Key:    "extract-src.pdf",
		DoclingJsonDestination: &fpv1.FileRef{
			Bucket:      testBucket,
			Key:         "extract.json",
			ContentType: "application/json",
		},
	}))
	if err != nil {
		t.Fatalf("ExtractMarkdown: %v", err)
	}
	if resp.Msg.Markdown == "" {
		t.Error("ExtractMarkdown.Markdown is empty")
	}

	// Verify docling JSON was stored.
	jsonData := downloadFile(t, objClient, ts, testBucket, "extract.json")
	if len(jsonData) == 0 {
		t.Error("docling JSON file is empty")
	}
}

// testTokenValidator is a minimal HMAC-based token validator for e2e tests.
type testTokenValidator struct {
	secret []byte
}

func newTestTokenValidator(secret string) *testTokenValidator {
	return &testTokenValidator{secret: []byte(secret)}
}

func (v *testTokenValidator) Issue(_ context.Context, req tokenstore.IssueRequest) (*tokenstore.Token, error) {
	expires := time.Now().Add(time.Hour).Unix()
	msg := fmt.Sprintf("%s:%s:%s:%d", req.Method, req.Bucket, req.Key, expires)
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(msg))
	tok := hex.EncodeToString(mac.Sum(nil))
	return &tokenstore.Token{
		Token:     tok,
		ExpiresAt: expires,
	}, nil
}

func (v *testTokenValidator) Validate(_ context.Context, method, bucket, key string, expiresAt int64, token string) (*tokenstore.Claims, error) {
	if expiresAt < time.Now().Unix() {
		return nil, fmt.Errorf("token expired")
	}
	msg := fmt.Sprintf("%s:%s:%s:%d", method, bucket, key, expiresAt)
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(msg))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(token), []byte(expected)) {
		return nil, fmt.Errorf("invalid token")
	}
	return &tokenstore.Claims{}, nil
}

func (v *testTokenValidator) Revoke(_ context.Context, _ string) error {
	return nil
}

func TestE2E_ProcessWorkflow(t *testing.T) {
	fpClient, objClient, ts := startE2E(t)
	ctx := context.Background()

	// Process requires ENTITY_STORE_URL for job tracking.
	if os.Getenv("ENTITY_STORE_URL") == "" {
		t.Skip("skipping: ENTITY_STORE_URL not set (Process requires job tracking)")
	}

	// Upload a text file.
	uploadTestFile(t, objClient, ts, "workflow-doc.txt", "text/plain", []byte("Workflow end-to-end test"))

	resp, err := fpClient.Process(ctx, connect.NewRequest(&fpv1.ProcessRequest{
		Inputs: []*fpv1.FileInput{
			{
				Name:        "doc",
				Bucket:      testBucket,
				Key:         "workflow-doc.txt",
				ContentType: "text/plain",
			},
		},
		Operations: []*fpv1.Operation{
			{
				Name:   "convert",
				Inputs: []string{"doc"},
				Op:     &fpv1.Operation_ConvertToPdf{ConvertToPdf: &fpv1.ConvertToPDFOp{}},
			},
			{
				Name:   "thumb",
				Inputs: []string{"convert"},
				Op:     &fpv1.Operation_Thumbnail{Thumbnail: &fpv1.ThumbnailOp{Width: 400, Dpi: 150}},
			},
		},
		Destinations: map[string]*fpv1.FileRef{
			"convert": {Bucket: testBucket, Key: "wf-output.pdf", ContentType: "application/pdf"},
			"thumb":   {Bucket: testBucket, Key: "wf-thumb.jpg", ContentType: "image/jpeg"},
		},
	}))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if resp.Msg.JobId == "" {
		t.Error("Process.JobId is empty")
	}
	if resp.Msg.WorkflowId == "" {
		t.Error("Process.WorkflowId is empty")
	}

	// Poll GetJob until the workflow completes.
	var jobResp *connect.Response[fpv1.GetJobResponse]
	for i := 0; i < 60; i++ {
		jobResp, err = fpClient.GetJob(ctx, connect.NewRequest(&fpv1.GetJobRequest{
			JobId: resp.Msg.JobId,
		}))
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if jobResp.Msg.Status == fpv1.JobStatus_JOB_STATUS_COMPLETED ||
			jobResp.Msg.Status == fpv1.JobStatus_JOB_STATUS_FAILED {
			break
		}
		time.Sleep(time.Second)
	}

	if jobResp.Msg.Status != fpv1.JobStatus_JOB_STATUS_COMPLETED {
		t.Fatalf("job status = %v, want COMPLETED (error: %s)", jobResp.Msg.Status, jobResp.Msg.Error)
	}

	// Check operation results from job.
	convertResult, ok := jobResp.Msg.Results["convert"]
	if !ok {
		t.Fatal("missing 'convert' result")
	}
	if !convertResult.Success {
		t.Errorf("convert failed: %s", convertResult.Error)
	}

	thumbResult, ok := jobResp.Msg.Results["thumb"]
	if !ok {
		t.Fatal("missing 'thumb' result")
	}
	if !thumbResult.Success {
		t.Errorf("thumb failed: %s", thumbResult.Error)
	}

	// Verify outputs were uploaded to destinations.
	pdfData := downloadFile(t, objClient, ts, testBucket, "wf-output.pdf")
	if !bytes.HasPrefix(pdfData, []byte("%PDF")) {
		t.Error("workflow PDF output is not a valid PDF")
	}

	thumbData := downloadFile(t, objClient, ts, testBucket, "wf-thumb.jpg")
	if len(thumbData) == 0 {
		t.Error("workflow thumbnail output is empty")
	}
}
