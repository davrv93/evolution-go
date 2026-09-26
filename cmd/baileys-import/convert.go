package main

// Conversión de las credenciales de Baileys (Evolution API v2.3.x) a los
// tipos de whatsmeow. Nada de este archivo toca la red ni la base.
//
// Formato de creds confirmado en Baileys 7.0.0-rc.9 (src/Types/Auth.ts,
// AuthenticationCreds; src/Utils/auth-utils.ts, initAuthCreds;
// src/Utils/validate-connection.ts, configureSuccessfulPairing):
//
//	noiseKey          KeyPair {private, public}   (public SIN prefijo 0x05: 32 bytes)
//	signedIdentityKey KeyPair
//	signedPreKey      {keyPair, signature (64), keyId}
//	                  firma = XEdDSA(identity.priv, 0x05 || spk.pub)  (crypto.ts, signedKeyPair)
//	registrationId    number
//	advSecretKey      string base64 (32 bytes)
//	me                {id: "<pn>:<dev>@s.whatsapp.net", lid: "<lid>:<dev>@lid", name}
//	account           ADVSignedDeviceIdentity {details, accountSignatureKey,
//	                  accountSignature, deviceSignature} — protobufjs toJSON → base64
//	platform          string ("smba", "android", "iphone"...)
//	nextPreKeyId / firstUnuploadedPreKeyId  numbers (src/Utils/signal.ts)
//
// whatsmeow guarda llaves de 32 bytes (*[32]byte) sin prefijo; se acepta la
// pública de 32 o de 33 bytes con 0x05 y se descarta el prefijo.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/libsignal/ecc"
	"golang.org/x/crypto/curve25519"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
)

// ---------------------------------------------------------------------------
// Tipos de entrada (JSON de Baileys)

type baileysKeyPair struct {
	Private bjBytes `json:"private"`
	Public  bjBytes `json:"public"`
}

type baileysSignedKeyPair struct {
	KeyPair   baileysKeyPair `json:"keyPair"`
	Signature bjBytes        `json:"signature"`
	KeyID     flexUint       `json:"keyId"`
}

type baileysContact struct {
	ID   string `json:"id"`
	LID  string `json:"lid"`
	Name string `json:"name"`
}

type baileysAccount struct {
	Details             bjBytes `json:"details"`
	AccountSignatureKey bjBytes `json:"accountSignatureKey"`
	AccountSignature    bjBytes `json:"accountSignature"`
	DeviceSignature     bjBytes `json:"deviceSignature"`
}

type baileysCreds struct {
	NoiseKey                baileysKeyPair       `json:"noiseKey"`
	SignedIdentityKey       baileysKeyPair       `json:"signedIdentityKey"`
	SignedPreKey            baileysSignedKeyPair `json:"signedPreKey"`
	RegistrationID          flexUint             `json:"registrationId"`
	AdvSecretKey            bjBytes              `json:"advSecretKey"`
	Me                      *baileysContact      `json:"me"`
	Account                 *baileysAccount      `json:"account"`
	Platform                string               `json:"platform"`
	MyAppStateKeyID         string               `json:"myAppStateKeyId"`
	NextPreKeyID            flexUint             `json:"nextPreKeyId"`
	FirstUnuploadedPreKeyID flexUint             `json:"firstUnuploadedPreKeyId"`
}

type baileysAppStateSyncKeyData struct {
	KeyData     bjBytes `json:"keyData"`
	Fingerprint *struct {
		RawID         *flexUint  `json:"rawId"`
		CurrentIndex  *flexUint  `json:"currentIndex"`
		DeviceIndexes []flexUint `json:"deviceIndexes"`
	} `json:"fingerprint"`
	Timestamp flexUint `json:"timestamp"`
}

type baileysTCToken struct {
	Token     bjBytes  `json:"token"`
	Timestamp flexUint `json:"timestamp"`
}

