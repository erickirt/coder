package searchquery

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/coderd/httpapi"
	"github.com/coder/coder/v2/codersdk"
)

// AuditLogs requires the database to fetch an organization by name
// to convert to organization uuid.
//
// Supported query parameters:
//
//   - request_id: UUID (can be used to search for associated audits e.g. connect/disconnect or open/close)
//   - resource_id: UUID
//   - resource_target: string
//   - username: string
//   - email: string
//   - date_from: string (date in format "2006-01-02")
//   - date_to: string (date in format "2006-01-02")
//   - organization: string (organization UUID or name)
//   - resource_type: string (enum)
//   - action: string (enum)
//   - build_reason: string (enum)
func AuditLogs(ctx context.Context, db database.Store, query string) (database.GetAuditLogsOffsetParams,
	database.CountAuditLogsParams, []codersdk.ValidationError,
) {
	// Always lowercase for all searches.
	query = strings.ToLower(query)
	values, errors := searchTerms(query, func(term string, values url.Values) error {
		values.Add("resource_type", term)
		return nil
	})
	if len(errors) > 0 {
		// nolint:exhaustruct // We don't need to initialize these structs because we return an error.
		return database.GetAuditLogsOffsetParams{}, database.CountAuditLogsParams{}, errors
	}

	const dateLayout = "2006-01-02"
	parser := httpapi.NewQueryParamParser()
	filter := database.GetAuditLogsOffsetParams{
		RequestID:      parser.UUID(values, uuid.Nil, "request_id"),
		ResourceID:     parser.UUID(values, uuid.Nil, "resource_id"),
		ResourceTarget: parser.String(values, "", "resource_target"),
		Username:       parser.String(values, "", "username"),
		Email:          parser.String(values, "", "email"),
		DateFrom:       parser.Time(values, time.Time{}, "date_from", dateLayout),
		DateTo:         parser.Time(values, time.Time{}, "date_to", dateLayout),
		OrganizationID: parseOrganization(ctx, db, parser, values, "organization"),
		ResourceType:   string(httpapi.ParseCustom(parser, values, "", "resource_type", httpapi.ParseEnum[database.ResourceType])),
		Action:         string(httpapi.ParseCustom(parser, values, "", "action", httpapi.ParseEnum[database.AuditAction])),
		BuildReason:    string(httpapi.ParseCustom(parser, values, "", "build_reason", httpapi.ParseEnum[database.BuildReason])),
	}
	if !filter.DateTo.IsZero() {
		filter.DateTo = filter.DateTo.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	}

	// Prepare the count filter, which uses the same parameters as the GetAuditLogsOffsetParams.
	// nolint:exhaustruct // UserID is not obtained from the query parameters.
	countFilter := database.CountAuditLogsParams{
		RequestID:      filter.RequestID,
		ResourceID:     filter.ResourceID,
		ResourceTarget: filter.ResourceTarget,
		Username:       filter.Username,
		Email:          filter.Email,
		DateFrom:       filter.DateFrom,
		DateTo:         filter.DateTo,
		OrganizationID: filter.OrganizationID,
		ResourceType:   filter.ResourceType,
		Action:         filter.Action,
		BuildReason:    filter.BuildReason,
	}

	parser.ErrorExcessParams(values)
	return filter, countFilter, parser.Errors
}

