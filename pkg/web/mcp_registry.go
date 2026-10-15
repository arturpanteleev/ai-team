package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
// caller's role does not already carry. Reads (S12) and actions (S13) are both
// operations of this registry; later steps append operations, and the parity
// matrix (mcp_parity.go) fails its test until every REST route has a tool or
// a documented reason.
//
// Actions differ from reads in three transport-level ways only: the internal
// request carries the session's CSRF token (the Bearer token replaces the
// browser's CSRF for MCP), the arguments marked Body become the JSON body, and
// every mutation is deduplicated by a client-supplied idempotency_key and
// recorded as an MCP-channel audit event.

const (
	// mcpMaxResultBytes bounds one tool result or resource body. Larger bodies
	// are returned in byte_offset slices (see mcpSlice).
	mcpMaxResultBytes = 256 << 10
	// mcpDefaultPageSize / mcpMaxPageSize bound locally paginated arrays.
	mcpDefaultPageSize = 50
	mcpMaxPageSize     = 200
	mcpMaxStringArg    = 4096
	// mcpMaxLongStringArg bounds a Body parameter marked Long (stage result
	// text, template YAML, artifact content).
	mcpMaxLongStringArg = 3 << 20
	mcpMaxArrayItems    = 64
	mcpIdempotencyKey   = "idempotency_key"
)

type mcpParamKind string

const (
	mcpString  mcpParamKind = "string"
	mcpInteger mcpParamKind = "integer"
	mcpBoolean mcpParamKind = "boolean"
	// mcpStringArray (JSON array of strings) and mcpStringMap (JSON object with
	// string values) are valid for Body parameters only.
	mcpStringArray mcpParamKind = "array"
	mcpStringMap   mcpParamKind = "object"
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
	// Body marks an argument that is sent in the JSON request body under its
	// own name instead of the query string. String values are passed through
	// untouched (no trimming), so submitted text keeps its exact bytes.
	Body bool
	// Long raises the string limit from 4 KiB to 3 MiB (Body only).
	Long bool
	// Digest requires a 64-character SHA-256 hex string (lower-cased).
	Digest bool
}

// mcpOperation is one product operation exposed over MCP.
type mcpOperation struct {
	Name        string
	Title       string
	Description string
	Method      string // GET (read) or POST/PUT/PATCH/DELETE (action).
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
	// ReadOnly marks a POST that changes nothing (template validation): it is
	// annotated read-only and does not need an idempotency_key.
	ReadOnly bool
	// Destructive marks an action that removes or cancels something.
	Destructive bool
	// Redact names top-level response fields that are secrets shown once (an
	// activation token). They are replaced in the copy kept for replays, so a
	// repeated delivery gets the outcome without the secret being stored.
	Redact []string
}

// mutating reports whether the operation changes server state.
func (o mcpOperation) mutating() bool { return o.Method != http.MethodGet && !o.ReadOnly }

