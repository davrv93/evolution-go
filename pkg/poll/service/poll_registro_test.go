package poll_service

// Registro de encuestas contra Postgres real (esquema temporal propio).
//
//	POLL_PRUEBA_DSN=postgres://postgres:pod@127.0.0.1:55433/pod_pruebas?sslmode=disable \
//	  go test ./pkg/poll/service -run Registro

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestResolverOpcionesPorHash(t *testing.T) {
	h := func(s string) []byte { x := sha256.Sum256([]byte(s)); return x[:] }
	nombres, desconocidos := ResolverOpciones([][]byte{h("Sí, claro"), h("otra")}, []string{"No", "Sí, claro"})
	if !reflect.DeepEqual(nombres, []string{"Sí, claro"}) || len(desconocidos) != 1 {
		t.Fatalf("resolver mal: %v %v", nombres, desconocidos)
	}
}

func TestRegistroDeEncuestasEnPostgres(t *testing.T) {
	dsn := os.Getenv("POLL_PRUEBA_DSN")
	if dsn == "" {
		t.Skip("sin POLL_PRUEBA_DSN: se salta la prueba contra Postgres")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	esquema := fmt.Sprintf("poll_prueba_%d", time.Now().UnixNano()%1_000_000_000)
	if _, err := admin.Exec("CREATE SCHEMA " + esquema); err != nil {
		t.Fatalf("no se pudo crear el esquema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + esquema + " CASCADE") })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	db, err := sql.Open("postgres", dsn+sep+"search_path="+esquema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &pollService{db: db}
	if err := s.migrarRegistro(); err != nil {
		t.Fatalf("migrar: %v", err)
	}
	e := Encuesta{MessageID: "3EB0X", InstanceID: "inst1", ChatJid: "51999@s.whatsapp.net", Pregunta: "¿Volverías?", Opciones: []string{"Sí, claro", "No"}}
	if err := s.RegistrarEncuesta(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.RegistrarEncuesta(ctx, e); err != nil {
		t.Fatalf("registrar dos veces es idempotente: %v", err)
	}
	got, err := s.EncuestaPorID(ctx, "inst1", "3EB0X")
	if err != nil || got == nil || !reflect.DeepEqual(got.Opciones, e.Opciones) || got.Pregunta != e.Pregunta {
		t.Fatalf("lectura mal: %+v %v", got, err)
	}
	if otra, _ := s.EncuestaPorID(ctx, "inst2", "3EB0X"); otra != nil {
		t.Fatal("otra instancia no ve la encuesta")
	}
}