func ConnectionLogs(ctx context.Context, db database.Store, query string, apiKey database.APIKey) (database.GetConnectionLogsOffsetParams, database.CountConnectionLogsParams, []codersdk.ValidationError) {
	// Always lowercase for all searches.
	query = strings.ToLower(query)
	values, errors := searchTerms(query, func(term string, values url.Values) error {
		values.Add("search", term)
		return nil
	})
	if len(errors) > 0 {
		// nolint:exhaustruct // We don't need to initialize these structs because we return an error.
		return database.GetConnectionLogsOffsetParams{}, database.CountConnectionLogsParams{}, errors
	}

	parser := httpapi.NewQueryParamParser()
	filter := database.GetConnectionLogsOffsetParams{
		OrganizationID:      parseOrganization(ctx, db, parser, values, "organization"),
		WorkspaceOwner:      parser.String(values, "", "workspace_owner"),
		WorkspaceOwnerEmail: parser.String(values, "", "workspace_owner_email"),
		Type:                string(httpapi.ParseCustom(parser, values, "", "type", httpapi.ParseEnum[database.ConnectionType])),
		Username:            parser.String(values, "", "username"),
		UserEmail:           parser.String(values, "", "user_email"),
		ConnectedAfter:      parser.Time3339Nano(values, time.Time{}, "connected_after"),
		ConnectedBefore:     parser.Time3339Nano(values, time.Time{}, "connected_before"),
		WorkspaceID:         parser.UUID(values, uuid.Nil, "workspace_id"),
		ConnectionID:        parser.UUID(values, uuid.Nil, "connection_id"),
		Status:              string(httpapi.ParseCustom(parser, values, "", "status", httpapi.ParseEnum[codersdk.ConnectionLogStatus])),
	}

	if filter.Username == "me" {
		filter.UserID = apiKey.UserID
		filter.Username = ""
	}

	if filter.WorkspaceOwner == "me" {
		filter.WorkspaceOwnerID = apiKey.UserID
		filter.WorkspaceOwner = ""
	}

	// This MUST be kept in sync with the above
	countFilter := database.CountConnectionLogsParams{
		OrganizationID:      filter.OrganizationID,
		WorkspaceOwner:      filter.WorkspaceOwner,
		WorkspaceOwnerID:    filter.WorkspaceOwnerID,
		WorkspaceOwnerEmail: filter.WorkspaceOwnerEmail,
		Type:                filter.Type,
		UserID:              filter.UserID,
		Username:            filter.Username,
		UserEmail:           filter.UserEmail,
		ConnectedAfter:      filter.ConnectedAfter,
		ConnectedBefore:     filter.ConnectedBefore,
		WorkspaceID:         filter.WorkspaceID,
		ConnectionID:        filter.ConnectionID,
		Status:              filter.Status,
	}
	parser.ErrorExcessParams(values)
	return filter, countFilter, parser.Errors
}

func Users(query string) (database.GetUsersParams, []codersdk.ValidationError) {
	// Always lowercase for all searches.
	query = strings.ToLower(query)
	values, errors := searchTerms(query, func(term string, values url.Values) error {
		values.Add("search", term)
		return nil
	})
	if len(errors) > 0 {
		return database.GetUsersParams{}, errors
	}

	parser := httpapi.NewQueryParamParser()
	filter := database.GetUsersParams{
		Search:          parser.String(values, "", "search"),
		Status:          httpapi.ParseCustomList(parser, values, []database.UserStatus{}, "status", httpapi.ParseEnum[database.UserStatus]),
		RbacRole:        parser.Strings(values, []string{}, "role"),
		LastSeenAfter:   parser.Time3339Nano(values, time.Time{}, "last_seen_after"),
		LastSeenBefore:  parser.Time3339Nano(values, time.Time{}, "last_seen_before"),
		CreatedAfter:    parser.Time3339Nano(values, time.Time{}, "created_after"),
		CreatedBefore:   parser.Time3339Nano(values, time.Time{}, "created_before"),
		GithubComUserID: parser.Int64(values, 0, "github_com_user_id"),
		LoginType:       httpapi.ParseCustomList(parser, values, []database.LoginType{}, "login_type", httpapi.ParseEnum[database.LoginType]),
	}
	parser.ErrorExcessParams(values)
	return filter, parser.Errors
}