// keyRequired: POST actions are not naturally idempotent, so the client must
// say which logical request a delivery belongs to. PUT, PATCH and DELETE
// replace or remove one identified thing; the key is optional for them.
func (o mcpOperation) keyRequired() bool { return o.mutating() && o.Method == http.MethodPost }

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
	switch operation.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		panic("mcp: operation " + operation.Name + ": unsupported method " + operation.Method)
	}
	if operation.Method == http.MethodGet && (operation.ReadOnly || operation.Destructive || operation.hasBodyParam()) {
		panic("mcp: operation " + operation.Name + ": a GET has no body and no side effects to flag")
	}
	if operation.Method != http.MethodGet && (operation.ArrayKey != "" || operation.Resource != "") {
		panic("mcp: operation " + operation.Name + ": actions have no pagination or resource form")
	}
	if operation.hasParam(mcpIdempotencyKey) {
		panic("mcp: operation " + operation.Name + ": " + mcpIdempotencyKey + " is reserved")
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

func (o mcpOperation) hasBodyParam() bool {
	for _, param := range o.Params {
		if param.Body {
			return true
		}
	}
	return false
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
	if o.Method != http.MethodGet {
		return append(params, mcpParam{Name: mcpIdempotencyKey, Kind: mcpString, Required: o.keyRequired(),
			Description: "Request key (1-128 visible characters) naming this logical request. Resending the same call with the same key " +
				"returns the first result and never repeats the action; the same key with different arguments is refused. " +
				"Use a fresh key (for example a UUID) for each new action."})
	}
	return append(params, mcpParam{Name: "byte_offset", Kind: mcpInteger, Min: &zero,
		Description: "Continue a result that was cut at the size limit: pass next_byte_offset from the previous truncated result."})
}

func (o mcpOperation) inputSchema() map[string]any {
	properties := map[string]any{}
	required := []string{}
	for _, param := range o.allParams() {
		property := map[string]any{"type": string(param.Kind), "description": param.Description}
		switch param.Kind {
		case mcpStringArray:
			property["items"] = map[string]any{"type": "string"}
		case mcpStringMap:
			property["additionalProperties"] = map[string]any{"type": "string"}
		}
		if param.Digest {
			property["pattern"] = "^[0-9a-fA-F]{64}$"
		}
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
	body       map[string]any // JSON body of an action
	key        string         // idempotency_key
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
	args := mcpArgs{values: map[string]string{}, body: map[string]any{}, limit: mcpDefaultPageSize}
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
		if name == mcpIdempotencyKey {
			key, keyErr := normalizeMCPKey(input[name])
			if keyErr != nil {
				return mcpArgs{}, keyErr
			}
			args.key = key
			continue
		}
		if param.Body {
			value, bodyErr := normalizeMCPBodyValue(param, input[name])
			if bodyErr != nil {
				return mcpArgs{}, bodyErr
			}
			args.body[name] = value
			continue
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
		if !param.Required {
			continue
		}
		_, inValues := args.values[param.Name]
		_, inBody := args.body[param.Name]
		if !inValues && !inBody {
			return mcpArgs{}, fmt.Errorf("missing required argument %q", param.Name)
		}
	}
	if o.keyRequired() && args.key == "" {
		return mcpArgs{}, fmt.Errorf("missing required argument %q: name this request so a repeated delivery cannot repeat the action", mcpIdempotencyKey)
	}
	return args, nil
}

func normalizeMCPKey(value any) (string, error) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	if !ok || text == "" || len(text) > 128 || !utf8.ValidString(text) {
		return "", fmt.Errorf("argument %q must be 1-128 visible characters", mcpIdempotencyKey)
	}
	for _, r := range text {
		if r < 0x21 || r == 0x7f {
			return "", fmt.Errorf("argument %q must be 1-128 visible characters", mcpIdempotencyKey)
		}
	}
	return text, nil
}

// normalizeMCPBodyValue validates one JSON body argument and returns the value
// to marshal. Unlike path and query values, text is not trimmed and may span
// lines: the REST handler decides what is acceptable and refuses the rest.
func normalizeMCPBodyValue(param mcpParam, value any) (any, error) {
	badType := func(want string) error { return fmt.Errorf("argument %q must be %s", param.Name, want) }
	switch param.Kind {
	case mcpString:
		text, ok := value.(string)
		if !ok {
			return nil, badType("a string")
		}
		limit := mcpMaxStringArg
		if param.Long {
			limit = mcpMaxLongStringArg
		}
		if len(text) > limit || strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
			return nil, fmt.Errorf("argument %q is not a valid string (at most %d bytes)", param.Name, limit)
		}
		if len(param.Enum) > 0 && !containsString(param.Enum, text) {
			return nil, fmt.Errorf("argument %q must be one of %s", param.Name, strings.Join(param.Enum, ", "))
		}
		if param.Digest {
			text = strings.ToLower(strings.TrimSpace(text))
			if decoded, err := hex.DecodeString(text); err != nil || len(decoded) != 32 {
				return nil, fmt.Errorf("argument %q must be a SHA-256 digest (64 hex characters)", param.Name)
			}
		}
		return text, nil
	case mcpInteger:
		number, ok := value.(json.Number)
		if !ok {
			return nil, badType("an integer")
		}
		n, err := strconv.ParseInt(number.String(), 10, 32)
		if err != nil {
			return nil, badType("an integer")
		}
		if param.Min != nil && n < int64(*param.Min) || param.Max != nil && n > int64(*param.Max) {
			return nil, fmt.Errorf("argument %q is out of range", param.Name)
		}
		return n, nil
	case mcpBoolean:
		flag, ok := value.(bool)
		if !ok {
			return nil, badType("a boolean")
		}
		return flag, nil
	case mcpStringArray:
		items, ok := value.([]any)
		if !ok || len(items) > mcpMaxArrayItems {
			return nil, fmt.Errorf("argument %q must be an array of at most %d strings", param.Name, mcpMaxArrayItems)
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok || len(text) > mcpMaxStringArg || strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
				return nil, fmt.Errorf("argument %q must contain only valid strings", param.Name)
			}
			out = append(out, text)
		}
		return out, nil
	case mcpStringMap:
		entries, ok := value.(map[string]any)
		if !ok || len(entries) > mcpMaxArrayItems {
			return nil, fmt.Errorf("argument %q must be an object of at most %d string values", param.Name, mcpMaxArrayItems)
		}
		out := make(map[string]string, len(entries))
		for key, item := range entries {
			text, ok := item.(string)
			if !ok || len(key) > mcpMaxStringArg || len(text) > mcpMaxStringArg || !utf8.ValidString(key) || !utf8.ValidString(text) {
				return nil, fmt.Errorf("argument %q must contain only valid strings", param.Name)
			}
			out[key] = text
		}
		return out, nil
	}
	return nil, fmt.Errorf("argument %q has an unsupported kind", param.Name)
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

