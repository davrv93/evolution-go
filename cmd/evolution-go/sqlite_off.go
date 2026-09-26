//go:build nosqlite

package main

// Build sin SQLite (`-tags nosqlite`, la de la imagen de producción): la
// sesión de whatsmeow va obligatoriamente a PostgreSQL (POSTGRES_AUTH_DB).
// modernc.org/sqlite y su libc transpilada eran ~1,7 MB de heap vivo en
// reposo (tablas que netdb arma en init aunque nadie abra la base) y unos
// 4 MB de binario. Si POSTGRES_AUTH_DB falta, el arranque falla con un
// mensaje claro en vez de «sql: unknown driver "sqlite"» más tarde.
const sqliteCompiled = false
