package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

func registerAuditRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("GET /v1/audit-entries",
		func(response http.ResponseWriter, request *http.Request) {
			filter, err := parseAuditFilter(request)
			if !handleDecode(response, err) {
				return
			}
			entries, err := app.QueryAudit(filter)
			if err != nil {
				writeDomainError(response, err)
				return
			}
			writeJSON(response, http.StatusOK, map[string]any{"entries": entries})
		})
}

func parseAuditFilter(request *http.Request) (audit.Filter, error) {
	query := request.URL.Query()
	filter := audit.Filter{
		TargetID:   strings.TrimSpace(query.Get("target_id")),
		TargetKind: strings.TrimSpace(query.Get("target_kind")),
	}
	if filter.TargetKind != "" && !audit.ValidTargetKind(filter.TargetKind) {
		return filter, domain.FieldError(domain.CodeInvalidInput,
			"target_kind", "must be formula, vessel, or run")
	}
	if operation := strings.TrimSpace(query.Get("operation")); operation != "" {
		if !audit.ValidOperation(operation) {
			return filter, domain.FieldError(domain.CodeInvalidInput,
				"operation", "must be a known audit operation")
		}
		filter.Operation = audit.Operation(operation)
	}
	from, err := parseAuditTime(query.Get("from"), "from")
	if err != nil {
		return filter, err
	}
	to, err := parseAuditTime(query.Get("to"), "to")
	if err != nil {
		return filter, err
	}
	if from != nil && to != nil && from.After(*to) {
		return filter, domain.NewError(domain.CodeInvalidInput,
			"query time range is inverted: from is after to")
	}
	filter.From = from
	filter.To = to
	return filter, nil
}

func parseAuditTime(value, field string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, domain.FieldError(domain.CodeInvalidInput,
			field, "must be an RFC3339 timestamp")
	}
	utc := parsed.UTC()
	return &utc, nil
}