// Sesión de libsignal-node (src/session_record.js, SessionRecord.serialize):
// {_sessions: {<baseKey b64>: {indexInfo: {remoteIdentityKey (b64, 33 B),
// closed (-1 = abierta), created, ...}, currentRatchet, _chains, ...}}, version}
type nodeSessionRecord struct {
	Sessions map[string]struct {
		IndexInfo struct {
			RemoteIdentityKey bjBytes `json:"remoteIdentityKey"`
			Closed            float64 `json:"closed"`
			Created           float64 `json:"created"`
		} `json:"indexInfo"`
	} `json:"_sessions"`
}

// ---------------------------------------------------------------------------
// Resultado de la conversión

type preKeyItem struct {
	ID       uint32
	Priv     [32]byte
	Uploaded bool
}

type appStateKeyItem struct {
	ID  []byte
	Key store.AppStateSyncKey
}

type identityItem struct {
	Address string
	Key     [32]byte
}

type converted struct {
	Device *store.Device // sin Container: se rellena al escribir

	// Identidad de la cuenta principal (lo que whatsmeow guarda al emparejar:
	// pair.go, PutIdentity(mainDeviceLID.SignalAddress(), accountSignatureKey)).
	MainIdentity identityItem

	PreKeys       []preKeyItem
	AppStateKeys  []appStateKeyItem
	LIDMappings   []store.LIDMapping
	PrivacyTokens []store.PrivacyToken
	Identities    []identityItem

	Summary summary
}

type checkResult struct {
	Name string
	OK   bool
}

type summary struct {
	JID, LID, Platform, PushName string
	PushNameFromFlag             bool
	RegistrationID               uint32
	SignedPreKeyID               uint32
	NextPreKeyID                 uint32
	FirstUnuploadedPreKeyID      uint32
	HasMyAppStateKeyID           bool
	MyAppStateKeyPresent         bool
	Hosted                       bool
	KeyIndex                     uint32

	Checks []checkResult

	KeysProvided        bool
	TotalFields         int
	PreKeys             int
	PreKeysUploaded     int
	PreKeysBad          int
	AppStateKeys        int
	AppStateKeysBad     int
	LIDPairs            int
	LIDBad              int
	PrivacyTokens       int
	PrivacyTokensNoTS   int
	PrivacyTokensBad    int
	Identities          int
	IdentitiesBad       int
	SkippedSessions     int
	SkippedSenderKeys   int
	SkippedSenderKeyMem int
	SkippedAppStateVer  int
	SkippedDeviceList   int
	SkippedOther        int
	OtherPrefixes       map[string]int
}

func (s *summary) addCheck(name string, ok bool) {
	s.Checks = append(s.Checks, checkResult{Name: name, OK: ok})
}

func (s *summary) allChecksOK() bool {
	for _, c := range s.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Utilidades de llaves

// pub32 normaliza una pública curve25519: 32 bytes, o 33 con prefijo 0x05.
func pub32(b []byte) ([32]byte, bool) {
	var out [32]byte
	switch {
	case len(b) == 32:
		copy(out[:], b)
		return out, true
	case len(b) == 33 && b[0] == ecc.DjbType:
		copy(out[:], b[1:])
		return out, true
	}
	return out, false
}

func priv32(b []byte) ([32]byte, bool) {
	var out [32]byte
	if len(b) != 32 {
		return out, false
	}
	copy(out[:], b)
	return out, true
}

func derivePub(priv [32]byte) [32]byte {
	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &priv)
	return pub
}

// keyPairFrom valida longitudes y que la pública guardada (si viene) sea la
// derivada de la privada. Devuelve el KeyPair de whatsmeow.
func keyPairFrom(kp baileysKeyPair) (*keys.KeyPair, bool, error) {
	priv, ok := priv32(kp.Private)
	if !ok {
		return nil, false, errors.New("privada de longitud distinta de 32 bytes")
	}
	derived := derivePub(priv)
	match := true
	if len(kp.Public) > 0 {
		stored, ok := pub32(kp.Public)
		if !ok {
			return nil, false, errors.New("pública de longitud no válida (se esperan 32 o 33 bytes con 0x05)")
		}
		match = stored == derived
	}
	return keys.NewKeyPairFromPrivateKey(priv), match, nil
}

