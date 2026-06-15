package filter

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// CompileToSQL translates the include/exclude expression lists into a Presto
// WHERE clause body (without the leading "WHERE"). It is used by the backfill
// domain to push the selection filter down to the ThinkingData OpenAPI rather
// than only enforcing it in-process. The returned SQL string has the shape:
//
//	(<include1> OR <include2> OR ...) AND NOT (<exclude1> OR <exclude2> OR ...)
//
// Either side may be empty:
//   - Empty include list  → omit the include conjunct (all rows pass).
//   - Empty exclude list  → omit the exclude conjunct.
//   - Both empty          → returns ("", nil).
//
// Only a subset of expr-lang is supported for pushdown:
//
//   - Identifiers (including hash-prefixed via the same rewrite the local
//     filter uses) become double-quoted column references: "#type", "level".
//   - Binary operators: ==, !=, <, <=, >, >=, &&/and, ||/or, in.
//   - Unary operators:  !/not, -, +.
//   - Literals: int, float, bool, string (single-quoted, embedded quotes
//     doubled), nil.
//   - Array literals (only as the right-hand side of "in").
//
// Unsupported nodes (function calls, ternary, member access other than the
// hash-rewrite pattern, etc.) cause a compile error identifying the offending
// source fragment, so the caller can rewrite the expression to a pushdown-safe
// form or accept that the local filter alone will enforce it.
func CompileToSQL(include, exclude []string) (string, error) {
	inc, err := compileGroupToSQL(include, "include")
	if err != nil {
		return "", err
	}
	exc, err := compileGroupToSQL(exclude, "exclude")
	if err != nil {
		return "", err
	}

	var parts []string
	if inc != "" {
		parts = append(parts, inc)
	}
	if exc != "" {
		parts = append(parts, "NOT ("+exc+")")
	}
	return strings.Join(parts, " AND "), nil
}

func compileGroupToSQL(exprs []string, kind string) (string, error) {
	if len(exprs) == 0 {
		return "", nil
	}
	pieces := make([]string, 0, len(exprs))
	for i, src := range exprs {
		sql, err := compileOneToSQL(src)
		if err != nil {
			return "", fmt.Errorf("filter: SQL pushdown %s[%d] %q: %w", kind, i, src, err)
		}
		pieces = append(pieces, "("+sql+")")
	}
	if len(pieces) == 1 {
		return pieces[0], nil
	}
	return "(" + strings.Join(pieces, " OR ") + ")", nil
}

func compileOneToSQL(src string) (string, error) {
	rewritten := rewriteHashRefs(src)
	tree, err := parser.Parse(rewritten)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	return emitSQL(tree.Node)
}

func emitSQL(n ast.Node) (string, error) {
	switch v := n.(type) {
	case *ast.BinaryNode:
		return emitBinary(v)
	case *ast.UnaryNode:
		return emitUnary(v)
	case *ast.IdentifierNode:
		return quoteIdent(v.Value), nil
	case *ast.MemberNode:
		return emitMember(v)
	case *ast.StringNode:
		return quoteString(v.Value), nil
	case *ast.IntegerNode:
		return strconv.FormatInt(int64(v.Value), 10), nil
	case *ast.FloatNode:
		return strconv.FormatFloat(v.Value, 'g', -1, 64), nil
	case *ast.BoolNode:
		if v.Value {
			return "TRUE", nil
		}
		return "FALSE", nil
	case *ast.NilNode:
		return "NULL", nil
	case *ast.ArrayNode:
		return emitArray(v)
	default:
		return "", fmt.Errorf("unsupported node %T", n)
	}
}

func emitBinary(v *ast.BinaryNode) (string, error) {
	op := strings.ToLower(v.Operator)

	// Handle "in" specially: RHS must be an array of literals.
	if op == "in" {
		left, err := emitSQL(v.Left)
		if err != nil {
			return "", err
		}
		arr, ok := v.Right.(*ast.ArrayNode)
		if !ok {
			return "", fmt.Errorf("'in' operator requires an array literal on the right, got %T", v.Right)
		}
		right, err := emitArray(arr)
		if err != nil {
			return "", err
		}
		return left + " IN " + right, nil
	}

	sqlOp, ok := binaryOpMap[op]
	if !ok {
		return "", fmt.Errorf("unsupported binary operator %q", v.Operator)
	}

	left, err := emitSQL(v.Left)
	if err != nil {
		return "", err
	}
	right, err := emitSQL(v.Right)
	if err != nil {
		return "", err
	}

	// Logical operators get extra parentheses for readability and precedence.
	if op == "&&" || op == "and" || op == "||" || op == "or" {
		return "(" + left + " " + sqlOp + " " + right + ")", nil
	}
	return left + " " + sqlOp + " " + right, nil
}

func emitUnary(v *ast.UnaryNode) (string, error) {
	op := strings.ToLower(v.Operator)
	inner, err := emitSQL(v.Node)
	if err != nil {
		return "", err
	}
	switch op {
	case "!", "not":
		return "NOT (" + inner + ")", nil
	case "-":
		return "-(" + inner + ")", nil
	case "+":
		return inner, nil
	default:
		return "", fmt.Errorf("unsupported unary operator %q", v.Operator)
	}
}

// emitMember recognises the $env["#field"] pattern produced by rewriteHashRefs
// and emits a quoted column reference. Anything else is rejected.
func emitMember(v *ast.MemberNode) (string, error) {
	id, ok := v.Node.(*ast.IdentifierNode)
	if !ok || id.Value != "$env" {
		return "", fmt.Errorf("member access not supported (only $env[\"#field\"] form, produced by hash-prefix rewrite)")
	}
	prop, ok := v.Property.(*ast.StringNode)
	if !ok {
		return "", fmt.Errorf("$env access requires a string literal key")
	}
	return quoteIdent(prop.Value), nil
}

func emitArray(v *ast.ArrayNode) (string, error) {
	if len(v.Nodes) == 0 {
		return "", fmt.Errorf("array literal cannot be empty in pushdown SQL")
	}
	parts := make([]string, 0, len(v.Nodes))
	for _, elem := range v.Nodes {
		switch e := elem.(type) {
		case *ast.StringNode:
			parts = append(parts, quoteString(e.Value))
		case *ast.IntegerNode:
			parts = append(parts, strconv.FormatInt(int64(e.Value), 10))
		case *ast.FloatNode:
			parts = append(parts, strconv.FormatFloat(e.Value, 'g', -1, 64))
		case *ast.BoolNode:
			if e.Value {
				parts = append(parts, "TRUE")
			} else {
				parts = append(parts, "FALSE")
			}
		default:
			return "", fmt.Errorf("array elements must be literals (string/int/float/bool), got %T", elem)
		}
	}
	return "(" + strings.Join(parts, ", ") + ")", nil
}

var binaryOpMap = map[string]string{
	"==":  "=",
	"!=":  "<>",
	"<":   "<",
	"<=":  "<=",
	">":   ">",
	">=":  ">=",
	"&&":  "AND",
	"and": "AND",
	"||":  "OR",
	"or":  "OR",
}

// quoteIdent wraps a column name in Presto double quotes, escaping any embedded
// double quotes by doubling them.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteString wraps a string literal in Presto single quotes, escaping any
// embedded single quotes by doubling them.
func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
