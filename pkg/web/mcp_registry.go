package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP tools are a thin, declarative projection of the REST API. Each
// mcpOperation names one REST route and its parameters; the call is served by
// the same chi router, with the same handlers, middleware and RBAC checks as a
// browser request, under the identity the MCP bearer token resolved to. No
// business rule lives here, so MCP cannot drift from REST or gain rights the
// caller's role does not already carry. Later steps (S11, S13, S14, S15) add
// operations by appending to the registry; they do not touch the transport.

const (
	// mcpMaxResultBytes bounds one tool result or resource body. Larger bodies
	// are returned in byte_offset slices (see mcpSlice).
	mcpMaxResultBytes = 256 << 10
	// mcpDefaultPageSize / mcpMaxPageSize bound locally paginated arrays.
	mcpDefaultPageSize = 50
	mcpMaxPageSize     = 200
	mcpMaxStringArg    = 4096
)

type mcpParamKind string

const (
	mcpString  mcpParamKind = "string"
	mcpInteger mcpParamKind = "integer"
	mcpBoolean mcpParamKind = "boolean"
)

// mcpParam is one tool argument. A parameter whose name appears as {name} in
// the operation path is a path parameter; every other parameter is sent as a
// query parameter of the same name.
type mcpParam struct {
	Name        string
	Description string
	Kind        mcpParamKind
	Required    bool
	Enum        []string
	Min, Max    *int
	// Subpath marks a path parameter that may contain "/" (artifact paths).
	// Each segment is still validated; "." and ".." segments are rejected.
	Subpath bool
}

// mcpOperation is one product operation exposed over MCP.
type mcpOperation struct {
	Name        string
	Title       string
	Description string
	Method      string // GET only in S12; S13 extends the registry with actions.
	Path        string // REST path with {param} placeholders.
	Params      []mcpParam
	// ArrayKey names a top-level array in the JSON response that is paginated
	// locally with limit/offset ("" = no local pagination). RootArray means the
	// response itself is an array; it is returned as {ArrayKey: [...]}.
	ArrayKey  string
	RootArray bool
	// Resource is an optional MCP resource URI (template) serving the same
	// REST route, e.g. "aiteam://tasks/{run_id}".
	Resource string
}

// mcpRegistry holds the operations registered on one MCP server.
type mcpRegistry struct {
	operations []mcpOperation
	byName     map[string]bool
}

func newMCPRegistry() *mcpRegistry { return &mcpRegistry{byName: map[string]bool{}} }

func (r *mcpRegistry) add(operation mcpOperation) {
	if operation.Method == "" {
		operation.Method = http.MethodGet
	}
	if operation.Method != http.MethodGet {
		panic("mcp: operation " + operation.Name + ": only read operations are registered in S12")
	}
	if r.byName[operation.Name] {
		panic("mcp: duplicate operation " + operation.Name)
	}
	for _, name := range pathPlaceholders(operation.Path) {
		if !operation.hasParam(name) {
			panic("mcp: operation " + operation.Name + " has no parameter for {" + name + "}")
		}
	}
	r.operations = append(r.operations, operation)
	r.byName[operation.Name] = true
}

func (o mcpOperation) hasParam(name string) bool {
	for _, param := range o.Params {
		if param.Name == name {
			return true
		}
	}
	return false
}

func pathPlaceholders(path string) []string {
	var names []string
	for {
		start := strings.IndexByte(path, '{')
		if start < 0 {
			return names
		}
		end := strings.IndexByte(path[start:], '}')
		if end < 0 {
			return names
		}
		names = append(names, path[start+1:start+end])
		path = path[start+end+1:]
	}
}

// allParams is the declared parameters plus the generic paging arguments.
func (o mcpOperation) allParams() []mcpParam {
	params := append([]mcpParam(nil), o.Params...)
	zero := 0
	if o.ArrayKey != "" {
		pageMax := mcpMaxPageSize
		one := 1
		params = append(params,
			mcpParam{Name: "limit", Kind: mcpInteger, Min: &one, Max: &pageMax,
				Description: fmt.Sprintf("Page size for %s (default %d, max %d).", o.ArrayKey, mcpDefaultPageSize, mcpMaxPageSize)},
			mcpParam{Name: "offset", Kind: mcpInteger, Min: &zero, Description: "Number of " + o.ArrayKey + " entries to skip (default 0)."})
	}
	return append(params, mcpParam{Name: "byte_offset", Kind: mcpInteger, Min: &zero,
		Description: "Continue a result that was cut at the size limit: pass next_byte_offset from the previous truncated result."})
}