func verifySig(pub [32]byte, msg []byte, sig []byte) bool {
	if len(sig) != 64 {
		return false
	}
	return ecc.VerifySignature(ecc.NewDjbECPublicKey(pub), msg, *(*[64]byte)(sig))
}

func concat(parts ...[]byte) []byte {
	var buf bytes.Buffer
	for _, p := range parts {
		buf.Write(p)
	}
	return buf.Bytes()
}

var (
	advAccountSigPrefix       = []byte{6, 0}
	advDeviceSigPrefix        = []byte{6, 1}
	advHostedAccountSigPrefix = []byte{6, 5}
)

// ---------------------------------------------------------------------------
// Conversión de creds

type convertOptions struct {
	PushName          string
	PushNameSet       bool
	LIDMigrationTS    int64
	SkipIdentities    bool
	SkipPrivacyTokens bool
	SkipLIDMappings   bool
}

func parseCreds(data []byte) (*baileysCreds, error) {
	inner, err := unwrapJSONString(data)
	if err != nil {
		return nil, fmt.Errorf("--creds: %v", err)
	}
	var c baileysCreds
	if err := json.Unmarshal(inner, &c); err != nil {
		// json.Unmarshal puede citar el valor en el error; no se propaga.
		return nil, errors.New("--creds: estructura no reconocida (¿es la columna Session.creds de Evolution?)")
	}
	return &c, nil
}

