// Command genapi fetches the rave.page OpenAPI 3 schema and generates a filtered
// Go client (only the operations rave-mate calls) via oapi-codegen.
//
// Mirrors the web repo's generate-api-client.js URL resolution:
//
//	RAVE_API_BASE_URL env → git branch (master/main = prod, else dev) → prod fallback.
//
// Dev/localhost hosts get InsecureSkipVerify (dev API has a self-signed cert).
//
// Run from the rave-mate root: `go run ./tools/genapi` (or `make generate-api`).
// Lives in its own module so oapi-codegen's heavy deps stay out of the app module.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oapi-codegen/oapi-codegen/v2/pkg/codegen"
)

// Operations rave-mate actually calls. Keep this list tight - every added id
// pulls its request/response models into the generated client.
var includeOps = []string{
	"createDesktopGrant",      // POST /auth/grant
	"exchangeDesktopGrant",    // POST /auth/exchange
	"createLiveStream",        // POST /streams
	"ingestLiveStreamEvents",  // POST /streams/{id}/ingest
	"heartbeatLiveStream",     // POST /streams/{id}/heartbeat
	"endLiveStream",           // POST /streams/{id}/end
	"getLiveStreamNowPlaying", // GET  /streams/{id}/now-playing
	// play/tracklist layer (Gap 1 - fingerprint identification + provisional catalog)
	"identifyTrackByFingerprint", // POST /tracks/fingerprint/identify
	"createTrackFingerprint",     // POST /tracks/{id}/fingerprints
	"createTrack",                // POST /tracks (provisional for unmatched plays)
	"createTrackObservation",     // POST /tracks/{id}/observations (metadata enrich)
	"getTrack",                   // GET  /tracks/{id}
	"getTrackByISRC",             // GET  /tracks/by-isrc/{isrc}
	"storeVrchatToken",           // POST   /auth/vrchat/token (opt-in uplink)
	"testVrchatConnection",       // GET    /auth/vrchat/test
	"deleteVrchatCredentials",    // DELETE /auth/vrchat/credentials
}

// outFile resolves to <rave-mate-root>/internal/apiclient/apiclient.gen.go regardless
// of CWD: this source is at <root>/tools/genapi/main.go, so root is two dirs up.
func outFilePath() string {
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(self)))
	return filepath.Join(root, "internal", "apiclient", "apiclient.gen.go")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genapi: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	base := resolveAPIBase()
	specURL := strings.TrimRight(base, "/") + "/openapi.json"
	fmt.Println("genapi: fetching", specURL)

	raw, err := fetchSpec(specURL)
	if err != nil {
		return fmt.Errorf("fetch spec: %w", err)
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	doc, err := loader.LoadFromData(raw)
	if err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}
	// Skip doc.Validate - the full prod spec trips strict validation on shapes
	// we don't generate; oapi-codegen only needs the included operations to resolve.
	fixupParamSchemas(doc) // swaggo emits path params with no schema; default them to string

	fmt.Printf("genapi: spec %s - %d paths, generating %d ops\n",
		doc.Info.Version, len(doc.Paths.Map()), len(includeOps))

	code, err := codegen.Generate(doc, codegen.Configuration{
		PackageName: "apiclient",
		Generate: codegen.GenerateOptions{
			Client: true,
			Models: true,
		},
		OutputOptions: codegen.OutputOptions{
			IncludeOperationIDs: includeOps,
		},
	})
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}

	code = appendMissingContextKeys(code)

	outFile := outFilePath()
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(outFile, []byte(code), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outFile, err)
	}
	fmt.Printf("genapi: wrote %s (%d bytes)\n", outFile, len(code))
	return nil
}

// appendMissingContextKeys defines the `xxxContextKey` types that oapi-codegen
// references in its security-scope consts but only emits when generating server
// code. Client-only generation leaves them undefined; append them so it compiles.
func appendMissingContextKeys(code string) string {
	re := regexp.MustCompile(`(\w+ContextKey)`)
	seen := map[string]bool{}
	var defs []string
	for _, m := range re.FindAllString(code, -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		if !strings.Contains(code, "type "+m+" ") {
			defs = append(defs, "type "+m+" string")
		}
	}
	if len(defs) == 0 {
		return code
	}
	return code + "\n// Security-scope context keys (oapi-codegen omits these in client-only mode).\n" +
		strings.Join(defs, "\n") + "\n"
}

// fixupParamSchemas gives every parameter that lacks both a schema and content a
// default string schema. Swaggo (the API's generator) omits schemas on path params,
// which oapi-codegen rejects.
func fixupParamSchemas(doc *openapi3.T) {
	fix := func(params openapi3.Parameters) {
		for _, pr := range params {
			if pr == nil || pr.Value == nil {
				continue
			}
			if pr.Value.Schema == nil && len(pr.Value.Content) == 0 {
				pr.Value.Schema = openapi3.NewSchemaRef("", openapi3.NewStringSchema())
			}
		}
	}
	for _, item := range doc.Paths.Map() {
		fix(item.Parameters)
		for _, op := range item.Operations() {
			fix(op.Parameters)
		}
	}
}

// fetchSpec GETs the schema; dev/localhost hosts skip TLS verification.
func fetchSpec(specURL string) ([]byte, error) {
	insecure := strings.Contains(specURL, "development.") ||
		strings.Contains(specURL, "localhost") ||
		strings.Contains(specURL, "127.0.0.1")
	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev cert only
	}
	cl := &http.Client{Timeout: 30 * time.Second, Transport: tr}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, specURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s -> %d", specURL, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// resolveAPIBase mirrors config.resolveAPIBase + the web generate script's branch detection.
func resolveAPIBase() string {
	if v := strings.TrimSpace(os.Getenv("RAVE_API_BASE_URL")); v != "" {
		return v
	}
	if strings.EqualFold(os.Getenv("RAVE_ENV"), "production") {
		return "https://api.rave.page"
	}
	switch gitBranch() {
	case "master", "main":
		return "https://api.rave.page"
	default:
		return "https://development.api.rave.page"
	}
}

func gitBranch() string {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
