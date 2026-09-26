//go:build !nosqlite

package main

// Driver SQLite (modernc.org/sqlite, C transpilado a Go). Sólo se usa cuando
// POSTGRES_AUTH_DB está vacío: la sesión de whatsmeow va entonces a
// ./dbdata/*.db. Con la etiqueta `nosqlite` queda fuera del binario.
import _ "modernc.org/sqlite"

const sqliteCompiled = true