func convertCreds(c *baileysCreds, opts convertOptions) (*converted, error) {
	out := &converted{}
	s := &out.Summary

	if c.Me == nil || c.Me.ID == "" {
		return nil, errors.New("creds.me.id vacío: la sesión nunca terminó de emparejarse")
	}
	if c.Account == nil {
		return nil, errors.New("creds.account ausente: la sesión nunca terminó de emparejarse")
	}

	jid, err := types.ParseJID(c.Me.ID)
	if err != nil || jid.User == "" {
		return nil, errors.New("creds.me.id no es un JID válido")
	}
	if jid.Server != types.DefaultUserServer {
		return nil, fmt.Errorf("creds.me.id con servidor inesperado %q (se espera s.whatsapp.net)", jid.Server)
	}
	if jid.Device == 0 {
		return nil, errors.New("creds.me.id sin número de dispositivo (:N); un dispositivo vinculado siempre lo tiene")
	}
	var lid types.JID
	if c.Me.LID != "" {
		lid, err = types.ParseJID(c.Me.LID)
		if err != nil || lid.User == "" || lid.Server != types.HiddenUserServer {
			return nil, errors.New("creds.me.lid no es un JID @lid válido")
		}
		if lid.Device == 0 {
			// whatsmeow guarda el LID con el mismo número de dispositivo.
			lid.Device = jid.Device
		}
	}

	noise, noiseOK, err := keyPairFrom(c.NoiseKey)
	if err != nil {
		return nil, fmt.Errorf("creds.noiseKey: %v", err)
	}
	ident, identOK, err := keyPairFrom(c.SignedIdentityKey)
	if err != nil {
		return nil, fmt.Errorf("creds.signedIdentityKey: %v", err)
	}
	spkKP, spkOK, err := keyPairFrom(c.SignedPreKey.KeyPair)
	if err != nil {
		return nil, fmt.Errorf("creds.signedPreKey.keyPair: %v", err)
	}
	if len(c.SignedPreKey.Signature) != 64 {
		return nil, errors.New("creds.signedPreKey.signature no mide 64 bytes")
	}
	if c.SignedPreKey.KeyID >= 1<<24 {
		return nil, errors.New("creds.signedPreKey.keyId fuera de rango (whatsmeow exige < 2^24)")
	}
	if c.RegistrationID > 0xFFFFFFFF {
		return nil, errors.New("creds.registrationId fuera de rango")
	}
	if len(c.AdvSecretKey) == 0 {
		return nil, errors.New("creds.advSecretKey vacío")
	}

	acc := c.Account
	if len(acc.Details) == 0 {
		return nil, errors.New("creds.account.details vacío")
	}
	if len(acc.AccountSignatureKey) != 32 {
		return nil, errors.New("creds.account.accountSignatureKey no mide 32 bytes")
	}
	if len(acc.AccountSignature) != 64 {
		return nil, errors.New("creds.account.accountSignature no mide 64 bytes")
	}
	if len(acc.DeviceSignature) != 64 {
		return nil, errors.New("creds.account.deviceSignature no mide 64 bytes")
	}
	var details waAdv.ADVDeviceIdentity
	if err := proto.Unmarshal(acc.Details, &details); err != nil {
		return nil, errors.New("creds.account.details no es un ADVDeviceIdentity válido")
	}
	s.Hosted = details.GetDeviceType() == waAdv.ADVEncryptionType_HOSTED
	s.KeyIndex = details.GetKeyIndex()

	// Comprobaciones criptográficas (mismas que hacen Baileys y whatsmeow al
	// emparejar: validate-connection.ts / pair.go).
	s.addCheck("noiseKey: pública = derivada de la privada", noiseOK)
	s.addCheck("signedIdentityKey: pública = derivada de la privada", identOK)
	s.addCheck("signedPreKey: pública = derivada de la privada", spkOK)
	spkMsg := concat([]byte{ecc.DjbType}, spkKP.Pub[:])
	s.addCheck("signedPreKey: firma verifica con signedIdentityKey", verifySig(*ident.Pub, spkMsg, c.SignedPreKey.Signature))
	accPrefix := advAccountSigPrefix
	if s.Hosted {
		accPrefix = advHostedAccountSigPrefix
	}
	s.addCheck("account.accountSignature verifica (cuenta principal firmó esta identidad)",
		verifySig(*(*[32]byte)(acc.AccountSignatureKey), concat(accPrefix, acc.Details, ident.Pub[:]), acc.AccountSignature))
	s.addCheck("account.deviceSignature verifica con signedIdentityKey",
		verifySig(*ident.Pub, concat(advDeviceSigPrefix, acc.Details, ident.Pub[:], acc.AccountSignatureKey), acc.DeviceSignature))

	pushName := c.Me.Name
	if opts.PushNameSet {
		pushName = opts.PushName
		s.PushNameFromFlag = true
	}

	spk := &keys.PreKey{
		KeyPair:   *spkKP,
		KeyID:     uint32(c.SignedPreKey.KeyID),
		Signature: (*[64]byte)(bytes.Clone(c.SignedPreKey.Signature)),
	}

	jidCopy := jid
	dev := &store.Device{
		NoiseKey:       noise,
		IdentityKey:    ident,
		SignedPreKey:   spk,
		RegistrationID: uint32(c.RegistrationID),
		AdvSecretKey:   bytes.Clone(c.AdvSecretKey),
		ID:             &jidCopy,
		LID:            lid,
		Account: &waAdv.ADVSignedDeviceIdentity{
			Details:             bytes.Clone(acc.Details),
			AccountSignatureKey: bytes.Clone(acc.AccountSignatureKey),
			AccountSignature:    bytes.Clone(acc.AccountSignature),
			DeviceSignature:     bytes.Clone(acc.DeviceSignature),
		},
		Platform:              c.Platform,
		PushName:              pushName,
		LIDMigrationTimestamp: opts.LIDMigrationTS,
	}
	out.Device = dev

	mainDev := lid
	if mainDev.IsEmpty() {
		mainDev = jid
	}
	mainDev.Device = 0
	out.MainIdentity = identityItem{
		Address: mainDev.SignalAddress().String(),
		Key:     *(*[32]byte)(acc.AccountSignatureKey),
	}
	if !lid.IsEmpty() && !opts.SkipLIDMappings {
		out.LIDMappings = append(out.LIDMappings, store.LIDMapping{
			LID: types.JID{User: lid.User, Server: types.HiddenUserServer},
			PN:  types.JID{User: jid.User, Server: types.DefaultUserServer},
		})
	}

	s.JID = jid.String()
	if !lid.IsEmpty() {
		s.LID = lid.String()
	}
	s.Platform = c.Platform
	s.PushName = pushName
	s.RegistrationID = uint32(c.RegistrationID)
	s.SignedPreKeyID = uint32(c.SignedPreKey.KeyID)
	s.NextPreKeyID = uint32(c.NextPreKeyID)
	s.FirstUnuploadedPreKeyID = uint32(c.FirstUnuploadedPreKeyID)
	s.HasMyAppStateKeyID = c.MyAppStateKeyID != ""

	if !s.allChecksOK() {
		// Se devuelve también el resultado para que --dry-run pueda mostrar
		// qué comprobación falló.
		return out, errChecksFailed
	}
	return out, nil
}