// mcpResult is what one tool call produced: the REST body (or the error
// JSON), and whether it is a replay of an earlier delivery of the same request.
type mcpResult struct {
	Text     string
	Err      string
	Replayed bool
}

func (o mcpOperation) run(ctx context.Context, server *Server, identity mcpIdentity, raw json.RawMessage) mcpResult {
	args, err := o.parseArgs(raw)
	if err != nil {
		return mcpResult{Err: mcpErrorText(http.StatusBadRequest, "bad_arguments", err.Error())}
	}
	if o.Method != http.MethodGet {
		return o.runAction(ctx, server, identity, args)
	}
	outcome, err := o.call(ctx, server, identity, args)
	if err != nil {
		return mcpResult{Err: mcpErrorText(http.StatusBadRequest, "bad_arguments", err.Error())}
	}
	return o.finish(outcome, args)
}

// finish turns a REST outcome into a tool result: errors keep their REST
// status and code, successful lists are paginated, big bodies are sliced.
func (o mcpOperation) finish(outcome mcpOutcome, args mcpArgs) mcpResult {
	if outcome.status < 200 || outcome.status > 299 {
		return mcpResult{Err: mcpRESTErrorText(outcome)}
	}
	body := outcome.body
	if o.ArrayKey != "" && outcome.json {
		paged, pageErr := paginateMCPArray(body, o.ArrayKey, o.RootArray, args.limit, args.offset)
		if pageErr != nil {
			return mcpResult{Err: mcpErrorText(http.StatusBadGateway, "bad_upstream_response", "response could not be paginated")}
		}
		body = paged
	}
	result, sliceErr := mcpSlice(body, args.byteOffset)
	if sliceErr != nil {
		return mcpResult{Err: mcpErrorText(http.StatusBadRequest, "bad_arguments", sliceErr.Error())}
	}
	return mcpResult{Text: result}
}