func Workspaces(ctx context.Context, db database.Store, query string, page codersdk.Pagination, agentInactiveDisconnectTimeout time.Duration) (database.GetWorkspacesParams, []codersdk.ValidationError) {
	filter := database.GetWorkspacesParams{
		AgentInactiveDisconnectTimeoutSeconds: int64(agentInactiveDisconnectTimeout.Seconds()),

		// #nosec G115 - Safe conversion for pagination offset which is expected to be within int32 range
		Offset: int32(page.Offset),
		// #nosec G115 - Safe conversion for pagination limit which is expected to be within int32 range
		Limit: int32(page.Limit),
	}

	if query == "" {
		return filter, nil
	}

	// Always lowercase for all searches.
	query = strings.ToLower(query)
	values, errors := searchTerms(query, func(term string, values url.Values) error {
		// It is a workspace name, and maybe includes an owner
		parts := splitQueryParameterByDelimiter(term, '/', false)
		switch len(parts) {
		case 1:
			values.Add("name", parts[0])
		case 2:
			values.Add("owner", parts[0])
			values.Add("name", parts[1])
		default:
			return xerrors.Errorf("Query element %q can only contain 1 '/'", term)
		}
		return nil
	})
	if len(errors) > 0 {
		return filter, errors
	}

	parser := httpapi.NewQueryParamParser()
	filter.WorkspaceIds = parser.UUIDs(values, []uuid.UUID{}, "id")
	filter.OwnerUsername = parser.String(values, "", "owner")
	filter.TemplateName = parser.String(values, "", "template")
	filter.Name = parser.String(values, "", "name")
	filter.Status = string(httpapi.ParseCustom(parser, values, "", "status", httpapi.ParseEnum[database.WorkspaceStatus]))
	filter.HasAgent = parser.String(values, "", "has-agent")
	filter.Dormant = parser.Boolean(values, false, "dormant")
	filter.LastUsedAfter = parser.Time3339Nano(values, time.Time{}, "last_used_after")
	filter.LastUsedBefore = parser.Time3339Nano(values, time.Time{}, "last_used_before")
	filter.UsingActive = sql.NullBool{
		// Invert the value of the query parameter to get the correct value.
		// UsingActive returns if the workspace is on the latest template active version.
		Bool: !parser.Boolean(values, true, "outdated"),
		// Only include this search term if it was provided. Otherwise default to omitting it
		// which will return all workspaces.
		Valid: values.Has("outdated"),
	}
	filter.HasAITask = parser.NullableBoolean(values, sql.NullBool{}, "has-ai-task")
	filter.OrganizationID = parseOrganization(ctx, db, parser, values, "organization")

	type paramMatch struct {
		name  string
		value *string
	}
	// parameter matching takes the form of:
	//	`param:<name>[=<value>]`
	// If the value is omitted, then we match on the presence of the parameter.
	// If the value is provided, then we match on the parameter and value.
	params := httpapi.ParseCustomList(parser, values, []paramMatch{}, "param", func(v string) (paramMatch, error) {
		// Ignore excess spaces
		v = strings.TrimSpace(v)
		parts := strings.Split(v, "=")
		if len(parts) == 1 {
			// Only match on the presence of the parameter
			return paramMatch{name: parts[0], value: nil}, nil
		}
		if len(parts) == 2 {
			if parts[1] == "" {
				return paramMatch{}, xerrors.Errorf("query element %q has an empty value. omit the '=' to match just on the parameter name", v)
			}
			// Match on the parameter and value
			return paramMatch{name: parts[0], value: &parts[1]}, nil
		}
		return paramMatch{}, xerrors.Errorf("query element %q can only contain 1 '='", v)
	})
	for _, p := range params {
		if p.value == nil {
			filter.HasParam = append(filter.HasParam, p.name)
			continue
		}
		filter.ParamNames = append(filter.ParamNames, p.name)
		filter.ParamValues = append(filter.ParamValues, *p.value)
	}

	parser.ErrorExcessParams(values)
	return filter, parser.Errors
}

