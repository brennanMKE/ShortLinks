// Package reports holds the shared model behind every analytics report served
// by GET /api/reports/{name} and, later, the MCP tools (#0138): the query
// parameters (params.go), the date windows (window.go), the SQL filter
// predicate (filter.go), the response envelope and the report registry
// (registry.go). A report is a Report value registered once; the Service
// parses parameters, resolves windows, opens one REPEATABLE READ read-only
// transaction and wraps the report's output in the common Envelope.
package reports