// runAction performs one action through the router. With an idempotency_key
// the first delivery claims the key, runs the action and stores its response;
// a repeated delivery gets the stored response without touching the router, so
// no second decision, run or delivery can result. The key is scoped to the
// authenticated person and the call (tool and arguments), never to a request
// field that names somebody else.
func (o mcpOperation) runAction(ctx context.Context, server *Server, identity mcpIdentity, args mcpArgs) mcpResult {
	var body []byte
	if o.hasBodyParam() {
		// An action that takes a body always sends a JSON object, even when
		// every optional field is absent: the REST handlers decode strictly
		// and reject an empty body, as a browser always sends {}.
		encoded, err := json.Marshal(args.body)
		if err != nil {
			return mcpResult{Err: mcpErrorText(http.StatusBadRequest, "bad_arguments", "arguments could not be encoded")}
		}
		body = encoded
	}
	path, err := o.restPath(args.values)
	if err != nil {
		return mcpResult{Err: mcpErrorText(http.StatusBadRequest, "bad_arguments", err.Error())}
	}
	query := o.query(args.values)
	actor := identity.Session.Principal.ActorID
	scope, fingerprint := "mcp:"+actor, ""
	if args.key != "" {
		digest := sha256.Sum256([]byte(o.Name + "\x00" + o.Method + "\x00" + path + "?" + query.Encode() + "\x00" + string(body)))
		fingerprint = hex.EncodeToString(digest[:])
		stored, claimed, claimErr := server.store.BeginIdempotency(scope, args.key, fingerprint)
		if claimErr != nil {
			return mcpResult{Err: mcpErrorText(http.StatusServiceUnavailable, "idempotency_unavailable", "idempotency store unavailable")}
		}
		if !claimed {
			switch {
			case stored.Conflict:
				return mcpResult{Err: mcpErrorText(http.StatusConflict, "idempotency_conflict", "idempotency_key was already used for a different request; use a new key")}
			case stored.InProgress:
				return mcpResult{Err: mcpErrorText(http.StatusConflict, "idempotency_in_progress", "a request with this idempotency_key is still in progress")}
			}
			replay := o.finish(mcpOutcome{status: stored.StatusCode, body: stored.Body, json: json.Valid(stored.Body)}, args)
			replay.Replayed = true
			return replay
		}
	}
	meta := &mcpCallMeta{Tool: o.Name, Key: args.key, TokenID: identity.TokenID, UserAgent: identity.UserAgent}
	outcome := server.mcpDispatch(ctx, identity, o.Method, path, query, body, meta)
	if args.key != "" {
		// Every response after the dispatch is stored, a 5xx included, exactly
		// like the REST policy: the action may already have had durable side
		// effects, so a retry with the same key replays the answer instead of
		// doing it twice. Only failures before the dispatch release the key.
		if err := server.store.CompleteIdempotency(scope, args.key, fingerprint, outcome.status, nil, redactMCPFields(outcome.body, o.Redact)); err != nil {
			return mcpResult{Err: mcpErrorText(http.StatusServiceUnavailable, "idempotency_unavailable", "response could not be recorded for retry")}
		}
	}
	if o.mutating() && (outcome.status >= 200 && outcome.status <= 299 || outcome.status == http.StatusUnauthorized || outcome.status == http.StatusForbidden) {
		// Successes and authorization denials are journaled; the record never
		// carries arguments and a failed write never breaks the call.
		server.auditMCPAction(identity, o, args, outcome)
	}
	return o.finish(outcome, args)
}

// redactMCPFields replaces the named top-level fields of a JSON object.
func redactMCPFields(body []byte, fields []string) []byte {
	if len(fields) == 0 {
		return body
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(body, &document) != nil {
		return body
	}
	for _, field := range fields {
		if _, ok := document[field]; ok {
			document[field] = json.RawMessage(`"[shown once by the first delivery; not kept for replays]"`)
		}
	}
	redacted, err := json.Marshal(document)
	if err != nil {
		return body
	}
	return redacted
}

// call performs the internal REST request of a read through the shared router.
func (o mcpOperation) call(ctx context.Context, server *Server, identity mcpIdentity, args mcpArgs) (mcpOutcome, error) {
	path, err := o.restPath(args.values)
	if err != nil {
		return mcpOutcome{}, err
	}
	return server.mcpDispatch(ctx, identity, o.Method, path, o.query(args.values), nil, nil), nil
}

// mcpCallMeta describes the MCP call an internal request belongs to. It travels
// in the request context (unforgeable from the network, like the session) so
// the shared handlers can record the channel and the request key.
type mcpCallMeta struct {
	Tool      string
	Key       string
	TokenID   string
	UserAgent string
}

type mcpCallMetaContextKey struct{}

// mcpCallMetaFromRequest returns the MCP call metadata of a request that was
// dispatched from the MCP server, and false for a browser or REST request.
func mcpCallMetaFromRequest(r *http.Request) (*mcpCallMeta, bool) {
	meta, ok := r.Context().Value(mcpCallMetaContextKey{}).(*mcpCallMeta)
	return meta, ok && meta != nil
}

// mcpDispatch serves one internal request through the router under the MCP
// identity. The session travels in the request context (see requestSession),
// so every handler sees the same principal it would see for a cookie session.
// For an action the session's own CSRF token is sent: the Bearer token that
// authenticated the MCP call is what replaces the browser's CSRF check, and the
// router still verifies it like any other write.
func (s *Server) mcpDispatch(ctx context.Context, identity mcpIdentity, method, path string, query url.Values, body []byte, meta *mcpCallMeta) mcpOutcome {
	target := &url.URL{Path: path, RawQuery: query.Encode()}
	// The tool context descends from the outer /mcp request, whose chi route
	// context would make the router treat this call as a mounted sub-request
	// and reuse the stale match. A typed nil route context starts routing over.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
	ctx = context.WithValue(ctx, mcpSessionContextKey{}, identity.Session)
	if meta != nil {
		ctx = context.WithValue(ctx, mcpCallMetaContextKey{}, meta)
	}
	var reader *bytes.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return mcpOutcome{status: http.StatusBadRequest, body: []byte("bad request")}
	}
	request.Host = identity.Host
	request.Header.Set("Accept", "application/json, text/plain")
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", identity.Session.CSRFToken)
		if len(body) > 0 {
			request.Header.Set("Content-Type", "application/json")
		}
	}
	recorder := newAPICapturedResponse()
	s.router.ServeHTTP(recorder, request)
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	contentType := recorder.header.Get("Content-Type")
	isJSON := strings.HasPrefix(contentType, "application/json")
	responseBody := recorder.body.Bytes()
	if isJSON {
		responseBody = bytes.TrimRight(responseBody, "\n") // the JSON encoder's trailing newline; text artifacts stay byte-exact
	}
	return mcpOutcome{status: status, body: responseBody, json: isJSON}
}

