package main

// Pruebas con datos 100 % sintéticos: se generan llaves curve25519 reales, una
// signed prekey firmada con la identidad y una identidad ADV firmada por una
// "cuenta principal" también sintética, se serializan como lo haría Baileys +
// Evolution 2.3.7 y se importan en un Postgres efímero.
//
// La base se toma de BAILEYS_IMPORT_TEST_DSN (por defecto el contenedor
// baileys-import-pg en 127.0.0.1:55501). Si no responde, las pruebas que la
// necesitan se saltan con aviso explícito.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/libsignal/ecc"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
)

// ---------------------------------------------------------------------------
// Generador sintético

type bufStyle int

const (
	styleB64    bufStyle = iota // {"type":"Buffer","data":"<b64>"}  (BufferJSON.replacer)
	styleArray                  // {"type":"Buffer","data":[n,...]}
	styleIdxObj                 // {"0":n,"1":n,...}  (Uint8Array sin replacer)
)

func buf(b []byte, st bufStyle) any {
	switch st {
	case styleArray:
		arr := make([]int, len(b))
		for i, v := range b {
			arr[i] = int(v)
		}
		return map[string]any{"type": "Buffer", "data": arr}
	case styleIdxObj:
		m := make(map[string]int, len(b))
		for i, v := range b {
			m[fmt.Sprint(i)] = int(v)
		}
		return m
	}
	return map[string]any{"type": "Buffer", "data": base64.StdEncoding.EncodeToString(b)}
}