var errChecksFailed = errors.New("alguna comprobación criptográfica de las creds falló; no se importa nada")

// ---------------------------------------------------------------------------
// Conversión del volcado de llaves (hash de Redis)
//
// Nombres de campo: `${type}-${id}` (use-multi-file-auth-state-prisma.ts,
// keys.set), con los tipos de SignalDataTypeMap (src/Types/Auth.ts):
// pre-key, session, sender-key, sender-key-memory, app-state-sync-key,
// app-state-sync-version, lid-mapping, device-list, tctoken.

const (
	pfxPreKey          = "pre-key-"
	pfxSession         = "session-"
	pfxSenderKeyMemory = "sender-key-memory-"
	pfxSenderKey       = "sender-key-"
	pfxAppStateKey     = "app-state-sync-key-"
	pfxAppStateVersion = "app-state-sync-version-"
	pfxLIDMapping      = "lid-mapping-"
	pfxDeviceList      = "device-list-"
	pfxTCToken         = "tctoken-"
)

// Orden importante: los prefijos largos antes que los cortos que los contienen.
var knownPrefixes = []string{
	pfxPreKey, pfxSession, pfxSenderKeyMemory, pfxSenderKey, pfxAppStateKey,
	pfxAppStateVersion, pfxLIDMapping, pfxDeviceList, pfxTCToken, "creds",
}

// nodeAddrToWhatsmeow: libsignal-node ProtocolAddress.toString() es
// "<id>.<device>" (protocol_address.js); go.mau.fi/libsignal usa
// "<id>:<device>" (SignalProtocolAddress.go). El <id> ya lleva el sufijo de
// dominio (_1 para LID) igual en ambos (Baileys jidToSignalProtocolAddress /
// whatsmeow JID.SignalAddressUser).
func nodeAddrToWhatsmeow(addr string) (string, bool) {
	i := strings.LastIndexByte(addr, '.')
	if i <= 0 || i == len(addr)-1 {
		return "", false
	}
	if _, err := strconv.ParseUint(addr[i+1:], 10, 16); err != nil {
		return "", false
	}
	return addr[:i] + ":" + addr[i+1:], true
}

