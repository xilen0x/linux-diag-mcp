// Package guard is the cross-cutting policy applied to every tools/call:
// a timeout that holds even if a tool ignores its context, panic recovery,
// error messages that never reflect client input, and a size limit on
// results that keeps them valid against the tool's output schema.
package guard

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Constant messages returned instead of anything derived from the request.
const (
	MsgInvalidArguments = "invalid arguments: they do not match the tool's input schema"
	MsgInternal         = "internal error"
	MsgUnknownTool      = "unknown tool or malformed request"
	MsgTooLarge         = "result too large"
	MsgCanceled         = "request canceled"
)

// Meta keys set on truncated results.
const (
	MetaTruncated     = "linux-diag-mcp/truncated"
	MetaOriginalBytes = "linux-diag-mcp/original_bytes"
)

// maxErrorRunes bounds error texts shown to the client.
const maxErrorRunes = 512

// Limits are applied to every tool call.
type Limits struct {
	MaxOutputBytes int
	Timeout        time.Duration
}

// Error marks an error produced by our own tool code, whose message is safe
// to show (after sanitizing). Any other error reaching the client, such as
// the SDK's schema validation errors which quote the offending input, is
// replaced by a constant message.
type Error struct{ err error }

func (e *Error) Error() string { return e.err.Error() }
func (e *Error) Unwrap() error { return e.err }

// Wrap marks err as safe to show. Wrap(nil) is nil.
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	return &Error{err: err}
}

type outcome struct {
	res mcp.Result
	err error
	// final results were built by the guard itself and are returned as is.
	final bool
}

// Middleware enforces l on tools/call and passes other methods through.
func Middleware(l Limits, logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			name := ""
			if r, ok := req.(*mcp.CallToolRequest); ok && r.Params != nil {
				name = r.Params.Name
			}
			log := logger.With("tool", name) // slog quotes hostile values

			ctx, cancel := context.WithTimeout(ctx, l.Timeout)
			defer cancel()
			done := make(chan outcome, 1) // buffered: an abandoned call never blocks
			go func() {
				defer func() {
					if p := recover(); p != nil {
						log.Error("tool panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
						done <- outcome{res: errorResult(MsgInternal), final: true}
					}
				}()
				res, err := next(ctx, method, req)
				done <- outcome{res: res, err: err}
			}()

			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					log.Warn("tool timed out", "timeout", l.Timeout)
					return errorResult(fmt.Sprintf("tool timed out after %s", l.Timeout)), nil
				}
				return errorResult(MsgCanceled), nil
			case o := <-done:
				if o.final {
					return o.res, nil
				}
				return finish(o, l, log)
			}
		}
	}
}

func finish(o outcome, l Limits, log *slog.Logger) (mcp.Result, error) {
	if o.err != nil {
		log.Warn("tool call rejected", "err", o.err)
		var rpcErr *jsonrpc.Error
		if errors.As(o.err, &rpcErr) && rpcErr.Code == jsonrpc.CodeInvalidParams {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: MsgUnknownTool}
		}
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: MsgInternal}
	}
	res, ok := o.res.(*mcp.CallToolResult)
	if !ok || res == nil {
		return o.res, nil
	}
	if res.IsError {
		err := res.GetError()
		var safe *Error
		if errors.As(err, &safe) {
			return errorResult(clean(safe.Error())), nil
		}
		log.Warn("tool call failed", "err", err)
		return errorResult(MsgInvalidArguments), nil
	}
	return limit(res, l.MaxOutputBytes, log), nil
}

// limit enforces the size cap on the structured result. Arrays are the
// only parts that can be shortened without breaking the output schema, so
// the longest array is cut repeatedly until the result fits.
func limit(res *mcp.CallToolResult, maxBytes int, log *slog.Logger) *mcp.CallToolResult {
	if res.StructuredContent == nil {
		return res
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		log.Error("encode result", "err", err)
		return errorResult(MsgInternal)
	}
	if len(raw) <= maxBytes {
		return res
	}
	original := len(raw)

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep uint64 values exact
	var v any
	if err := dec.Decode(&v); err != nil {
		log.Error("decode result", "err", err)
		return errorResult(MsgInternal)
	}
	for len(raw) > maxBytes {
		arr, ok := longestArray(v)
		if !ok {
			log.Warn("result too large", "bytes", original)
			return errorResult(MsgTooLarge)
		}
		n := len(arr.items)
		keep := min(n*maxBytes/len(raw), n-1) // proportional cut, always progress
		arr.set(arr.items[:keep])
		if raw, err = json.Marshal(v); err != nil {
			log.Error("encode result", "err", err)
			return errorResult(MsgInternal)
		}
	}
	log.Info("result truncated", "original_bytes", original, "bytes", len(raw))
	meta := mcp.Meta{}
	for k, val := range res.Meta {
		meta[k] = val
	}
	meta[MetaTruncated] = true
	meta[MetaOriginalBytes] = original
	return &mcp.CallToolResult{
		Meta:              meta,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		StructuredContent: v,
	}
}

type arrayRef struct {
	items []any
	set   func([]any)
}

// longestArray finds the non-empty array with the most elements anywhere in
// v (object keys visited in sorted order, so ties resolve deterministically).
func longestArray(v any) (arrayRef, bool) {
	var best arrayRef
	var walk func(v any)
	consider := func(items []any, set func([]any)) {
		if len(items) > len(best.items) {
			best = arrayRef{items: items, set: set}
		}
	}
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			slices.SortFunc(keys, cmp.Compare[string])
			for _, k := range keys {
				if a, ok := x[k].([]any); ok {
					consider(a, func(n []any) { x[k] = n })
				}
				walk(x[k])
			}
		case []any:
			for i, c := range x {
				if a, ok := c.([]any); ok {
					consider(a, func(n []any) { x[i] = n })
				}
				walk(c)
			}
		}
	}
	walk(v)
	return best, len(best.items) > 0
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// clean drops control/format characters and invalid UTF-8 and caps length.
func clean(s string) string {
	var b strings.Builder
	n := 0
	for len(s) > 0 && n < maxErrorRunes {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if (r == utf8.RuneError && size <= 1) || !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
