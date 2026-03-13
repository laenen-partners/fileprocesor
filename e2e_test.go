package fileprocesor_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	fileprocesor "github.com/laenen-partners/fileprocesor"
	"github.com/laenen-partners/jobs"
	jobspg "github.com/laenen-partners/jobs/postgres"
	"github.com/laenen-partners/objectstore"
	"github.com/laenen-partners/objectstore/tokenstore"
)

const testBucket = "e2e-test"

// newTestStore creates a local objectstore with the test bucket.
func newTestStore(t *testing.T) objectstore.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := objectstore.NewLocalStore(dir, "http://localhost", newTestTokenValidator("test-secret"))
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	if err := store.EnsureBucket(context.Background(), testBucket); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	return store
}

// startE2E boots a Processor backed by real docker-compose services.
// No database required — only sync methods are available.
func startE2E(t *testing.T) (*fileprocesor.Processor, objectstore.Store) {
	t.Helper()

	gotenbergURL := envOrSkip(t, "GOTENBERG_URL")
	doclingURL := envOrSkip(t, "DOCLING_URL")
	pdf2imgURL := envOrSkip(t, "PDF2IMG_URL")

	store := newTestStore(t)

	cfg := fileprocesor.Config{
		GotenbergURL: gotenbergURL,
		DoclingURL:   doclingURL,
		PDF2ImgURL:   pdf2imgURL,
		ClamAVAddr:   os.Getenv("CLAMAV_ADDRESS"),
	}

	proc, err := fileprocesor.NewProcessor(cfg, store)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	return proc, store
}

