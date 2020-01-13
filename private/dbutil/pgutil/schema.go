// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

// Package pgutil contains utilities for postgres
package pgutil

import (
	"context"
	"database/sql"
	"encoding/hex"
	"math/rand"
	"net/url"
	"strings"

	"github.com/lib/pq"
)

// CreateRandomTestingSchemaName creates a random schema name string.
func CreateRandomTestingSchemaName(n int) string {
	data := make([]byte, n)

	// math/rand.Read() always returns a nil error so there's no need to handle the error.
	_, _ = rand.Read(data)
	return hex.EncodeToString(data)
}

// ConnstrWithSchema adds schema to a  connection string
func ConnstrWithSchema(connstr, schema string) string {
	if strings.Contains(connstr, "?") {
		connstr += "&options="
	} else {
		connstr += "?options="
	}
	return connstr + url.QueryEscape("--search_path="+pq.QuoteIdentifier(schema))
}

// ParseSchemaFromConnstr returns the name of the schema parsed from the
// connection string if one is provided
func ParseSchemaFromConnstr(connstr string) (string, error) {
	url, err := url.Parse(connstr)
	if err != nil {
		return "", err
	}
	queryValues := url.Query()
	// this is the Proper™ way to encode search_path in a pq connection string
	options := queryValues["options"]
	for _, option := range options {
		if strings.HasPrefix(option, "--search_path=") {
			return UnquoteIdentifier(option[len("--search_path="):]), nil
		}
	}
	// this is another way we've used before; supported brokenly as a kludge in github.com/lib/pq
	schema := queryValues["search_path"]
	if len(schema) > 0 {
		return UnquoteIdentifier(schema[0]), nil
	}
	return "", nil
}

// QuoteSchema quotes schema name for
func QuoteSchema(schema string) string {
	return pq.QuoteIdentifier(schema)
}

// Execer is for executing sql
type Execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// CreateSchema creates a schema if it doesn't exist.
func CreateSchema(ctx context.Context, db Execer, schema string) (err error) {
	for try := 0; try < 5; try++ {
		_, err = db.Exec(`create schema if not exists ` + QuoteSchema(schema) + `;`)

		// Postgres `CREATE SCHEMA IF NOT EXISTS` may return "duplicate key value violates unique constraint".
		// In that case, we will retry rather than doing anything more complicated.
		//
		// See more in: https://stackoverflow.com/a/29908840/192220
		if IsConstraintError(err) {
			continue
		}
		return err
	}

	return err
}

// DropSchema drops the named schema
func DropSchema(ctx context.Context, db Execer, schema string) error {
	_, err := db.Exec(`drop schema ` + QuoteSchema(schema) + ` cascade;`)
	return err
}
