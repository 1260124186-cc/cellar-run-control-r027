package httpapi

import (
	"net/http"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

func registerAuditRoutes(mux *http.ServeMux, app *service.Service) {
	// Audit history is read-only: the only audit route is this GET. Nothing
	// on the HTTP surface can modify, delete, or rewrite entries.
	mux.HandleFunc("GET /v1/audit-entries", func(response http.ResponseWriter, request *http.Request) {
		query, err := parseAuditQuery(request)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		entries, err := app.AuditEntries(query)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"audit_entries": entries})
	})
}

func parseAuditQuery(request *http.Request) (audit.Query, error) {
	var query audit.Query
	values := request.URL.Query()

	if raw := values.Get("target_kind"); raw != "" {
		kind := audit.Kind(raw)
		if !kind.Valid() {
			return audit.Query{}, domain.FieldError(domain.CodeInvalidInput,
				"target_kind", "must be formula, vessel, or run")
		}
		query.TargetKind = kind
	}
	query.TargetID = values.Get("target_id")

	if raw := values.Get("operation"); raw != "" {
		operation := audit.Operation(raw)
		if !operation.Valid() {
			return audit.Query{}, domain.FieldError(domain.CodeInvalidInput,
				"operation", "must be a known audit operation")
		}
		query.Operation = operation
	}

	if raw := values.Get("from"); raw != "" {
		from, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return audit.Query{}, domain.FieldError(domain.CodeInvalidInput,
				"from", "must be an RFC3339 timestamp")
		}
		from = from.UTC()
		query.From = &from
	}
	if raw := values.Get("to"); raw != "" {
		to, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return audit.Query{}, domain.FieldError(domain.CodeInvalidInput,
				"to", "must be an RFC3339 timestamp")
		}
		to = to.UTC()
		query.To = &to
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return audit.Query{}, domain.FieldError(domain.CodeInvalidInput,
			"from", "must not be later than to")
	}
	return query, nil
}