// startPostgresContainer spins up a Postgres testcontainer and returns the connection string.
func startPostgresContainer(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	pgc, err := tcpg.Run(ctx, "postgres:17",
		tcpg.WithDatabase("fileprocessor_test"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := pgc.Terminate(ctx); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	return connStr
}

// startE2EWithDB boots a Processor with DBOS + jobs for workflow tests.
// Uses testcontainers for Postgres — no external database required.
func startE2EWithDB(t *testing.T) (*fileprocesor.Processor, objectstore.Store) {
	t.Helper()

	gotenbergURL := envOrSkip(t, "GOTENBERG_URL")
	doclingURL := envOrSkip(t, "DOCLING_URL")
	pdf2imgURL := envOrSkip(t, "PDF2IMG_URL")

	dbURL := startPostgresContainer(t)
	ctx := context.Background()

	store := newTestStore(t)

	// Run jobs migrations.
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := jobspg.Migrate(ctx, pool, ""); err != nil {
		t.Fatalf("jobs migrate: %v", err)
	}

	// Consumer owns DBOS lifecycle.
	dbosCtx, err := dbos.NewDBOSContext(ctx, dbos.Config{
		AppName:     "fileprocessor-test",
		DatabaseURL: dbURL,
	})
	if err != nil {
		t.Fatalf("init DBOS: %v", err)
	}

	// Consumer owns jobs client backed by Postgres.
	jobStore := jobspg.NewStore(pool)
	jobsClient := jobs.NewClient(jobStore)

	cfg := fileprocesor.Config{
		GotenbergURL: gotenbergURL,
		DoclingURL:   doclingURL,
		PDF2ImgURL:   pdf2imgURL,
		ClamAVAddr:   os.Getenv("CLAMAV_ADDRESS"),
	}

	proc, err := fileprocesor.NewProcessor(cfg, store,
		fileprocesor.WithDBOS(dbosCtx),
		fileprocesor.WithJobs(jobsClient),
	)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	// Consumer registers workflows and launches DBOS.
	dbos.RegisterWorkflow(dbosCtx, proc.ProcessWorkflow)
	if err := dbos.Launch(dbosCtx); err != nil {
		t.Fatalf("launch DBOS: %v", err)
	}
	t.Cleanup(func() {
		dbos.Shutdown(dbosCtx, 10*time.Second)
	})

	return proc, store
}

func envOrSkip(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("skipping: %s not set (run docker compose up and source .env)", key)
	}
	return v
}

// uploadTestFile writes a file directly to the store.
func uploadTestFile(t *testing.T, store objectstore.Store, key, contentType string, data []byte) {
	t.Helper()
	err := store.PutObject(context.Background(), testBucket, key, bytes.NewReader(data), int64(len(data)), contentType)
	if err != nil {
		t.Fatalf("PutObject %s: %v", key, err)
	}
}

// downloadTestFile reads a file directly from the store.
func downloadTestFile(t *testing.T, store objectstore.Store, bucket, key string) []byte {
	t.Helper()
	rc, err := store.GetObject(context.Background(), bucket, key)
	if err != nil {
		t.Fatalf("GetObject %s/%s: %v", bucket, key, err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatalf("read %s/%s: %v", bucket, key, err)
	}
	return buf.Bytes()
}

func TestE2E_ScanFile_Disabled(t *testing.T) {
	if os.Getenv("CLAMAV_ADDRESS") != "" {
		t.Skip("skipping disabled-scan test: CLAMAV_ADDRESS is set")
	}
	proc, store := startE2E(t)
	ctx := context.Background()

	uploadTestFile(t, store, "scan-test.txt", "text/plain", []byte("hello world"))

	resp, err := proc.ScanFile(ctx, fileprocesor.ScanFileRequest{
		Bucket: testBucket,
		Key:    "scan-test.txt",
	})
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if resp.Clean {
		t.Error("ScanFile.Clean = true, want false (scanning disabled returns clean=false)")
	}
	if resp.Detail == "" {
		t.Error("ScanFile.Detail is empty, want disabled message")
	}
}

func TestE2E_ConvertToPDF(t *testing.T) {
	proc, store := startE2E(t)
	ctx := context.Background()

	uploadTestFile(t, store, "doc.txt", "text/plain", []byte("Hello, Gotenberg!"))

	resp, err := proc.ConvertToPDF(ctx, fileprocesor.ConvertToPDFRequest{
		Bucket:      testBucket,
		Key:         "doc.txt",
		ContentType: "text/plain",
		Destination: &fileprocesor.FileRef{
			Bucket:      testBucket,
			Key:         "doc.pdf",
			ContentType: "application/pdf",
		},
	})
	if err != nil {
		t.Fatalf("ConvertToPDF: %v", err)
	}
	if resp.SizeBytes <= 0 {
		t.Errorf("ConvertToPDF.SizeBytes = %d, want > 0", resp.SizeBytes)
	}
	if resp.Destination == nil || resp.Destination.Key != "doc.pdf" {
		t.Errorf("ConvertToPDF.Destination.Key = %v, want doc.pdf", resp.Destination)
	}

	pdfData := downloadTestFile(t, store, testBucket, "doc.pdf")
	if !bytes.HasPrefix(pdfData, []byte("%PDF")) {
		t.Errorf("downloaded file is not a PDF, starts with %q", string(pdfData[:min(20, len(pdfData))]))
	}
}

func TestE2E_MergePDFs(t *testing.T) {
	proc, store := startE2E(t)
	ctx := context.Background()

	// Convert two text files to PDF first.
	for _, name := range []string{"a.txt", "b.txt"} {
		uploadTestFile(t, store, name, "text/plain", []byte("Content of "+name))
		_, err := proc.ConvertToPDF(ctx, fileprocesor.ConvertToPDFRequest{
			Bucket:      testBucket,
			Key:         name,
			ContentType: "text/plain",
			Destination: &fileprocesor.FileRef{
				Bucket:      testBucket,
				Key:         name + ".pdf",
				ContentType: "application/pdf",
			},
		})
		if err != nil {
			t.Fatalf("ConvertToPDF %s: %v", name, err)
		}
	}

	resp, err := proc.MergePDFs(ctx, fileprocesor.MergePDFsRequest{
		Files: []fileprocesor.FileRef{
			{Bucket: testBucket, Key: "a.txt.pdf"},
			{Bucket: testBucket, Key: "b.txt.pdf"},
		},
		Destination: &fileprocesor.FileRef{
			Bucket:      testBucket,
			Key:         "merged.pdf",
			ContentType: "application/pdf",
		},
	})
	if err != nil {
		t.Fatalf("MergePDFs: %v", err)
	}
	if resp.SizeBytes <= 0 {
		t.Errorf("MergePDFs.SizeBytes = %d, want > 0", resp.SizeBytes)
	}

	merged := downloadTestFile(t, store, testBucket, "merged.pdf")
	if !bytes.HasPrefix(merged, []byte("%PDF")) {
		t.Error("merged file is not a PDF")
	}
}

func TestE2E_GenerateThumbnail(t *testing.T) {
	proc, store := startE2E(t)
	ctx := context.Background()

	// Convert text to PDF first (pdf2img needs a real PDF).
	uploadTestFile(t, store, "thumb-src.txt", "text/plain", []byte("Thumbnail test"))
	_, err := proc.ConvertToPDF(ctx, fileprocesor.ConvertToPDFRequest{
		Bucket:      testBucket,
		Key:         "thumb-src.txt",
		ContentType: "text/plain",
		Destination: &fileprocesor.FileRef{
			Bucket:      testBucket,
			Key:         "thumb-src.pdf",
			ContentType: "application/pdf",
		},
	})
	if err != nil {
		t.Fatalf("ConvertToPDF: %v", err)
	}

	resp, err := proc.GenerateThumbnail(ctx, fileprocesor.GenerateThumbnailRequest{
		Bucket: testBucket,
		Key:    "thumb-src.pdf",
		Destination: &fileprocesor.FileRef{
			Bucket:      testBucket,
			Key:         "thumb.jpg",
			ContentType: "image/jpeg",
		},
	})
	if err != nil {
		t.Fatalf("GenerateThumbnail: %v", err)
	}
	if resp.SizeBytes <= 0 {
		t.Errorf("GenerateThumbnail.SizeBytes = %d, want > 0", resp.SizeBytes)
	}

	thumbData := downloadTestFile(t, store, testBucket, "thumb.jpg")
	if len(thumbData) == 0 {
		t.Error("thumbnail file is empty")
	}
}

func TestE2E_ExtractMarkdown(t *testing.T) {
	proc, store := startE2E(t)
	ctx := context.Background()

	// Convert text to PDF (Docling works best with PDFs).
	uploadTestFile(t, store, "extract-src.txt", "text/plain", []byte("Extract markdown test content"))
	_, err := proc.ConvertToPDF(ctx, fileprocesor.ConvertToPDFRequest{
		Bucket:      testBucket,
		Key:         "extract-src.txt",
		ContentType: "text/plain",
		Destination: &fileprocesor.FileRef{
			Bucket:      testBucket,
			Key:         "extract-src.pdf",
			ContentType: "application/pdf",
		},
	})
	if err != nil {
		t.Fatalf("ConvertToPDF: %v", err)
	}

	resp, err := proc.ExtractMarkdown(ctx, fileprocesor.ExtractMarkdownRequest{
		Bucket: testBucket,
		Key:    "extract-src.pdf",
		DoclingJSONDestination: &fileprocesor.FileRef{
			Bucket:      testBucket,
			Key:         "extract.json",
			ContentType: "application/json",
		},
	})
	if err != nil {
		t.Fatalf("ExtractMarkdown: %v", err)
	}
	if resp.Markdown == "" {
		t.Error("ExtractMarkdown.Markdown is empty")
	}

	jsonData := downloadTestFile(t, store, testBucket, "extract.json")
	if len(jsonData) == 0 {
		t.Error("docling JSON file is empty")
	}
}

func TestE2E_ProcessWorkflow(t *testing.T) {
	proc, store := startE2EWithDB(t)
	ctx := context.Background()

	uploadTestFile(t, store, "workflow-doc.txt", "text/plain", []byte("Workflow end-to-end test"))

	resp, err := proc.Process(ctx, fileprocesor.ProcessInput{
		Inputs: []fileprocesor.FileInput{
			{
				Name:        "doc",
				Bucket:      testBucket,
				Key:         "workflow-doc.txt",
				ContentType: "text/plain",
			},
		},
		Operations: []fileprocesor.Operation{
			{
				Name:         "convert",
				Inputs:       []string{"doc"},
				ConvertToPDF: &fileprocesor.ConvertToPDFOp{},
			},
			{
				Name:      "thumb",
				Inputs:    []string{"convert"},
				Thumbnail: &fileprocesor.ThumbnailOp{Width: 400, DPI: 150},
			},
		},
		Destinations: map[string]fileprocesor.FileRef{
			"convert": {Bucket: testBucket, Key: "wf-output.pdf", ContentType: "application/pdf"},
			"thumb":   {Bucket: testBucket, Key: "wf-thumb.jpg", ContentType: "image/jpeg"},
		},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if resp.JobID == "" {
		t.Error("Process.JobID is empty")
	}
	if resp.ExternalReference == "" {
		t.Error("Process.ExternalReference is empty")
	}

	// Poll GetJob until the workflow completes.
	var job *fileprocesor.JobInfo
	for i := 0; i < 60; i++ {
		job, err = proc.GetJob(ctx, resp.JobID)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if job.Status == fileprocesor.JobStatusCompleted || job.Status == fileprocesor.JobStatusFailed {
			break
		}
		time.Sleep(time.Second)
	}

	if job.Status != fileprocesor.JobStatusCompleted {
		t.Fatalf("job status = %v, want completed (error: %s)", job.Status, job.Error)
	}

	convertResult, ok := job.Results["convert"]
	if !ok {
		t.Fatal("missing 'convert' result")
	}
	if !convertResult.Success {
		t.Errorf("convert failed: %s", convertResult.Error)
	}

	thumbResult, ok := job.Results["thumb"]
	if !ok {
		t.Fatal("missing 'thumb' result")
	}
	if !thumbResult.Success {
		t.Errorf("thumb failed: %s", thumbResult.Error)
	}

	pdfData := downloadTestFile(t, store, testBucket, "wf-output.pdf")
	if !bytes.HasPrefix(pdfData, []byte("%PDF")) {
		t.Error("workflow PDF output is not a valid PDF")
	}

	thumbData := downloadTestFile(t, store, testBucket, "wf-thumb.jpg")
	if len(thumbData) == 0 {
		t.Error("workflow thumbnail output is empty")
	}
}

// --- Test token validator (HMAC-based, for local objectstore) ---

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
