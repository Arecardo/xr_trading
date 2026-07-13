// Package sqlbuilder builds small parameterized PostgreSQL queries.
package sqlbuilder

import (
	"errors"
	"fmt"
	"strings"
)

// Condition is a parameterized SQL predicate.
type Condition struct {
	sql  string
	args []any
}

// Eq builds a column equality predicate.
func Eq(column string, value any) Condition {
	return Condition{sql: fmt.Sprintf("%s = ?", column), args: []any{value}}
}

// Gte builds a column greater-than-or-equal predicate.
func Gte(column string, value any) Condition {
	return Condition{sql: fmt.Sprintf("%s >= ?", column), args: []any{value}}
}

// Lte builds a column less-than-or-equal predicate.
func Lte(column string, value any) Condition {
	return Condition{sql: fmt.Sprintf("%s <= ?", column), args: []any{value}}
}

// Raw builds a custom predicate. Prefer the typed helpers for user input.
func Raw(sql string, args ...any) Condition {
	return Condition{sql: sql, args: args}
}

// SelectQuery is a chainable SELECT query builder.
type SelectQuery struct {
	columns    []string
	table      string
	conditions []Condition
	orderBy    []string
	limit      *int
	offset     *int
}

// Select starts a SELECT query.
func Select(columns ...string) *SelectQuery {
	return &SelectQuery{columns: columns}
}

// From sets the source table.
func (q *SelectQuery) From(table string) *SelectQuery {
	q.table = table
	return q
}

// Where adds the first WHERE predicate. Calling it more than once appends with AND.
func (q *SelectQuery) Where(condition Condition) *SelectQuery {
	return q.And(condition)
}

// And appends another WHERE predicate with AND semantics.
func (q *SelectQuery) And(condition Condition) *SelectQuery {
	q.conditions = append(q.conditions, condition)
	return q
}

// OrderBy appends ORDER BY expressions.
func (q *SelectQuery) OrderBy(expressions ...string) *SelectQuery {
	q.orderBy = append(q.orderBy, expressions...)
	return q
}

// Limit sets LIMIT.
func (q *SelectQuery) Limit(limit int) *SelectQuery {
	q.limit = &limit
	return q
}

// Offset sets OFFSET.
func (q *SelectQuery) Offset(offset int) *SelectQuery {
	q.offset = &offset
	return q
}

// Build renders SQL with PostgreSQL placeholders and ordered args.
func (q *SelectQuery) Build() (string, []any, error) {
	if len(q.columns) == 0 {
		return "", nil, errors.New("select columns are required")
	}
	if q.table == "" {
		return "", nil, errors.New("from table is required")
	}
	var builder strings.Builder
	builder.WriteString("SELECT ")
	builder.WriteString(strings.Join(q.columns, ", "))
	builder.WriteString(" FROM ")
	builder.WriteString(q.table)

	var args []any
	if len(q.conditions) > 0 {
		builder.WriteString(" WHERE ")
		for index, condition := range q.conditions {
			if condition.sql == "" {
				return "", nil, errors.New("condition SQL is required")
			}
			if index > 0 {
				builder.WriteString(" AND ")
			}
			builder.WriteString(rebindPlaceholders(condition.sql, len(args)+1))
			args = append(args, condition.args...)
		}
	}
	if len(q.orderBy) > 0 {
		builder.WriteString(" ORDER BY ")
		builder.WriteString(strings.Join(q.orderBy, ", "))
	}
	if q.limit != nil {
		if *q.limit < 0 {
			return "", nil, errors.New("limit must not be negative")
		}
		builder.WriteString(fmt.Sprintf(" LIMIT %d", *q.limit))
	}
	if q.offset != nil {
		if *q.offset < 0 {
			return "", nil, errors.New("offset must not be negative")
		}
		builder.WriteString(fmt.Sprintf(" OFFSET %d", *q.offset))
	}
	return builder.String(), args, nil
}

func rebindPlaceholders(sql string, start int) string {
	var builder strings.Builder
	next := start
	for _, char := range sql {
		if char != '?' {
			builder.WriteRune(char)
			continue
		}
		builder.WriteString(fmt.Sprintf("$%d", next))
		next++
	}
	return builder.String()
}