func Templates(ctx context.Context, db database.Store, query string) (database.GetTemplatesWithFilterParams, []codersdk.ValidationError) {
	// Always lowercase for all searches.
	query = strings.ToLower(query)
	values, errors := searchTerms(query, func(term string, values url.Values) error {
		// Default to the template name
		values.Add("name", term)
		return nil
	})
	if len(errors) > 0 {
		return database.GetTemplatesWithFilterParams{}, errors
	}

	parser := httpapi.NewQueryParamParser()
	filter := database.GetTemplatesWithFilterParams{
		Deleted:        parser.Boolean(values, false, "deleted"),
		ExactName:      parser.String(values, "", "exact_name"),
		FuzzyName:      parser.String(values, "", "name"),
		IDs:            parser.UUIDs(values, []uuid.UUID{}, "ids"),
		Deprecated:     parser.NullableBoolean(values, sql.NullBool{}, "deprecated"),
		OrganizationID: parseOrganization(ctx, db, parser, values, "organization"),
		HasAITask:      parser.NullableBoolean(values, sql.NullBool{}, "has-ai-task"),
	}

	parser.ErrorExcessParams(values)
	return filter, parser.Errors
}

func searchTerms(query string, defaultKey func(term string, values url.Values) error) (url.Values, []codersdk.ValidationError) {
	searchValues := make(url.Values)

	// Because we do this in 2 passes, we want to maintain quotes on the first
	// pass. Further splitting occurs on the second pass and quotes will be
	// dropped.
	elements := splitQueryParameterByDelimiter(query, ' ', true)
	for _, element := range elements {
		if strings.HasPrefix(element, ":") || strings.HasSuffix(element, ":") {
			return nil, []codersdk.ValidationError{
				{
					Field:  "q",
					Detail: fmt.Sprintf("Query element %q cannot start or end with ':'", element),
				},
			}
		}
		parts := splitQueryParameterByDelimiter(element, ':', false)
		switch len(parts) {
		case 1:
			// No key:value pair. Use default behavior.
			err := defaultKey(element, searchValues)
			if err != nil {
				return nil, []codersdk.ValidationError{
					{Field: "q", Detail: err.Error()},
				}
			}
		case 2:
			searchValues.Add(strings.ToLower(parts[0]), parts[1])
		default:
			return nil, []codersdk.ValidationError{
				{
					Field:  "q",
					Detail: fmt.Sprintf("Query element %q can only contain 1 ':'", element),
				},
			}
		}
	}

	return searchValues, nil
}

func parseOrganization(ctx context.Context, db database.Store, parser *httpapi.QueryParamParser, vals url.Values, queryParam string) uuid.UUID {
	return httpapi.ParseCustom(parser, vals, uuid.Nil, queryParam, func(v string) (uuid.UUID, error) {
		if v == "" {
			return uuid.Nil, nil
		}
		organizationID, err := uuid.Parse(v)
		if err == nil {
			return organizationID, nil
		}
		organization, err := db.GetOrganizationByName(ctx, database.GetOrganizationByNameParams{
			Name: v, Deleted: false,
		})
		if err != nil {
			return uuid.Nil, xerrors.Errorf("organization %q either does not exist, or you are unauthorized to view it", v)
		}
		return organization.ID, nil
	})
}

// splitQueryParameterByDelimiter takes a query string and splits it into the individual elements
// of the query. Each element is separated by a delimiter. All quoted strings are
// kept as a single element.
//
// Although all our names cannot have spaces, that is a validation error.
// We should still parse the quoted string as a single value so that validation
// can properly fail on the space. If we do not, a value of `template:"my name"`
// will search `template:"my name:name"`, which produces an empty list instead of
// an error.
// nolint:revive
func splitQueryParameterByDelimiter(query string, delimiter rune, maintainQuotes bool) []string {
	quoted := false
	parts := strings.FieldsFunc(query, func(r rune) bool {
		if r == '"' {
			quoted = !quoted
		}
		return !quoted && r == delimiter
	})
	if !maintainQuotes {
		for i, part := range parts {
			parts[i] = strings.Trim(part, "\"")
		}
	}

	return parts
}
