// Command baileys-import migra una sesión de WhatsApp de Evolution API v2.3.x
// (Node, Baileys 7) al almacén de whatsmeow que usa evolution-go, sin volver a
// escanear el QR.
//
// No conecta a WhatsApp ni envía nada: sólo lee dos volcados locales y escribe
// en la base evogo_auth. NUNCA imprime material de llaves: ni en el resumen ni
// en los errores.
//
// Uso:
//
//	baileys-import --creds creds.json [--keys keys.txt] --dry-run [--dsn ...]
//	baileys-import --creds creds.json [--keys keys.txt] --dsn 'postgres://...' [--replace]
//
// El DSN también puede ir en la variable BAILEYS_IMPORT_DSN para que no quede
// en el historial de la shell ni en `ps`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type options struct {
	CredsPath      string
	KeysPath       string
	DSN            string
	DryRun         bool
	Replace        bool
	PushName       string
	PushNameSet    bool
	LIDMigrationTS int64
	NoIdentities   bool
	NoTCTokens     bool
	NoLIDMap       bool
}

func main() {
	var o options
	fs := flag.NewFlagSet("baileys-import", flag.ContinueOnError)
	fs.StringVar(&o.CredsPath, "creds", "", "archivo con Session.creds de Evolution (BufferJSON; admite la doble codificación de la columna)")
	fs.StringVar(&o.KeysPath, "keys", "", "opcional: volcado del hash de llaves de Redis (objeto JSON campo→valor, o salida de `redis-cli --raw HGETALL`)")
	fs.StringVar(&o.DSN, "dsn", "", "DSN postgres de evogo_auth (o variable BAILEYS_IMPORT_DSN)")
	fs.BoolVar(&o.DryRun, "dry-run", false, "valida y muestra un resumen sin secretos; no escribe nada")
	fs.BoolVar(&o.Replace, "replace", false, "si ya existe un dispositivo con ese JID, lo borra (con todas sus llaves) y lo sustituye")
	pushName := fs.String("push-name", "", "sobrescribe el push name (por defecto creds.me.name)")
	fs.Int64Var(&o.LIDMigrationTS, "lid-migration-ts", 0, "valor para whatsmeow_device.lid_migration_ts (0 = lo aprende whatsmeow)")
	fs.BoolVar(&o.NoIdentities, "no-identities", false, "no importar identidades de contactos sacadas de las sesiones")
	fs.BoolVar(&o.NoTCTokens, "no-tctokens", false, "no importar tctoken (privacy tokens)")
	fs.BoolVar(&o.NoLIDMap, "no-lid-map", false, "no importar lid-mapping a whatsmeow_lid_map")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "push-name" {
			o.PushNameSet = true
			o.PushName = *pushName
		}
	})
	if o.DSN == "" {
		o.DSN = os.Getenv("BAILEYS_IMPORT_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := run(ctx, o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options, w io.Writer) error {
	if o.CredsPath == "" {
		return errors.New("falta --creds")
	}
	if !o.DryRun && o.DSN == "" {
		return errors.New("falta --dsn (o BAILEYS_IMPORT_DSN); para sólo validar usa --dry-run")
	}

	credsData, err := os.ReadFile(o.CredsPath)
	if err != nil {
		return fmt.Errorf("leer --creds: %w", err)
	}
	creds, err := parseCreds(credsData)
	if err != nil {
		return err
	}
	copts := convertOptions{
		PushName:          o.PushName,
		PushNameSet:       o.PushNameSet,
		LIDMigrationTS:    o.LIDMigrationTS,
		SkipIdentities:    o.NoIdentities,
		SkipPrivacyTokens: o.NoTCTokens,
		SkipLIDMappings:   o.NoLIDMap,
	}
	conv, convErr := convertCreds(creds, copts)
	if convErr != nil && !errors.Is(convErr, errChecksFailed) {
		return convErr
	}

	if o.KeysPath != "" {
		keysData, err := os.ReadFile(o.KeysPath)
		if err != nil {
			return fmt.Errorf("leer --keys: %w", err)
		}
		fields, err := parseKeysDump(keysData)
		if err != nil {
			return err
		}
		convertKeys(conv, fields, uint32(creds.FirstUnuploadedPreKeyID), creds.MyAppStateKeyID, copts)
	}

	var st *authStore
	if o.DSN != "" {
		st, err = openAuthStore(ctx, o.DSN)
		if err != nil {
			return err
		}
		defer st.Close()
	}

	if o.DryRun {
		printSummary(w, &conv.Summary)
		if st != nil {
			exists, same, schema, err := st.probe(ctx, *conv.Device.ID)
			if err != nil {
				return err
			}
			switch {
			case !schema:
				fmt.Fprintln(w, "  base destino:           sin esquema whatsmeow todavía (se crea con Upgrade al importar)")
			case exists:
				fmt.Fprintln(w, "  base destino:           YA EXISTE un dispositivo con este JID → hará falta --replace")
			default:
				fmt.Fprintln(w, "  base destino:           no existe dispositivo con este JID")
			}
			if len(same) > 0 {
				fmt.Fprintf(w, "  otros dispositivos del mismo número en la base: %s\n", strings.Join(same, ", "))
			}
		}
		if convErr != nil {
			return convErr
		}
		fmt.Fprintln(w, "DRY-RUN: no se escribió nada.")
		return nil
	}

	if convErr != nil {
		printSummary(w, &conv.Summary)
		return convErr
	}
	if err := st.upgrade(ctx); err != nil {
		return err
	}
	if err := st.write(ctx, conv, o.Replace); err != nil {
		return err
	}
	printSummary(w, &conv.Summary)
	fmt.Fprintf(w, "IMPORTADO: dispositivo %s escrito en whatsmeow_device (transacción confirmada).\n", conv.Summary.JID)
	return nil
}

func yesNo(b bool) string {
	if b {
		return "OK"
	}
	return "FALLA"
}

// printSummary muestra SÓLO metadatos: identificadores públicos de la cuenta,
// conteos y resultados de comprobaciones. Ningún byte de llave.
func printSummary(w io.Writer, s *summary) {
	fmt.Fprintln(w, "== baileys-import: resumen (sin secretos) ==")
	fmt.Fprintf(w, "  JID:                     %s\n", s.JID)
	if s.LID != "" {
		fmt.Fprintf(w, "  LID:                     %s\n", s.LID)
	} else {
		fmt.Fprintln(w, "  LID:                     (ausente en creds.me.lid)")
	}
	fmt.Fprintf(w, "  platform:                %q\n", s.Platform)
	pn := "presente"
	if s.PushName == "" {
		pn = "VACÍO (evolution-go no marca connected=true en BD hasta recibir PushNameSetting; usa --push-name)"
	}
	if s.PushNameFromFlag {
		pn += " (de --push-name)"
	}
	fmt.Fprintf(w, "  push name:               %s\n", pn)
	fmt.Fprintf(w, "  registrationId:          %d\n", s.RegistrationID)
	fmt.Fprintf(w, "  signedPreKey.keyId:      %d\n", s.SignedPreKeyID)
	fmt.Fprintf(w, "  nextPreKeyId / firstUnuploadedPreKeyId: %d / %d\n", s.NextPreKeyID, s.FirstUnuploadedPreKeyID)
	fmt.Fprintf(w, "  ADV: keyIndex=%d hosted=%v\n", s.KeyIndex, s.Hosted)
	fmt.Fprintln(w, "  comprobaciones:")
	for _, c := range s.Checks {
		fmt.Fprintf(w, "    [%s] %s\n", yesNo(c.OK), c.Name)
	}
	if !s.KeysProvided {
		fmt.Fprintln(w, "  llaves (--keys):         no se dieron; sólo se importará el dispositivo")
		return
	}
	fmt.Fprintf(w, "  llaves (--keys):         %d campos\n", s.TotalFields)
	fmt.Fprintf(w, "    pre-keys a importar:   %d (subidas=%d, sin subir=%d, descartadas por inválidas=%d)\n",
		s.PreKeys, s.PreKeysUploaded, s.PreKeys-s.PreKeysUploaded, s.PreKeysBad)
	fmt.Fprintf(w, "    app-state-sync-keys:   %d (inválidas=%d; myAppStateKeyId presente entre ellas: %v)\n",
		s.AppStateKeys, s.AppStateKeysBad, !s.HasMyAppStateKeyID || s.MyAppStateKeyPresent)
	fmt.Fprintf(w, "    lid-mapping (pares):   %d (inválidos/duplicados=%d)\n", s.LIDPairs, s.LIDBad)
	fmt.Fprintf(w, "    tctoken:               %d (sin fecha, omitidos=%d; inválidos=%d)\n", s.PrivacyTokens, s.PrivacyTokensNoTS, s.PrivacyTokensBad)
	fmt.Fprintf(w, "    identidades contactos: %d (sacadas de sesiones; ilegibles=%d)\n", s.Identities, s.IdentitiesBad)
	fmt.Fprintln(w, "  NO se importan (whatsmeow las renegocia solo):")
	fmt.Fprintf(w, "    session-*:             %d (libsignal-node JSON ≠ protobuf de go.mau.fi/libsignal)\n", s.SkippedSessions)
	fmt.Fprintf(w, "    sender-key-*:          %d\n", s.SkippedSenderKeys)
	fmt.Fprintf(w, "    sender-key-memory-*:   %d\n", s.SkippedSenderKeyMem)
	fmt.Fprintf(w, "    app-state-sync-version-*: %d (whatsmeow hará sincronización completa del app-state)\n", s.SkippedAppStateVer)
	fmt.Fprintf(w, "    device-list-*:         %d (whatsmeow no las persiste)\n", s.SkippedDeviceList)
	if s.SkippedOther > 0 {
		keys := make([]string, 0, len(s.OtherPrefixes))
		for k := range s.OtherPrefixes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s-*=%d", k, s.OtherPrefixes[k])
		}
		fmt.Fprintf(w, "    otros:                 %d (%s)\n", s.SkippedOther, strings.Join(parts, ", "))
	}
}