func convertKeys(out *converted, fields map[string]json.RawMessage, firstUnuploaded uint32, myAppStateKeyID string, opts convertOptions) {
	s := &out.Summary
	s.KeysProvided = true
	s.TotalFields = len(fields)
	s.OtherPrefixes = map[string]int{}

	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)

	lidByPN := map[string]string{}
	for _, m := range out.LIDMappings {
		lidByPN[m.PN.User] = m.LID.User
	}
	type pendingToken struct {
		jid types.JID
		tok baileysTCToken
	}
	var tokens []pendingToken

	for _, field := range names {
		val := fields[field]
		switch {
		case strings.HasPrefix(field, pfxPreKey):
			id, err := strconv.ParseUint(strings.TrimPrefix(field, pfxPreKey), 10, 32)
			var kp baileysKeyPair
			if err != nil || id >= 1<<24 || json.Unmarshal(val, &kp) != nil {
				s.PreKeysBad++
				continue
			}
			priv, ok := priv32(kp.Private)
			if !ok {
				s.PreKeysBad++
				continue
			}
			if len(kp.Public) > 0 {
				stored, ok := pub32(kp.Public)
				if !ok || stored != derivePub(priv) {
					s.PreKeysBad++
					continue
				}
			}
			// Baileys adelanta firstUnuploadedPreKeyId al generar el lote que
			// va a subir (signal.ts, getNextPreKeys): id < firstUnuploaded ⇒
			// ya se subió al servidor.
			uploaded := firstUnuploaded == 0 || uint32(id) < firstUnuploaded
			out.PreKeys = append(out.PreKeys, preKeyItem{ID: uint32(id), Priv: priv, Uploaded: uploaded})
			s.PreKeys++
			if uploaded {
				s.PreKeysUploaded++
			}

		case strings.HasPrefix(field, pfxAppStateKey):
			idStr := strings.TrimPrefix(field, pfxAppStateKey)
			keyID, err := decodeB64(idStr)
			var d baileysAppStateSyncKeyData
			if err != nil || len(keyID) == 0 || json.Unmarshal(val, &d) != nil || len(d.KeyData) == 0 {
				s.AppStateKeysBad++
				continue
			}
			fp := &waE2E.AppStateSyncKeyFingerprint{}
			if d.Fingerprint != nil {
				if d.Fingerprint.RawID != nil {
					fp.RawID = proto.Uint32(uint32(*d.Fingerprint.RawID))
				}
				if d.Fingerprint.CurrentIndex != nil {
					fp.CurrentIndex = proto.Uint32(uint32(*d.Fingerprint.CurrentIndex))
				}
				for _, di := range d.Fingerprint.DeviceIndexes {
					fp.DeviceIndexes = append(fp.DeviceIndexes, uint32(di))
				}
			}
			// Igual que whatsmeow (message.go, handleAppStateSyncKeyShare):
			// guarda proto.Marshal(fingerprint).
			fpBytes, err := proto.Marshal(fp)
			if err != nil {
				s.AppStateKeysBad++
				continue
			}
			out.AppStateKeys = append(out.AppStateKeys, appStateKeyItem{
				ID:  keyID,
				Key: store.AppStateSyncKey{Data: bytes.Clone(d.KeyData), Fingerprint: fpBytes, Timestamp: int64(d.Timestamp)},
			})
			s.AppStateKeys++
			if idStr == myAppStateKeyID {
				s.MyAppStateKeyPresent = true
			}

		case strings.HasPrefix(field, pfxLIDMapping):
			if opts.SkipLIDMappings {
				continue
			}
			id := strings.TrimPrefix(field, pfxLIDMapping)
			if strings.HasSuffix(id, "_reverse") {
				// Entrada inversa (lid → pn); la directa trae el mismo par.
				// Si faltara la directa, se usa ésta.
				lidUser := strings.TrimSuffix(id, "_reverse")
				pnUser, ok := lidMappingValue(val)
				if !ok || !isDigits(lidUser) {
					s.LIDBad++
					continue
				}
				if _, seen := lidByPN[pnUser]; !seen {
					lidByPN[pnUser] = lidUser
				}
				continue
			}
			lidUser, ok := lidMappingValue(val)
			if !ok || !isDigits(id) {
				s.LIDBad++
				continue
			}
			lidByPN[id] = lidUser

		case strings.HasPrefix(field, pfxTCToken):
			if opts.SkipPrivacyTokens {
				continue
			}
			j, err := types.ParseJID(strings.TrimPrefix(field, pfxTCToken))
			var t baileysTCToken
			if err != nil || j.User == "" || json.Unmarshal(val, &t) != nil || len(t.Token) == 0 {
				s.PrivacyTokensBad++
				continue
			}
			if t.Timestamp == 0 {
				// whatsmeow purga por antigüedad; sin fecha no hay forma
				// honesta de fecharlo. Se omite (el contacto lo reenvía).
				s.PrivacyTokensNoTS++
				continue
			}
			tokens = append(tokens, pendingToken{jid: j.ToNonAD(), tok: t})

		case strings.HasPrefix(field, pfxSession):
			s.SkippedSessions++
			if opts.SkipIdentities {
				continue
			}
			addr, ok := nodeAddrToWhatsmeow(strings.TrimPrefix(field, pfxSession))
			var rec nodeSessionRecord
			if !ok || json.Unmarshal(val, &rec) != nil || len(rec.Sessions) == 0 {
				s.IdentitiesBad++
				continue
			}
			// Toma la identidad de la sesión abierta (closed == -1) o, si no
			// hay, de la más reciente.
			var best []byte
			bestOpen := false
			bestCreated := -1.0
			for _, e := range rec.Sessions {
				open := e.IndexInfo.Closed == -1
				if best == nil || (open && !bestOpen) || (open == bestOpen && e.IndexInfo.Created > bestCreated) {
					best, bestOpen, bestCreated = e.IndexInfo.RemoteIdentityKey, open, e.IndexInfo.Created
				}
			}
			k, ok := pub32(best)
			if !ok {
				s.IdentitiesBad++
				continue
			}
			out.Identities = append(out.Identities, identityItem{Address: addr, Key: k})
			s.Identities++

		case strings.HasPrefix(field, pfxSenderKeyMemory):
			s.SkippedSenderKeyMem++
		case strings.HasPrefix(field, pfxSenderKey):
			s.SkippedSenderKeys++
		case strings.HasPrefix(field, pfxAppStateVersion):
			s.SkippedAppStateVer++
		case strings.HasPrefix(field, pfxDeviceList):
			s.SkippedDeviceList++
		default:
			s.SkippedOther++
			p := field
			if i := strings.IndexByte(p, '-'); i > 0 {
				p = p[:i]
			}
			if len(p) > 24 {
				p = p[:24]
			}
			s.OtherPrefixes[p]++
		}
	}

	if !opts.SkipLIDMappings {
		// Reconstruye la lista (incluye el par propio de convertCreds).
		pns := make([]string, 0, len(lidByPN))
		for pn := range lidByPN {
			pns = append(pns, pn)
		}
		sort.Strings(pns)
		out.LIDMappings = out.LIDMappings[:0]
		seenLID := map[string]bool{}
		for _, pn := range pns {
			l := lidByPN[pn]
			if seenLID[l] {
				// whatsmeow_lid_map.lid es PK y pn UNIQUE: un LID con dos PN
				// es incoherente; se queda el primero.
				s.LIDBad++
				continue
			}
			seenLID[l] = true
			out.LIDMappings = append(out.LIDMappings, store.LIDMapping{
				LID: types.JID{User: l, Server: types.HiddenUserServer},
				PN:  types.JID{User: pn, Server: types.DefaultUserServer},
			})
		}
		s.LIDPairs = len(out.LIDMappings)
	}

	// whatsmeow guarda los tctoken por LID (tctoken.go,
	// resolveTCTokenStorageLID): PN → LID si hay mapeo.
	seenTok := map[types.JID]int{}
	for _, pt := range tokens {
		user := pt.jid
		if user.Server == types.DefaultUserServer {
			if l, ok := lidByPN[user.User]; ok {
				user = types.JID{User: l, Server: types.HiddenUserServer}
			}
		}
		tok := store.PrivacyToken{
			User:      user,
			Token:     bytes.Clone(pt.tok.Token),
			Timestamp: time.Unix(int64(pt.tok.Timestamp), 0),
		}
		if i, dup := seenTok[user]; dup {
			// Mismo contacto por PN y por LID: gana el más reciente.
			if tok.Timestamp.After(out.PrivacyTokens[i].Timestamp) {
				out.PrivacyTokens[i] = tok
			}
			continue
		}
		seenTok[user] = len(out.PrivacyTokens)
		out.PrivacyTokens = append(out.PrivacyTokens, tok)
	}
	s.PrivacyTokens = len(out.PrivacyTokens)
}

func lidMappingValue(val json.RawMessage) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(val))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return "", false
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case json.Number:
		s = x.String()
	default:
		return "", false
	}
	if !isDigits(s) {
		return "", false
	}
	return s, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