func (o mcpOperation) inputSchema() map[string]any {
	properties := map[string]any{}
	required := []string{}
	for _, param := range o.allParams() {
		property := map[string]any{"type": string(param.Kind), "description": param.Description}
		if len(param.Enum) > 0 {
			property["enum"] = param.Enum
		}
		if param.Min != nil {
			property["minimum"] = *param.Min
		}
		if param.Max != nil {
			property["maximum"] = *param.Max
		}
		properties[param.Name] = property
		if param.Required {
			required = append(required, param.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// mcpArgs are validated, normalized argument values (strings for path and
// query use, byte_offset/limit/offset parsed).
type mcpArgs struct {
	values     map[string]string
	byteOffset int
	limit      int
	offset     int
}

func (o mcpOperation) parseArgs(raw json.RawMessage) (mcpArgs, error) {
	input := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil {
			return mcpArgs{}, errors.New("arguments must be a JSON object")
		}
	}
	declared := map[string]mcpParam{}
	for _, param := range o.allParams() {
		declared[param.Name] = param
	}
	args := mcpArgs{values: map[string]string{}, limit: mcpDefaultPageSize}
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		param, known := declared[name]
		if !known {
			return mcpArgs{}, fmt.Errorf("unknown argument %q", name)
		}
		text, err := normalizeMCPValue(param, input[name])
		if err != nil {
			return mcpArgs{}, err
		}
		switch name {
		case "byte_offset":
			args.byteOffset, _ = strconv.Atoi(text)
		case "limit":
			if o.ArrayKey != "" {
				args.limit, _ = strconv.Atoi(text)
			} else {
				args.values[name] = text
			}
		case "offset":
			if o.ArrayKey != "" {
				args.offset, _ = strconv.Atoi(text)
			} else {
				args.values[name] = text
			}
		default:
			args.values[name] = text
		}
	}
	for _, param := range o.Params {
		if param.Required {
			if _, ok := args.values[param.Name]; !ok {
				return mcpArgs{}, fmt.Errorf("missing required argument %q", param.Name)
			}
		}
	}
	return args, nil
}

func normalizeMCPValue(param mcpParam, value any) (string, error) {
	var text string
	switch param.Kind {
	case mcpString:
		str, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("argument %q must be a string", param.Name)
		}
		text = strings.TrimSpace(str)
		if len(text) > mcpMaxStringArg || strings.ContainsAny(text, "\x00\r\n") || !utf8.ValidString(text) {
			return "", fmt.Errorf("argument %q is not a valid string", param.Name)
		}
		if len(param.Enum) > 0 {
			ok := false
			for _, allowed := range param.Enum {
				ok = ok || allowed == text
			}
			if !ok {
				return "", fmt.Errorf("argument %q must be one of %s", param.Name, strings.Join(param.Enum, ", "))
			}
		}
	case mcpInteger:
		number, ok := value.(json.Number)
		if !ok {
			return "", fmt.Errorf("argument %q must be an integer", param.Name)
		}
		n, err := strconv.ParseInt(number.String(), 10, 32)
		if err != nil {
			return "", fmt.Errorf("argument %q must be an integer", param.Name)
		}
		if param.Min != nil && n < int64(*param.Min) || param.Max != nil && n > int64(*param.Max) {
			return "", fmt.Errorf("argument %q is out of range", param.Name)
		}
		text = strconv.FormatInt(n, 10)
	case mcpBoolean:
		flag, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("argument %q must be a boolean", param.Name)
		}
		text = strconv.FormatBool(flag)
	}
	return text, nil
}

