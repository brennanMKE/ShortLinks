package main

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator/schema_validation"
	"gopkg.in/yaml.v3"

	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/handlers"
	"github.com/brennanMKE/ShortLinks/internal/openapi"
)

// apiRoutes returns the "METHOD /path" patterns buildMux registers under /api,
// in the same spelling as an OpenAPI path: Go 1.22 patterns already use
// {name} wildcards, so normalising is just splitting the method off and
// keeping /api routes. HEAD is implied by GET and is not a separate route.
func apiRoutes(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok {
			method, path = "", p // a method-less pattern matches every method
		}
		if !strings.HasPrefix(path, "/api/") {
			continue
		}
		out = append(out, strings.TrimSpace(method)+" "+path)
	}
	sort.Strings(out)
	return out
}

// specOperations parses the embedded spec and returns its operations as
// "METHOD /path", failing the test if the document does not build.
func specOperations(t *testing.T) []string {
	t.Helper()
	doc, err := libopenapi.NewDocument(openapi.Spec())
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	model, err := doc.BuildV3Model()
	if err != nil || model == nil {
		t.Fatalf("build spec model: %v", err)
	}
	var out []string
	for path, item := range model.Model.Paths.PathItems.FromOldest() {
		for method := range item.GetOperations().FromOldest() {
			out = append(out, strings.ToUpper(method)+" "+path)
		}
	}
	sort.Strings(out)
	return out
}

// diffRoutes returns the routes missing from the spec and the spec operations
// that match no route.
func diffRoutes(routes, ops []string) (missing, extra []string) {
	inSpec := map[string]bool{}
	for _, o := range ops {
		inSpec[o] = true
	}
	inRouter := map[string]bool{}
	for _, r := range routes {
		inRouter[r] = true
		if !inSpec[r] {
			missing = append(missing, r)
		}
	}
	for _, o := range ops {
		if !inRouter[o] {
			extra = append(extra, o)
		}
	}
	return missing, extra
}

// TestOpenAPIDrift fails when an /api route registered in buildMux has no
// entry in the OpenAPI spec, or the spec describes a route that does not
// exist (#0148).
func TestOpenAPIDrift(t *testing.T) {
	routes := apiRoutes(routePatterns())
	ops := specOperations(t)
	if len(routes) == 0 || len(ops) == 0 {
		t.Fatalf("empty route table (%d) or spec (%d)", len(routes), len(ops))
	}
	missing, extra := diffRoutes(routes, ops)
	if len(missing) > 0 {
		t.Errorf("/api routes missing from internal/openapi/openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("spec operations with no route in buildMux:\n  %s", strings.Join(extra, "\n  "))
	}
	if t.Failed() {
		return
	}
	t.Logf("%d /api routes, %d spec operations, in step", len(routes), len(ops))
}

// TestDiffRoutes proves the comparison reports both kinds of drift.
func TestDiffRoutes(t *testing.T) {
	routes := []string{"GET /api/a", "POST /api/a", "GET /api/b/{id}"}
	missing, extra := diffRoutes(routes, []string{"GET /api/a", "GET /api/b/{id}", "DELETE /api/c"})
	if len(missing) != 1 || missing[0] != "POST /api/a" {
		t.Errorf("missing = %v, want [POST /api/a]", missing)
	}
	if len(extra) != 1 || extra[0] != "DELETE /api/c" {
		t.Errorf("extra = %v, want [DELETE /api/c]", extra)
	}
}

// TestOpenAPIValid validates the embedded document against the OpenAPI 3.1
// schema (unknown keywords, wrong member types). An unresolved $ref is caught
// by BuildV3Model, not by that schema check; the walk in
// TestOpenAPISchemaTypes covers invalid schema "type" values.
func TestOpenAPIValid(t *testing.T) {
	doc, err := libopenapi.NewDocument(openapi.Spec())
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	if v := doc.GetSpecInfo().Version; !strings.HasPrefix(v, "3.1") {
		t.Fatalf("spec version = %q, want 3.1.x", v)
	}
	ok, errs := validator.ValidateOpenAPIDocument(doc)
	for _, e := range errs {
		t.Errorf("invalid: %s (%s)", e.Message, e.Reason)
		for _, ve := range e.SchemaValidationErrors {
			t.Errorf("   at %s: %s", ve.FieldPath, ve.Reason)
		}
	}
	if !ok && len(errs) == 0 {
		t.Error("spec reported invalid with no errors")
	}
	if _, err := doc.BuildV3Model(); err != nil {
		t.Errorf("build model: %v", err)
	}
}

// TestOpenAPISchemaTypes walks every schema in the document (the generic
// YAML tree, so nothing depends on a model's lazy resolution) and fails on a
// "type" that is not a JSON Schema type, which the OpenAPI document schema
// accepts as an arbitrary-valued keyword inside a schema.
func TestOpenAPISchemaTypes(t *testing.T) {
	var root map[string]any
	if err := yaml.Unmarshal(openapi.Spec(), &root); err != nil {
		t.Fatal(err)
	}
	valid := map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "array": true, "object": true, "null": true}
	// schemaMaps are keywords whose value is a map of name -> schema, and
	// schemaLists hold a list of schemas; everything else named here is one schema.
	schemaMaps := map[string]bool{"properties": true, "patternProperties": true, "$defs": true}
	schemaLists := map[string]bool{"allOf": true, "anyOf": true, "oneOf": true, "prefixItems": true}
	schemaOne := map[string]bool{"items": true, "additionalProperties": true, "not": true, "if": true, "then": true, "else": true}
	checked := 0
	var walk func(path string, n any)
	walk = func(path string, n any) {
		m, ok := n.(map[string]any)
		if !ok {
			return
		}
		checked++
		switch ty := m["type"].(type) {
		case string:
			if !valid[ty] {
				t.Errorf("%s: invalid type %q", path, ty)
			}
		case []any:
			for _, e := range ty {
				if es, ok := e.(string); !ok || !valid[es] {
					t.Errorf("%s: invalid type %v", path, e)
				}
			}
		case nil:
		default:
			t.Errorf("%s: type must be a string or list, got %T", path, ty)
		}
		for k, v := range m {
			switch {
			case schemaMaps[k]:
				if sub, ok := v.(map[string]any); ok {
					for name, c := range sub {
						walk(path+"/"+k+"/"+name, c)
					}
				}
			case schemaLists[k]:
				if sub, ok := v.([]any); ok {
					for i, c := range sub {
						walk(path+"/"+k+"/"+strconv.Itoa(i), c)
					}
				}
			case schemaOne[k]:
				walk(path+"/"+k, v)
			}
		}
	}
	comps, _ := root["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	for name, sc := range schemas {
		walk("#/components/schemas/"+name, sc)
	}
	// Inline schemas: parameters, request bodies, responses and headers.
	var inline func(path string, n any)
	inline = func(path string, n any) {
		switch v := n.(type) {
		case map[string]any:
			if sc, ok := v["schema"]; ok {
				walk(path+"/schema", sc)
			}
			for k, c := range v {
				if k != "schemas" {
					inline(path+"/"+k, c)
				}
			}
		case []any:
			for i, c := range v {
				inline(path+"/"+strconv.Itoa(i), c)
			}
		}
	}
	inline("#", root)
	if checked < 100 {
		t.Errorf("walked only %d schemas; the walk is not reaching the document", checked)
	}
}

