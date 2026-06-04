// Package filter applies user-defined expr-lang expressions to decide whether
// a parsed log record should be kept or dropped before downstream processing.
//
// Two rule lists are supported:
//
//   - Include: if non-empty, a record is kept only when at least one include
//     expression evaluates to true (OR semantics). An empty list lets every
//     record through this stage.
//   - Exclude: a record is dropped if any exclude expression evaluates to true.
//
// Include is applied first, exclude second. Expressions are compiled once at
// Filter construction; evaluation is type-asserted to bool via expr.AsBool().
//
// # Hash-prefixed field access
//
// ThinkingData log records contain system fields with a leading '#' (e.g.
// #type, #event_name). Because '#' is not a valid identifier character in
// expr-lang, references like `#type == "track"` are transparently rewritten
// to `$env["#type"] == "track"` before compilation. Hash references inside
// string literals are left alone.
package filter

import (
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"

	"rocket-nano/tools/tango/internal/logging"
)

// Filter holds compiled include/exclude programs. A nil *Filter keeps every
// record (no-op), which is convenient for callers that always have a filter
// reference regardless of whether rules are configured.
type Filter struct {
	include []*vm.Program
	exclude []*vm.Program
}

// New compiles the include and exclude expression lists. An empty or nil list
// is allowed for either side. Returns a non-nil *Filter whose Keep method is
// a no-op when both lists are empty.
//
// Each expression must evaluate to a boolean. Compilation failures are
// returned with the offending expression index and source for easy debugging.
func New(include, exclude []string) (*Filter, error) {
	inc, err := compileAll(include, "include")
	if err != nil {
		return nil, err
	}
	exc, err := compileAll(exclude, "exclude")
	if err != nil {
		return nil, err
	}
	if len(inc) > 0 || len(exc) > 0 {
		logging.WithFields(logging.Fields{"include": len(inc), "exclude": len(exc)}).
			Debug("filter: compiled reporting rules")
	}
	return &Filter{include: inc, exclude: exc}, nil
}

// Keep returns whether the record should be kept. Evaluation errors are
// returned alongside the decision so the caller can record them; on error the
// expression is treated as not-matched (conservative: include miss / exclude
// miss). A nil *Filter keeps every record.
func (f *Filter) Keep(env map[string]any) (bool, error) {
	if f == nil {
		return true, nil
	}

	var firstErr error

	if len(f.include) > 0 {
		matched := false
		for _, p := range f.include {
			ok, err := evalBool(p, env)
			if err != nil && firstErr == nil {
				firstErr = err
				continue
			}
			if ok {
				matched = true
				break
			}
		}
		if !matched {
			return false, firstErr
		}
	}

	for _, p := range f.exclude {
		ok, err := evalBool(p, env)
		if err != nil && firstErr == nil {
			firstErr = err
			continue
		}
		if ok {
			return false, firstErr
		}
	}

	return true, firstErr
}

// Empty reports whether the filter has no rules (no-op).
func (f *Filter) Empty() bool {
	return f == nil || (len(f.include) == 0 && len(f.exclude) == 0)
}

func compileAll(exprs []string, kind string) ([]*vm.Program, error) {
	if len(exprs) == 0 {
		return nil, nil
	}
	out := make([]*vm.Program, 0, len(exprs))
	for i, src := range exprs {
		rewritten := rewriteHashRefs(src)
		p, err := expr.Compile(rewritten,
			expr.AsBool(),
			expr.AllowUndefinedVariables(),
		)
		if err != nil {
			return nil, fmt.Errorf("filter: compile %s[%d] %q: %w", kind, i, src, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// rewriteHashRefs rewrites every `#identifier` token that appears outside of
// a string literal into `$env["#identifier"]`, so expr-lang can compile it.
// Supports double-quoted, single-quoted, and backtick-quoted string literals,
// honouring backslash escapes inside double/single quotes.
func rewriteHashRefs(src string) string {
	var sb strings.Builder
	sb.Grow(len(src) + 16)

	inStr := false
	var quote byte
	i := 0
	for i < len(src) {
		c := src[i]
		if inStr {
			sb.WriteByte(c)
			if (quote == '"' || quote == '\'') && c == '\\' && i+1 < len(src) {
				sb.WriteByte(src[i+1])
				i += 2
				continue
			}
			if c == quote {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			inStr = true
			quote = c
			sb.WriteByte(c)
			i++
			continue
		}
		if c == '#' {
			j := i + 1
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			if j > i+1 && isIdentStart(src[i+1]) {
				sb.WriteString(`$env["`)
				sb.WriteString(src[i:j])
				sb.WriteString(`"]`)
				i = j
				continue
			}
		}
		sb.WriteByte(c)
		i++
	}
	return sb.String()
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func evalBool(p *vm.Program, env map[string]any) (bool, error) {
	out, err := expr.Run(p, env)
	if err != nil {
		return false, err
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("filter: expression did not return bool, got %T", out)
	}
	return b, nil
}