// restPath expands {placeholders}. Path parameters never reach the router
// unvalidated: a value may not contain "/" (unless Subpath, where every
// segment is checked), so a tool argument cannot select a different route.
func (o mcpOperation) restPath(values map[string]string) (string, error) {
	path := o.Path
	for _, name := range pathPlaceholders(o.Path) {
		value := values[name]
		if value == "" {
			return "", fmt.Errorf("missing required argument %q", name)
		}
		param := o.paramByName(name)
		if param.Subpath {
			for _, segment := range strings.Split(value, "/") {
				if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, `\`) {
					return "", fmt.Errorf("argument %q is not a valid path", name)
				}
			}
		} else if strings.ContainsAny(value, `/\`) || value == "." || value == ".." {
			return "", fmt.Errorf("argument %q is not a valid identifier", name)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", value)
	}
	return path, nil
}

func (o mcpOperation) paramByName(name string) mcpParam {
	for _, param := range o.Params {
		if param.Name == name {
			return param
		}
	}
	return mcpParam{}
}

func (o mcpOperation) query(values map[string]string) url.Values {
	query := url.Values{}
	placeholders := map[string]bool{}
	for _, name := range pathPlaceholders(o.Path) {
		placeholders[name] = true
	}
	for name, value := range values {
		if !placeholders[name] {
			query.Set(name, value)
		}
	}
	return query
}

// mcpOutcome is a REST response, or an operation-level failure, ready to be
// turned into a tool result or resource body.
type mcpOutcome struct {
	status int
	body   []byte
	json   bool
}

func (o mcpOperation) run(ctx context.Context, server *Server, identity mcpIdentity, raw json.RawMessage) (text string, errText string) {
	args, err := o.parseArgs(raw)
	if err != nil {
		return "", mcpErrorText(http.StatusBadRequest, "bad_arguments", err.Error())
	}
	outcome, err := o.call(ctx, server, identity, args)
	if err != nil {
		return "", mcpErrorText(http.StatusBadRequest, "bad_arguments", err.Error())
	}
	if outcome.status < 200 || outcome.status > 299 {
		return "", mcpRESTErrorText(outcome)
	}
	body := outcome.body
	if o.ArrayKey != "" && outcome.json {
		paged, pageErr := paginateMCPArray(body, o.ArrayKey, o.RootArray, args.limit, args.offset)
		if pageErr != nil {
			return "", mcpErrorText(http.StatusBadGateway, "bad_upstream_response", "response could not be paginated")
		}
		body = paged
	}
	result, sliceErr := mcpSlice(body, args.byteOffset)
	if sliceErr != nil {
		return "", mcpErrorText(http.StatusBadRequest, "bad_arguments", sliceErr.Error())
	}
	return result, ""
}

// call performs the internal REST request through the shared router.
func (o mcpOperation) call(ctx context.Context, server *Server, identity mcpIdentity, args mcpArgs) (mcpOutcome, error) {
	path, err := o.restPath(args.values)
	if err != nil {
		return mcpOutcome{}, err
	}
	return server.mcpDispatch(ctx, identity, o.Method, path, o.query(args.values)), nil
}

// mcpDispatch serves one internal request through the router under the MCP
// identity. The session travels in the request context (see requestSession),
// so every handler sees the same principal it would see for a cookie session.
func (s *Server) mcpDispatch(ctx context.Context, identity mcpIdentity, method, path string, query url.Values) mcpOutcome {
	target := &url.URL{Path: path, RawQuery: query.Encode()}
	// The tool context descends from the outer /mcp request, whose chi route
	// context would make the router treat this call as a mounted sub-request
	// and reuse the stale match. A typed nil route context starts routing over.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
	request, err := http.NewRequestWithContext(context.WithValue(ctx, mcpSessionContextKey{}, identity.Session), method, target.String(), nil)
	if err != nil {
		return mcpOutcome{status: http.StatusBadRequest, body: []byte("bad request")}
	}
	request.Host = identity.Host
	request.Header.Set("Accept", "application/json, text/plain")
	recorder := newAPICapturedResponse()
	s.router.ServeHTTP(recorder, request)
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	contentType := recorder.header.Get("Content-Type")
	isJSON := strings.HasPrefix(contentType, "application/json")
	body := recorder.body.Bytes()
	if isJSON {
		body = bytes.TrimRight(body, "\n") // the JSON encoder's trailing newline; text artifacts stay byte-exact
	}
	return mcpOutcome{status: status, body: body, json: isJSON}
}

func mcpRESTErrorText(outcome mcpOutcome) string {
	var parsed apiResponseError
	message, code := strings.TrimSpace(string(outcome.body)), apiErrorCode(outcome.status)
	if json.Unmarshal(outcome.body, &parsed) == nil && parsed.Error != "" {
		message = parsed.Error
		if parsed.Code != "" {
			code = parsed.Code
		}
	}
	if len(message) > 500 {
		message = message[:500]
	}
	if !utf8.ValidString(message) {
		message = "request failed"
	}
	return mcpErrorText(outcome.status, code, message)
}

func mcpErrorText(status int, code, message string) string {
	encoded, _ := json.Marshal(map[string]any{"error": message, "code": code, "status": status})
	return string(encoded)
}

// paginateMCPArray slices the named array of a JSON object (or a root array)
// and reports total/limit/offset/has_more/next_offset next to it.
func paginateMCPArray(body []byte, key string, root bool, limit, offset int) ([]byte, error) {
	var document map[string]json.RawMessage
	var entries []json.RawMessage
	if root {
		document = map[string]json.RawMessage{}
		if err := json.Unmarshal(body, &entries); err != nil {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(body, &document); err != nil {
			return nil, err
		}
		if raw, ok := document[key]; ok && string(raw) != "null" {
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, err
			}
		}
	}
	total := len(entries)
	if offset > total {
		offset = total
	}
	end := min(total, offset+limit)
	page := entries[offset:end]
	if page == nil {
		page = []json.RawMessage{}
	}
	encodedPage, _ := json.Marshal(page)
	document[key] = encodedPage
	set := func(name string, value any) {
		encoded, _ := json.Marshal(value)
		document[name] = encoded
	}
	set("total", total)
	set("limit", limit)
	set("offset", offset)
	set("has_more", end < total)
	if end < total {
		set("next_offset", end)
	} else {
		set("next_offset", nil)
	}
	return json.Marshal(document)
}

// mcpSlice returns body as is when it fits mcpMaxResultBytes and no
// byte_offset was requested. Otherwise it returns a JSON envelope with one
// UTF-8-safe slice and next_byte_offset for the continuation.
func mcpSlice(body []byte, offset int) (string, error) {
	if offset == 0 && len(body) <= mcpMaxResultBytes {
		if !utf8.Valid(body) {
			return "", errors.New("result is not valid UTF-8 text")
		}
		return string(body), nil
	}
	if offset > len(body) {
		return "", errors.New("byte_offset is beyond the end of the result")
	}
	start := offset
	for start < len(body) && !utf8.RuneStart(body[start]) {
		start++
	}
	end := min(len(body), start+mcpMaxResultBytes)
	for end < len(body) && !utf8.RuneStart(body[end]) {
		end--
	}
	if !utf8.Valid(body[start:end]) {
		return "", errors.New("result is not valid UTF-8 text")
	}
	envelope := map[string]any{
		"truncated": end < len(body), "total_bytes": len(body), "byte_offset": start, "content": string(body[start:end]),
	}
	if end < len(body) {
		envelope["next_byte_offset"] = end
	}
	encoded, _ := json.Marshal(envelope)
	return string(encoded), nil
}

// register installs every operation as an MCP tool and, where declared, as a
// resource (template) on the SDK server.
func (r *mcpRegistry) register(sdk *mcp.Server, server *Server) {
	for i := range r.operations {
		operation := r.operations[i]
		sdk.AddTool(&mcp.Tool{
			Name: operation.Name, Title: operation.Title, Description: operation.Description,
			InputSchema: operation.inputSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPointer(false)},
		}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			identity, ok := mcpIdentityFromContext(ctx)
			if !ok {
				return mcpToolError(mcpErrorText(http.StatusUnauthorized, "unauthorized", "authentication required")), nil
			}
			text, failure := operation.run(ctx, server, identity, request.Params.Arguments)
			if failure != "" {
				return mcpToolError(failure), nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
		})
		if operation.Resource != "" {
			r.registerResource(sdk, server, operation)
		}
	}
}

func mcpToolError(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func boolPointer(value bool) *bool { return &value }

func (r *mcpRegistry) registerResource(sdk *mcp.Server, server *Server, operation mcpOperation) {
	handler := func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		identity, ok := mcpIdentityFromContext(ctx)
		if !ok {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		values, matched := matchMCPResourceURI(operation.Resource, request.Params.URI)
		if !matched {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		args := mcpArgs{values: values, limit: mcpDefaultPageSize}
		for name, value := range values {
			if _, err := normalizeMCPValue(operation.paramByName(name), resourceValueAsJSON(operation.paramByName(name), value)); err != nil {
				return nil, mcp.ResourceNotFoundError(request.Params.URI)
			}
		}
		outcome, err := operation.call(ctx, server, identity, args)
		if err != nil || outcome.status < 200 || outcome.status > 299 {
			// Missing, forbidden and malformed all look the same: no leak.
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		text, sliceErr := mcpSlice(outcome.body, 0)
		if sliceErr != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		mime := "text/plain"
		if outcome.json {
			mime = "application/json"
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, MIMEType: mime, Text: text}}}, nil
	}
	if len(pathPlaceholders(operation.Resource)) == 0 {
		sdk.AddResource(&mcp.Resource{URI: operation.Resource, Name: operation.Name, Title: operation.Title,
			Description: operation.Description, MIMEType: "application/json"}, handler)
		return
	}
	sdk.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: operation.Resource, Name: operation.Name, Title: operation.Title,
		Description: operation.Description, MIMEType: "application/json"}, handler)
}

// matchMCPResourceURI matches uri against a template made of literal text and
// {name} segments ("aiteam://tasks/{run_id}/events"). A variable matches one
// non-empty path segment.
func matchMCPResourceURI(template, uri string) (map[string]string, bool) {
	templateParts, uriParts := strings.Split(template, "/"), strings.Split(uri, "/")
	if len(templateParts) != len(uriParts) {
		return nil, false
	}
	values := map[string]string{}
	for i, part := range templateParts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			value, err := url.PathUnescape(uriParts[i])
			if err != nil || value == "" {
				return nil, false
			}
			values[part[1:len(part)-1]] = value
			continue
		}
		if part != uriParts[i] {
			return nil, false
		}
	}
	return values, true
}

// resourceValueAsJSON lifts a URI segment to the JSON type a tool argument of
// that kind would carry, so resources and tools share one validation.
func resourceValueAsJSON(param mcpParam, value string) any {
	switch param.Kind {
	case mcpInteger:
		return json.Number(value)
	case mcpBoolean:
		return value == "true"
	default:
		return value
	}
}