// TestOpenAPIOperationConventions checks what the schema cannot: every
// operation has an id (unique) and a 401 or is explicitly unauthenticated, and
// the three creates document Idempotency-Key.
func TestOpenAPIOperationConventions(t *testing.T) {
	doc, err := libopenapi.NewDocument(openapi.Spec())
	if err != nil {
		t.Fatal(err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	idem := map[string]bool{}
	for path, item := range model.Model.Paths.PathItems.FromOldest() {
		for method, op := range item.GetOperations().FromOldest() {
			name := strings.ToUpper(method) + " " + path
			if op.OperationId == "" {
				t.Errorf("%s has no operationId", name)
			} else if prev, dup := ids[op.OperationId]; dup {
				t.Errorf("operationId %q used by %s and %s", op.OperationId, prev, name)
			}
			ids[op.OperationId] = name
			if op.Responses == nil || op.Responses.Codes.Len() == 0 {
				t.Errorf("%s has no responses", name)
			}
			for _, p := range op.Parameters {
				if p.Name == "Idempotency-Key" {
					idem[name] = true
				}
			}
		}
	}
	for _, want := range []string{"POST /api/links", "POST /api/campaigns", "POST /api/campaigns/{slug}/links/batch"} {
		if !idem[want] {
			t.Errorf("%s does not document Idempotency-Key", want)
		}
	}
}

// TestOpenAPIEndpoint serves GET /api/openapi.yaml through the real route table
// with an authentication middleware that refuses everything, proving the route
// is public and returns the embedded document.
func TestOpenAPIEndpoint(t *testing.T) {
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
		})
	}
	h, _ := buildMux(&config.Config{BaseURL: "https://go.example.test"}, nil,
		&handlers.AuthHandler{}, &handlers.CredentialsHandler{}, &handlers.SettingsHandler{},
		&handlers.AdminUsersHandler{}, &handlers.AdminAuditHandler{}, &handlers.URLFiltersHandler{},
		&handlers.EventsHandler{}, &handlers.RedirectHandler{}, &handlers.LinksHandler{},
		&handlers.CampaignsHandler{}, &handlers.ReportsHandler{}, &handlers.MeHandler{},
		&handlers.HomeHandler{}, &handlers.APITokensHandler{},
		deny, deny, deny, deny, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 without credentials", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	if rec.Body.String() != string(openapi.Spec()) {
		t.Error("body is not the embedded spec")
	}
	if !strings.HasPrefix(rec.Body.String(), "openapi: 3.1") {
		t.Error("body does not start with an openapi 3.1 header")
	}

	// A protected route on the same mux does refuse, so the check above is meaningful.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/me status = %d, want 401", rec.Code)
	}
}

// TestOpenAPIETag checks the conditional GET: a matching If-None-Match gets
// 304 with no body, a stale one gets the document.
func TestOpenAPIETag(t *testing.T) {
	h := openapi.Handler()
	get := func(inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := get("")
	etag := first.Header().Get("ETag")
	if len(etag) < 10 || etag[0] != '"' {
		t.Fatalf("ETag = %q, want a quoted hash", etag)
	}
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		rec := get(inm)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %q: status %d, %d body bytes; want 304 and none", inm, rec.Code, rec.Body.Len())
		}
		if rec.Header().Get("ETag") != etag {
			t.Errorf("304 for %q lacks the ETag", inm)
		}
	}
	rec := get(`"stale"`)
	if rec.Code != http.StatusOK || rec.Body.Len() != len(openapi.Spec()) {
		t.Errorf("stale If-None-Match: status %d, %d bytes; want 200 and the spec", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("ETag") != etag {
		t.Error("ETag changed between requests")
	}
}