func rnd(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

type synthAppKey struct {
	ID          []byte
	Data        []byte
	RawID, Cur  uint32
	DevIdx      []uint32
	TimestampMS int64
}

type synthSession struct {
	NodeAddr string // "<user>[_1].<dev>"
	Remote   *keys.KeyPair
}

type synth struct {
	Noise, Ident, SPK, Account *keys.KeyPair
	SPKID                      uint32
	SPKSig                     [64]byte
	Details                    []byte
	AccSig, DevSig             [64]byte
	AdvSecret                  []byte
	RegID                      uint32
	PNUser, LIDUser            string
	Device                     uint16
	PushName, Platform         string

	PreKeys         map[uint32]*keys.KeyPair
	FirstUnuploaded uint32
	NextPreKeyID    uint32
	AppKeys         []synthAppKey
	LIDPairs        map[string]string // pn → lid (contactos)
	TCTokens        map[string][]byte // jid → token
	TCTokenTS       int64
	Sessions        []synthSession
}

func sign(kp *keys.KeyPair, msg []byte) [64]byte {
	return ecc.CalculateSignature(ecc.NewDjbECPrivateKey(*kp.Priv), msg)
}

func newSynth(t *testing.T, pnUser, lidUser string, device uint16) *synth {
	t.Helper()
	s := &synth{
		Noise: keys.NewKeyPair(), Ident: keys.NewKeyPair(), SPK: keys.NewKeyPair(), Account: keys.NewKeyPair(),
		SPKID:     3, // Baileys 7 rota la signed prekey: keyId > 1 es lo normal
		AdvSecret: rnd(32),
		RegID:     12345,
		PNUser:    pnUser, LIDUser: lidUser, Device: device,
		PushName: "Venta Sintética", Platform: "smba",
		PreKeys:  map[uint32]*keys.KeyPair{},
		LIDPairs: map[string]string{}, TCTokens: map[string][]byte{},
		TCTokenTS: 1_700_000_000,
	}
	s.SPKSig = sign(s.Ident, append([]byte{ecc.DjbType}, s.SPK.Pub[:]...))
	details, err := proto.Marshal(&waAdv.ADVDeviceIdentity{
		RawID:      proto.Uint32(987654),
		Timestamp:  proto.Uint64(1_690_000_000),
		KeyIndex:   proto.Uint32(4),
		DeviceType: waAdv.ADVEncryptionType_E2EE.Enum(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Details = details
	s.AccSig = sign(s.Account, concat([]byte{6, 0}, details, s.Ident.Pub[:]))
	s.DevSig = sign(s.Ident, concat([]byte{6, 1}, details, s.Ident.Pub[:], s.Account.Pub[:]))

	// 30 pre-keys; Baileys ya subió 1..25 (firstUnuploadedPreKeyId=26).
	for id := uint32(1); id <= 30; id++ {
		s.PreKeys[id] = keys.NewKeyPair()
	}
	delete(s.PreKeys, 7) // consumida por un contacto: Baileys la borró
	s.FirstUnuploaded, s.NextPreKeyID = 26, 31

	for i := 0; i < 3; i++ {
		s.AppKeys = append(s.AppKeys, synthAppKey{
			ID: rnd(6), Data: rnd(32), RawID: uint32(1000 + i), Cur: uint32(i), DevIdx: []uint32{0, uint32(i + 1)},
			TimestampMS: 1_690_000_000_000 + int64(i),
		})
	}
	s.LIDPairs["51900000111"] = "200000000000111"
	s.LIDPairs["51900000222"] = "200000000000222"
	s.TCTokens["51900000111@s.whatsapp.net"] = rnd(20) // PN con mapeo → se guarda por LID
	s.TCTokens["200000000000333@lid"] = rnd(20)        // ya por LID
	s.TCTokens["51900000999@s.whatsapp.net"] = rnd(20) // PN sin mapeo
	s.Sessions = []synthSession{
		{NodeAddr: "51900000111.0", Remote: keys.NewKeyPair()},
		{NodeAddr: "200000000000222_1.3", Remote: keys.NewKeyPair()},
	}
	return s
}

func (s *synth) jid() string { return fmt.Sprintf("%s:%d@s.whatsapp.net", s.PNUser, s.Device) }
func (s *synth) lid() string { return fmt.Sprintf("%s:%d@lid", s.LIDUser, s.Device) }

// credsJSON devuelve lo que guarda Evolution en Session.creds:
// JSON.stringify(JSON.stringify(creds, BufferJSON.replacer)).
func (s *synth) credsColumn(t *testing.T, st bufStyle, pub33 bool) []byte {
	t.Helper()
	pub := func(k *keys.KeyPair) any {
		if pub33 {
			return buf(append([]byte{5}, k.Pub[:]...), st)
		}
		return buf(k.Pub[:], st)
	}
	kp := func(k *keys.KeyPair) map[string]any {
		return map[string]any{"private": buf(k.Priv[:], st), "public": pub(k)}
	}
	b64 := base64.StdEncoding.EncodeToString
	creds := map[string]any{
		"noiseKey":                kp(s.Noise),
		"pairingEphemeralKeyPair": kp(keys.NewKeyPair()),
		"signedIdentityKey":       kp(s.Ident),
		"signedPreKey": map[string]any{
			"keyPair":   kp(s.SPK),
			"signature": buf(s.SPKSig[:], st),
			"keyId":     s.SPKID,
		},
		"registrationId":           s.RegID,
		"advSecretKey":             b64(s.AdvSecret),
		"processedHistoryMessages": []any{},
		"nextPreKeyId":             s.NextPreKeyID,
		"firstUnuploadedPreKeyId":  s.FirstUnuploaded,
		"accountSyncCounter":       1,
		"accountSettings":          map[string]any{"unarchiveChats": false},
		"registered":               false,
		"me":                       map[string]any{"id": s.jid(), "lid": s.lid(), "name": s.PushName},
		// protobufjs toJSON: bytes → base64
		"account": map[string]any{
			"details":             b64(s.Details),
			"accountSignatureKey": b64(s.Account.Pub[:]),
			"accountSignature":    b64(s.AccSig[:]),
			"deviceSignature":     b64(s.DevSig[:]),
		},
		"signalIdentities": []any{map[string]any{
			"identifier":    map[string]any{"name": s.lid(), "deviceId": 0},
			"identifierKey": buf(append([]byte{5}, s.Account.Pub[:]...), st),
		}},
		"platform":                 s.Platform,
		"myAppStateKeyId":          b64(s.AppKeys[len(s.AppKeys)-1].ID),
		"lastAccountSyncTimestamp": 1_690_000_000,
	}
	inner, err := json.Marshal(creds)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := json.Marshal(string(inner))
	if err != nil {
		t.Fatal(err)
	}
	return outer
}

// redisRaw replica la doble codificación de Evolution 2.3.7 (CacheService.hSet
// + RedisCache.hSet): lo que queda en Redis es JSON.stringify(JSON.stringify(v)).
func redisRaw(t *testing.T, v any) string {
	t.Helper()
	j1, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := json.Marshal(string(j1))
	if err != nil {
		t.Fatal(err)
	}
	return string(j2)
}

func (s *synth) keysFields(t *testing.T, st bufStyle) map[string]string {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString
	f := map[string]string{}
	for id, k := range s.PreKeys {
		f[fmt.Sprintf("pre-key-%d", id)] = redisRaw(t, map[string]any{"private": buf(k.Priv[:], st), "public": buf(k.Pub[:], st)})
	}
	for _, k := range s.AppKeys {
		f["app-state-sync-key-"+b64(k.ID)] = redisRaw(t, map[string]any{
			"keyData":     b64(k.Data),
			"fingerprint": map[string]any{"rawId": k.RawID, "currentIndex": k.Cur, "deviceIndexes": k.DevIdx},
			"timestamp":   fmt.Sprint(k.TimestampMS), // longs: String
		})
	}
	for pn, l := range s.LIDPairs {
		f["lid-mapping-"+pn] = redisRaw(t, l)
		f["lid-mapping-"+l+"_reverse"] = redisRaw(t, pn)
	}
	for j, tok := range s.TCTokens {
		f["tctoken-"+j] = redisRaw(t, map[string]any{"token": buf(tok, st), "timestamp": fmt.Sprint(s.TCTokenTS)})
	}
	f["tctoken-51900000888@s.whatsapp.net"] = redisRaw(t, map[string]any{"token": buf(rnd(20), st)}) // sin fecha
	for _, ss := range s.Sessions {
		rec := map[string]any{
			"_sessions": map[string]any{
				b64(rnd(33)): map[string]any{
					"registrationId": 42,
					"currentRatchet": map[string]any{
						"ephemeralKeyPair":       map[string]any{"pubKey": b64(rnd(33)), "privKey": b64(rnd(32))},
						"lastRemoteEphemeralKey": b64(rnd(33)), "previousCounter": 0, "rootKey": b64(rnd(32)),
					},
					"indexInfo": map[string]any{
						"baseKey": b64(rnd(33)), "baseKeyType": 1, "closed": -1, "used": 1, "created": 1,
						"remoteIdentityKey": b64(append([]byte{5}, ss.Remote.Pub[:]...)),
					},
					"_chains": map[string]any{},
				},
			},
			"version": "v1",
		}
		f["session-"+ss.NodeAddr] = redisRaw(t, rec)
	}
	f["sender-key-120363000000000000@g.us::51900000111::0"] = redisRaw(t, buf([]byte(`{"x":1}`), st))
	f["sender-key-memory-120363000000000000@g.us"] = redisRaw(t, map[string]any{"51900000111:0@s.whatsapp.net": true})
	f["app-state-sync-version-regular"] = redisRaw(t, map[string]any{"version": 5, "hash": buf(rnd(128), st), "indexValueMap": map[string]any{}})
	f["device-list-51900000111"] = redisRaw(t, []string{"0", "1"})
	return f
}

// secrets: todo lo que jamás debe aparecer en la salida.
func (s *synth) secrets() [][]byte {
	out := [][]byte{s.Noise.Priv[:], s.Ident.Priv[:], s.SPK.Priv[:], s.AdvSecret, s.SPKSig[:], s.AccSig[:], s.DevSig[:]}
	for _, k := range s.PreKeys {
		out = append(out, k.Priv[:])
	}
	for _, k := range s.AppKeys {
		out = append(out, k.Data)
	}
	for _, tok := range s.TCTokens {
		out = append(out, tok)
	}
	return out
}

func assertNoSecrets(t *testing.T, text string, secrets [][]byte) {
	t.Helper()
	for _, sec := range secrets {
		for _, enc := range []string{
			base64.StdEncoding.EncodeToString(sec), base64.RawStdEncoding.EncodeToString(sec),
			base64.URLEncoding.EncodeToString(sec), hex.EncodeToString(sec), strings.ToUpper(hex.EncodeToString(sec)),
			fmt.Sprint(sec),
		} {
			if len(enc) >= 16 && strings.Contains(text, enc) {
				t.Fatalf("la salida contiene material secreto (codificación de %d bytes)", len(sec))
			}
		}
	}
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func keysAsJSONObject(t *testing.T, f map[string]string) []byte {
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func keysAsHGETALL(f map[string]string) []byte {
	var b strings.Builder
	for k, v := range f {
		b.WriteString(k)
		b.WriteByte('\n')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// Base efímera

func testDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv("BAILEYS_IMPORT_TEST_DSN")
	if base == "" {
		base = "postgres://postgres:x@127.0.0.1:55501/postgres?sslmode=disable"
	}
	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Skipf("Postgres de prueba no disponible: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Skipf("Postgres de prueba no responde (arranca baileys-import-pg o define BAILEYS_IMPORT_TEST_DSN): %v", err)
	}
	name := "bi_" + hex.EncodeToString(rnd(6))
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
		_ = admin.Close()
	})
	return strings.Replace(base, "/postgres?", "/"+name+"?", 1)
}

func openVerify(t *testing.T, dsn string) (*sqlstore.Container, *sql.DB) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return sqlstore.NewWithDB(db, "postgres", nil), db
}

// ---------------------------------------------------------------------------
// Pruebas unitarias de formato

func TestBJBytesForms(t *testing.T) {
	want := []byte{0, 1, 2, 250, 255}
	cases := map[string]string{
		"buffer-b64":   `{"type":"Buffer","data":"AAEC+v8="}`,
		"buffer-array": `{"type":"Buffer","data":[0,1,2,250,255]}`,
		"idx-object":   `{"0":0,"1":1,"2":2,"3":250,"4":255}`,
		"plain-b64":    `"AAEC+v8="`,
		"plain-array":  `[0,1,2,250,255]`,
	}
	for name, in := range cases {
		var b bjBytes
		if err := json.Unmarshal([]byte(in), &b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(b, want) {
			t.Fatalf("%s: bytes distintos", name)
		}
	}
	for _, bad := range []string{`{"type":"Nope","data":"AA=="}`, `{"0":1,"2":3}`, `[300]`, `true`} {
		var b bjBytes
		if err := json.Unmarshal([]byte(bad), &b); err == nil {
			t.Fatalf("debería fallar: %s", bad)
		}
	}
}

func TestDecodeRedisValue(t *testing.T) {
	// objeto doblemente codificado
	raw := redisRaw(t, map[string]any{"a": 1})
	v, err := decodeRedisValue(raw)
	if err != nil || string(v) != `{"a":1}` {
		t.Fatalf("objeto: %q %v", v, err)
	}
	// cadena (lid-mapping) doblemente codificada: debe quedar CADENA, no número
	v, err = decodeRedisValue(redisRaw(t, "200000000000111"))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := lidMappingValue(v); !ok || s != "200000000000111" {
		t.Fatalf("lid-mapping: %q", v)
	}
	// una sola capa (otra herramienta): objeto tal cual
	v, err = decodeRedisValue(`{"a":1}`)
	if err != nil || string(v) != `{"a":1}` {
		t.Fatalf("una capa: %q %v", v, err)
	}
	// creds: la columna es JSON.stringify(JSON.stringify(...))
	inner, err := unwrapJSONString([]byte(`"{\"x\":1}"` + "\n"))
	if err != nil || string(inner) != `{"x":1}` {
		t.Fatalf("unwrap: %q %v", inner, err)
	}
}

func TestNodeAddr(t *testing.T) {
	for in, want := range map[string]string{
		"51900000111.0":       "51900000111:0",
		"200000000000222_1.3": "200000000000222_1:3",
	} {
		got, ok := nodeAddrToWhatsmeow(in)
		if !ok || got != want {
			t.Fatalf("%s → %s (quería %s)", in, got, want)
		}
	}
	// y coincide con lo que genera whatsmeow para ese JID
	j := types.JID{User: "200000000000222", Device: 3, Server: types.HiddenUserServer}
	if j.SignalAddress().String() != "200000000000222_1:3" {
		t.Fatalf("whatsmeow genera %s", j.SignalAddress().String())
	}
}

// ---------------------------------------------------------------------------
// Dry-run: resumen sin secretos y comprobaciones

func TestDryRunSummaryNoSecrets(t *testing.T) {
	s := newSynth(t, "51900000001", "100000000000001", 7)
	dir := t.TempDir()
	creds := writeFile(t, dir, "creds.json", s.credsColumn(t, styleB64, false))
	kf := writeFile(t, dir, "keys.json", keysAsJSONObject(t, s.keysFields(t, styleB64)))

	var out bytes.Buffer
	if err := run(context.Background(), options{CredsPath: creds, KeysPath: kf, DryRun: true}, &out); err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out.String())
	}
	txt := out.String()
	assertNoSecrets(t, txt, s.secrets())
	for _, want := range []string{
		"JID:                     51900000001:7@s.whatsapp.net",
		"LID:                     100000000000001:7@lid",
		`platform:                "smba"`,
		"registrationId:          12345",
		"signedPreKey.keyId:      3",
		"[OK] signedPreKey: firma verifica con signedIdentityKey",
		"[OK] account.accountSignature verifica",
		"[OK] account.deviceSignature verifica",
		"pre-keys a importar:   29 (subidas=24, sin subir=5, descartadas por inválidas=0)",
		"app-state-sync-keys:   3 (inválidas=0; myAppStateKeyId presente entre ellas: true)",
		"lid-mapping (pares):   3",
		"tctoken:               3 (sin fecha, omitidos=1; inválidos=0)",
		"identidades contactos: 2",
		"session-*:             2",
		"sender-key-*:          1",
		"sender-key-memory-*:   1",
		"app-state-sync-version-*: 1",
		"device-list-*:         1",
		"DRY-RUN: no se escribió nada.",
	} {
		if !strings.Contains(txt, want) {
			t.Fatalf("falta %q en el resumen:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "FALLA") {
		t.Fatalf("alguna comprobación falló:\n%s", txt)
	}
}

func TestDryRunDetectsBadSignature(t *testing.T) {
	s := newSynth(t, "51900000002", "100000000000002", 3)
	s.SPKSig[10] ^= 0xFF // firma corrupta
	dir := t.TempDir()
	creds := writeFile(t, dir, "creds.json", s.credsColumn(t, styleB64, false))
	var out bytes.Buffer
	err := run(context.Background(), options{CredsPath: creds, DryRun: true}, &out)
	if !errors.Is(err, errChecksFailed) {
		t.Fatalf("esperaba errChecksFailed, fue %v", err)
	}
	if !strings.Contains(out.String(), "[FALLA] signedPreKey: firma verifica") {
		t.Fatalf("el resumen no marca la firma:\n%s", out.String())
	}
	assertNoSecrets(t, out.String()+err.Error(), s.secrets())

	// Pública que no cuadra con la privada
	s2 := newSynth(t, "51900000003", "100000000000003", 3)
	s2.Noise = &keys.KeyPair{Priv: s2.Noise.Priv, Pub: keys.NewKeyPair().Pub}
	creds2 := writeFile(t, dir, "creds2.json", s2.credsColumn(t, styleB64, false))
	out.Reset()
	err = run(context.Background(), options{CredsPath: creds2, DryRun: true}, &out)
	if !errors.Is(err, errChecksFailed) || !strings.Contains(out.String(), "[FALLA] noiseKey") {
		t.Fatalf("no detectó noiseKey incoherente: %v\n%s", err, out.String())
	}
}

func TestErrorsDoNotLeak(t *testing.T) {
	s := newSynth(t, "51900000004", "100000000000004", 3)
	dir := t.TempDir()
	// privada de 31 bytes: el error debe nombrar el campo, no el valor
	col := s.credsColumn(t, styleB64, false)
	var inner string
	_ = json.Unmarshal(col, &inner)
	var m map[string]any
	_ = json.Unmarshal([]byte(inner), &m)
	short := s.Ident.Priv[:31]
	m["signedIdentityKey"].(map[string]any)["private"] = buf(short, styleB64)
	bad, _ := json.Marshal(m)
	p := writeFile(t, dir, "bad.json", bad)
	var out bytes.Buffer
	err := run(context.Background(), options{CredsPath: p, DryRun: true}, &out)
	if err == nil || !strings.Contains(err.Error(), "signedIdentityKey") {
		t.Fatalf("error inesperado: %v", err)
	}
	assertNoSecrets(t, err.Error()+out.String(), append(s.secrets(), short))

	// DSN con contraseña: el error de conexión no debe repetirla
	err = run(context.Background(), options{CredsPath: writeFile(t, dir, "ok.json", s.credsColumn(t, styleB64, false)),
		DSN: "postgres://u:SuperSecretoXYZ@127.0.0.1:1/db?sslmode=disable&connect_timeout=2"}, &out)
	if err == nil || strings.Contains(err.Error(), "SuperSecretoXYZ") {
		t.Fatalf("el error de DSN filtra la contraseña o no falló: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Importación completa en Postgres

func TestImportRoundTrip(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		style  bufStyle
		pub33  bool
		hgetal bool
		dev    uint16
	}{
		{"buffer-b64/pub32/json", styleB64, false, false, 7},
		{"buffer-array/pub33/hgetall", styleArray, true, true, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSynth(t, fmt.Sprintf("5190000%04d", tc.dev), fmt.Sprintf("10000000000%04d", tc.dev), tc.dev)
			dir := t.TempDir()
			creds := writeFile(t, dir, "creds.json", s.credsColumn(t, tc.style, tc.pub33))
			fields := s.keysFields(t, tc.style)
			var kdata []byte
			if tc.hgetal {
				kdata = keysAsHGETALL(fields)
			} else {
				kdata = keysAsJSONObject(t, fields)
			}
			kf := writeFile(t, dir, "keys.dump", kdata)

			var out bytes.Buffer
			if err := run(ctx, options{CredsPath: creds, KeysPath: kf, DSN: dsn}, &out); err != nil {
				t.Fatalf("import: %v\n%s", err, out.String())
			}
			assertNoSecrets(t, out.String(), s.secrets())
			verifyImported(t, ctx, dsn, s)

			// Reimportar sin --replace: aborta y no toca nada
			out.Reset()
			err := run(ctx, options{CredsPath: creds, KeysPath: kf, DSN: dsn}, &out)
			if !errors.Is(err, errDeviceExists) {
				t.Fatalf("esperaba errDeviceExists, fue %v", err)
			}
			// Con --replace: sustituye limpio (sin duplicados)
			if err := run(ctx, options{CredsPath: creds, KeysPath: kf, DSN: dsn, Replace: true}, &out); err != nil {
				t.Fatalf("replace: %v", err)
			}
			verifyImported(t, ctx, dsn, s)

			// Dry-run contra la base: avisa de que existe
			out.Reset()
			if err := run(ctx, options{CredsPath: creds, DSN: dsn, DryRun: true}, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "YA EXISTE un dispositivo con este JID") {
				t.Fatalf("dry-run no detecta el existente:\n%s", out.String())
			}
		})
	}
}

func verifyImported(t *testing.T, ctx context.Context, dsn string, s *synth) {
	t.Helper()
	container, db := openVerify(t, dsn)
	jid, _ := types.ParseJID(s.jid())
	dev, err := container.GetDevice(ctx, jid)
	if err != nil || dev == nil {
		t.Fatalf("GetDevice: %v (nil=%v)", err, dev == nil)
	}

	eq := func(name string, a, b []byte) {
		t.Helper()
		if !bytes.Equal(a, b) {
			t.Fatalf("%s no coincide byte a byte", name)
		}
	}
	if dev.ID.String() != s.jid() {
		t.Fatalf("ID %s", dev.ID)
	}
	if dev.LID.String() != s.lid() {
		t.Fatalf("LID %s", dev.LID)
	}
	if dev.RegistrationID != s.RegID {
		t.Fatalf("RegistrationID %d", dev.RegistrationID)
	}
	eq("NoiseKey.Priv", dev.NoiseKey.Priv[:], s.Noise.Priv[:])
	eq("NoiseKey.Pub", dev.NoiseKey.Pub[:], s.Noise.Pub[:])
	eq("IdentityKey.Priv", dev.IdentityKey.Priv[:], s.Ident.Priv[:])
	eq("IdentityKey.Pub", dev.IdentityKey.Pub[:], s.Ident.Pub[:])
	eq("SignedPreKey.Priv", dev.SignedPreKey.Priv[:], s.SPK.Priv[:])
	eq("SignedPreKey.Pub", dev.SignedPreKey.Pub[:], s.SPK.Pub[:])
	eq("SignedPreKey.Signature", dev.SignedPreKey.Signature[:], s.SPKSig[:])
	if dev.SignedPreKey.KeyID != s.SPKID {
		t.Fatalf("SignedPreKey.KeyID %d", dev.SignedPreKey.KeyID)
	}
	// La firma se verifica con lo LEÍDO de la base, como haría el servidor.
	if !ecc.VerifySignature(ecc.NewDjbECPublicKey(*dev.IdentityKey.Pub),
		append([]byte{ecc.DjbType}, dev.SignedPreKey.Pub[:]...), *dev.SignedPreKey.Signature) {
		t.Fatal("la firma de la signed prekey leída de la base no verifica")
	}
	eq("AdvSecretKey", dev.AdvSecretKey, s.AdvSecret)
	eq("Account.Details", dev.Account.Details, s.Details)
	eq("Account.AccountSignatureKey", dev.Account.AccountSignatureKey, s.Account.Pub[:])
	eq("Account.AccountSignature", dev.Account.AccountSignature, s.AccSig[:])
	eq("Account.DeviceSignature", dev.Account.DeviceSignature, s.DevSig[:])
	if dev.Platform != s.Platform || dev.PushName != s.PushName {
		t.Fatalf("Platform/PushName: %q %q", dev.Platform, dev.PushName)
	}
	// La firma de dispositivo leída también verifica (la usa whatsmeow en cada pkmsg).
	if !ecc.VerifySignature(ecc.NewDjbECPublicKey(*dev.IdentityKey.Pub),
		concat([]byte{6, 1}, dev.Account.Details, dev.IdentityKey.Pub[:], dev.Account.AccountSignatureKey),
		*(*[64]byte)(dev.Account.DeviceSignature)) {
		t.Fatal("deviceSignature leída no verifica")
	}

	// Pre-keys
	var n, up int
	if err := db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE uploaded) FROM whatsmeow_pre_keys WHERE jid=$1`, s.jid()).Scan(&n, &up); err != nil {
		t.Fatal(err)
	}
	if n != len(s.PreKeys) || up != 24 {
		t.Fatalf("pre-keys: total=%d subidas=%d", n, up)
	}
	for id, k := range s.PreKeys {
		pk, err := dev.PreKeys.GetPreKey(ctx, id)
		if err != nil || pk == nil {
			t.Fatalf("GetPreKey(%d): %v", id, err)
		}
		eq(fmt.Sprintf("pre-key %d priv", id), pk.Priv[:], k.Priv[:])
		eq(fmt.Sprintf("pre-key %d pub", id), pk.Pub[:], k.Pub[:])
	}
	if c, _ := dev.PreKeys.UploadedPreKeyCount(ctx); c != 24 {
		t.Fatalf("UploadedPreKeyCount=%d", c)
	}
	// La siguiente que genere whatsmeow no pisa ids de Baileys.
	next, err := dev.PreKeys.GenOnePreKey(ctx)
	if err != nil || next.KeyID != 31 {
		t.Fatalf("GenOnePreKey id=%v err=%v", next, err)
	}
	if err := dev.PreKeys.RemovePreKey(ctx, next.KeyID); err != nil {
		t.Fatal(err)
	}

	// App-state sync keys
	for _, k := range s.AppKeys {
		got, err := dev.AppStateKeys.GetAppStateSyncKey(ctx, k.ID)
		if err != nil || got == nil {
			t.Fatalf("GetAppStateSyncKey: %v", err)
		}
		eq("app-state keyData", got.Data, k.Data)
		if got.Timestamp != k.TimestampMS {
			t.Fatalf("app-state timestamp %d", got.Timestamp)
		}
		var fp waE2E.AppStateSyncKeyFingerprint
		if err := proto.Unmarshal(got.Fingerprint, &fp); err != nil {
			t.Fatal(err)
		}
		if fp.GetRawID() != k.RawID || fp.GetCurrentIndex() != k.Cur || fmt.Sprint(fp.GetDeviceIndexes()) != fmt.Sprint(k.DevIdx) {
			t.Fatalf("fingerprint distinto")
		}
	}
	latest, err := dev.AppStateKeys.GetLatestAppStateSyncKeyID(ctx)
	if err != nil || !bytes.Equal(latest, s.AppKeys[len(s.AppKeys)-1].ID) {
		t.Fatalf("GetLatestAppStateSyncKeyID no es la última")
	}

	// LID map (propio + contactos)
	for pn, l := range map[string]string{s.PNUser: s.LIDUser, "51900000111": "200000000000111", "51900000222": "200000000000222"} {
		got, err := container.LIDMap.GetLIDForPN(ctx, types.JID{User: pn, Server: types.DefaultUserServer})
		if err != nil || got.User != l {
			t.Fatalf("GetLIDForPN(%s)=%s err=%v", pn, got, err)
		}
	}

	// Identidades: cuenta principal + contactos (sacadas de sesiones)
	idOf := func(addr string) []byte {
		var b []byte
		if err := db.QueryRowContext(ctx, `SELECT identity FROM whatsmeow_identity_keys WHERE our_jid=$1 AND their_id=$2`, s.jid(), addr).Scan(&b); err != nil {
			t.Fatalf("identidad %s: %v", addr, err)
		}
		return b
	}
	mainAddr := types.JID{User: s.LIDUser, Server: types.HiddenUserServer}.SignalAddress().String()
	eq("identidad cuenta principal", idOf(mainAddr), s.Account.Pub[:])
	eq("identidad contacto PN", idOf("51900000111:0"), s.Sessions[0].Remote.Pub[:])
	eq("identidad contacto LID", idOf("200000000000222_1:3"), s.Sessions[1].Remote.Pub[:])
	var nsess int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM whatsmeow_sessions WHERE our_jid=$1`, s.jid()).Scan(&nsess)
	if nsess != 0 {
		t.Fatalf("no debería haber sesiones importadas: %d", nsess)
	}

	// Privacy tokens: PN con mapeo → guardado por LID
	checkTok := func(user types.JID, want []byte, storedAs string) {
		t.Helper()
		pt, err := dev.PrivacyTokens.GetPrivacyToken(ctx, user)
		if err != nil || pt == nil {
			t.Fatalf("GetPrivacyToken(%s): %v", user, err)
		}
		eq("tctoken "+user.String(), pt.Token, want)
		if pt.Timestamp.Unix() != s.TCTokenTS {
			t.Fatalf("tctoken ts %d", pt.Timestamp.Unix())
		}
		var c int
		_ = db.QueryRowContext(ctx, `SELECT count(*) FROM whatsmeow_privacy_tokens WHERE our_jid=$1 AND their_jid=$2`, s.jid(), storedAs).Scan(&c)
		if c != 1 {
			t.Fatalf("tctoken no guardado como %s", storedAs)
		}
	}
	checkTok(types.JID{User: "200000000000111", Server: types.HiddenUserServer}, s.TCTokens["51900000111@s.whatsapp.net"], "200000000000111@lid")
	checkTok(types.JID{User: "200000000000333", Server: types.HiddenUserServer}, s.TCTokens["200000000000333@lid"], "200000000000333@lid")
	checkTok(types.JID{User: "51900000999", Server: types.DefaultUserServer}, s.TCTokens["51900000999@s.whatsapp.net"], "51900000999@s.whatsapp.net")
	var ntok int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM whatsmeow_privacy_tokens WHERE our_jid=$1`, s.jid()).Scan(&ntok)
	if ntok != 3 {
		t.Fatalf("privacy tokens=%d", ntok)
	}
}

// ---------------------------------------------------------------------------
// Contraprueba con una sesión sintética generada por el código REAL de Baileys
// 7.0.0-rc.9 (initAuthCreds, configureSuccessfulPairing, getNextPreKeys,
// libsignal-node SessionBuilder) y serializada como Evolution 2.3.7. Sólo se
// ejecuta si BAILEYS_IMPORT_NODE_FIXTURE apunta al directorio con creds.txt,
// keys.txt (formato redis-cli --raw HGETALL) y expected.json.

func TestBaileysNodeFixture(t *testing.T) {
	dir := os.Getenv("BAILEYS_IMPORT_NODE_FIXTURE")
	if dir == "" {
		t.Skip("BAILEYS_IMPORT_NODE_FIXTURE no definido")
	}
	dsn := testDSN(t)
	ctx := context.Background()
	var exp struct {
		JID, LID, PushName, Platform                     string
		RegistrationID                                   uint32
		NoisePriv, IdentPriv, SpkPriv, SpkSig            string
		SpkID                                            uint32
		AdvSecret, AccDetails, AccSigKey, AccSig, DevSig string
		FirstUnuploaded, NextPreKeyID                    uint32
		PreKeys                                          map[string]string
		AppKeys                                          []struct {
			ID, Data   string
			TS         int64
			RawID, Cur uint32
		}
		Tctoken, RemoteIdentity string
	}
	raw, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatal(err)
	}
	h := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	var out bytes.Buffer
	o := options{CredsPath: filepath.Join(dir, "creds.txt"), KeysPath: filepath.Join(dir, "keys.txt"), DryRun: true}
	if err := run(ctx, o, &out); err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out.String())
	}
	t.Log("\n" + out.String())
	secrets := [][]byte{h(exp.NoisePriv), h(exp.IdentPriv), h(exp.SpkPriv), h(exp.AdvSecret), h(exp.Tctoken)}
	for _, p := range exp.PreKeys {
		secrets = append(secrets, h(p))
	}
	assertNoSecrets(t, out.String(), secrets)

	o.DryRun, o.DSN = false, dsn
	out.Reset()
	if err := run(ctx, o, &out); err != nil {
		t.Fatalf("import: %v\n%s", err, out.String())
	}

	container, db := openVerify(t, dsn)
	jid, _ := types.ParseJID(exp.JID)
	dev, err := container.GetDevice(ctx, jid)
	if err != nil || dev == nil {
		t.Fatalf("GetDevice: %v", err)
	}
	eq := func(name string, a, b []byte) {
		t.Helper()
		if !bytes.Equal(a, b) {
			t.Fatalf("%s no coincide", name)
		}
	}
	if dev.ID.String() != exp.JID || dev.LID.String() != exp.LID || dev.PushName != exp.PushName || dev.Platform != exp.Platform || dev.RegistrationID != exp.RegistrationID {
		t.Fatalf("metadatos: %s %s %q %q %d", dev.ID, dev.LID, dev.PushName, dev.Platform, dev.RegistrationID)
	}
	eq("noise", dev.NoiseKey.Priv[:], h(exp.NoisePriv))
	eq("identity", dev.IdentityKey.Priv[:], h(exp.IdentPriv))
	eq("spk", dev.SignedPreKey.Priv[:], h(exp.SpkPriv))
	eq("spk sig", dev.SignedPreKey.Signature[:], h(exp.SpkSig))
	if dev.SignedPreKey.KeyID != exp.SpkID {
		t.Fatalf("spk id %d", dev.SignedPreKey.KeyID)
	}
	if !ecc.VerifySignature(ecc.NewDjbECPublicKey(*dev.IdentityKey.Pub), append([]byte{5}, dev.SignedPreKey.Pub[:]...), *dev.SignedPreKey.Signature) {
		t.Fatal("firma spk no verifica")
	}
	eq("adv secret", dev.AdvSecretKey, h(exp.AdvSecret))
	eq("details", dev.Account.Details, h(exp.AccDetails))
	eq("acc sig key", dev.Account.AccountSignatureKey, h(exp.AccSigKey))
	eq("acc sig", dev.Account.AccountSignature, h(exp.AccSig))
	eq("dev sig", dev.Account.DeviceSignature, h(exp.DevSig))

	var n, up int
	_ = db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE uploaded) FROM whatsmeow_pre_keys WHERE jid=$1`, exp.JID).Scan(&n, &up)
	wantUp := 0
	for idStr, p := range exp.PreKeys {
		var id uint32
		fmt.Sscan(idStr, &id)
		pk, err := dev.PreKeys.GetPreKey(ctx, id)
		if err != nil || pk == nil {
			t.Fatalf("pre-key %d: %v", id, err)
		}
		eq("pre-key "+idStr, pk.Priv[:], h(p))
		if id < exp.FirstUnuploaded {
			wantUp++
		}
	}
	if n != len(exp.PreKeys) || up != wantUp {
		t.Fatalf("pre-keys total=%d (quería %d) subidas=%d (quería %d)", n, len(exp.PreKeys), up, wantUp)
	}
	for _, k := range exp.AppKeys {
		got, err := dev.AppStateKeys.GetAppStateSyncKey(ctx, h(k.ID))
		if err != nil || got == nil {
			t.Fatalf("app key: %v", err)
		}
		eq("app key data", got.Data, h(k.Data))
		var fp waE2E.AppStateSyncKeyFingerprint
		_ = proto.Unmarshal(got.Fingerprint, &fp)
		if got.Timestamp != k.TS || fp.GetRawID() != k.RawID || fp.GetCurrentIndex() != k.Cur {
			t.Fatalf("app key meta")
		}
	}
	l, _ := container.LIDMap.GetLIDForPN(ctx, types.JID{User: "51944455566", Server: types.DefaultUserServer})
	if l.User != "300000000000088" {
		t.Fatalf("lid map %s", l)
	}
	pt, _ := dev.PrivacyTokens.GetPrivacyToken(ctx, types.JID{User: "300000000000088", Server: types.HiddenUserServer})
	if pt == nil {
		t.Fatal("tctoken ausente")
	}
	eq("tctoken", pt.Token, h(exp.Tctoken))
	var ident []byte
	if err := db.QueryRowContext(ctx, `SELECT identity FROM whatsmeow_identity_keys WHERE our_jid=$1 AND their_id=$2`, exp.JID, "300000000000088_1:2").Scan(&ident); err != nil {
		t.Fatal(err)
	}
	eq("identidad contacto (de la sesión libsignal-node)", ident, h(exp.RemoteIdentity))
}