// auditMCPAction records that a successful action arrived through MCP: who,
// which tool, which token (client) and request key. Arguments are never
// recorded (they may hold submitted results). A task action becomes a run
// event next to the stage events it caused; an action without a run goes to
// the team audit. Decisions additionally carry channel "mcp" in their own
// audit record (humandecision).
func (s *Server) auditMCPAction(identity mcpIdentity, o mcpOperation, args mcpArgs, outcome mcpOutcome) {
	now := s.clock()
	data := map[string]any{
		"actor_id": identity.Session.Principal.ActorID, "channel": humandecisionChannelMCP, "tool": o.Name,
		"method": o.Method, "status": outcome.status,
	}
	if outcome.status == http.StatusUnauthorized || outcome.status == http.StatusForbidden {
		data["denied"] = true
	}
	if identity.TokenID != "" {
		data["token_id"] = identity.TokenID
	}
	if identity.UserAgent != "" {
		data["client"] = identity.UserAgent
	}
	if args.key != "" {
		data["idempotency_key"] = args.key
	}
	runID := args.values["run_id"]
	if runID == "" && outcome.json {
		var created struct {
			RunID string `json:"run_id"`
		}
		if json.Unmarshal(outcome.body, &created) == nil {
			runID = created.RunID
		}
	}
	if runID != "" {
		s.appendRunEvent(runID, mcpActionEvent, now, data)
		return
	}
	if err := s.store.AppendAudit(identity.Session.Principal.ActorID, mcpActionEvent, "", data, now); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ mcp audit: %v\n", err)
	}
}

const (
	// mcpActionEvent is the audit record every successful MCP action leaves.
	mcpActionEvent = "mcp_action"
	// humandecisionChannelMCP equals humandecision.ChannelMCP; spelled out so
	// the audit above does not depend on a decision being involved.
	humandecisionChannelMCP = "mcp"
)

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
			Annotations: operation.annotations(),
		}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			identity, ok := mcpIdentityFromContext(ctx)
			if !ok {
				return mcpToolError(mcpErrorText(http.StatusUnauthorized, "unauthorized", "authentication required")), nil
			}
			result := operation.run(ctx, server, identity, request.Params.Arguments)
			if result.Err != "" {
				return mcpToolError(result.Err), nil
			}
			response := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: result.Text}}}
			if result.Replayed {
				response.Meta = mcp.Meta{"idempotent_replay": true}
			}
			return response, nil
		})
		if operation.Resource != "" {
			r.registerResource(sdk, server, operation)
		}
	}
}

// annotations tell a client what a tool may do. Reads and validations are
// read-only; every action is idempotent when called with a request key and is
// flagged destructive only when it cancels or removes something.
func (o mcpOperation) annotations() *mcp.ToolAnnotations {
	if !o.mutating() {
		return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPointer(false)}
	}
	return &mcp.ToolAnnotations{DestructiveHint: boolPointer(o.Destructive), IdempotentHint: true, OpenWorldHint: boolPointer(false)}
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
