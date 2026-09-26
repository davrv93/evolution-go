package main

// Escritura en la base evogo_auth con el sqlstore de whatsmeow, igual que lo
// abre evolution-go (pkg/whatsmeow/service/whatsmeow.go:
// sqlstore.NewWithDB(authDB, "postgres", logger) + Upgrade).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/lib/pq"
	"go.mau.fi/util/dbutil"

	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/store/sqlstore/upgrades"
	"go.mau.fi/whatsmeow/types"
)

var errDeviceExists = errors.New("ya existe un dispositivo con ese JID en whatsmeow_device; usa --replace para sustituirlo (borra sus llaves y sesiones)")

type authStore struct {
	sqlDB     *sql.DB
	db        *dbutil.Database
	container *sqlstore.Container
}

// sanitizeDBError evita que un error de conexión repita el DSN (que lleva la
// contraseña). Sólo se dejan pasar errores del servidor Postgres y de red.
func sanitizeDBError(what string, err error) error {
	if err == nil {
		return nil
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return fmt.Errorf("%s: postgres %s (%s)", what, pqErr.Code, pqErr.Message)
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return fmt.Errorf("%s: error de red (%s %s)", what, netErr.Op, netErr.Net)
	}
	return fmt.Errorf("%s: falló (detalle omitido para no mostrar el DSN)", what)
}

func openAuthStore(ctx context.Context, dsn string) (*authStore, error) {
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, sanitizeDBError("abrir --dsn", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, sanitizeDBError("conectar a --dsn", err)
	}
	wrapped, err := dbutil.NewWithDB(sqlDB, "postgres")
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	wrapped.UpgradeTable = upgrades.Table
	wrapped.VersionTable = "whatsmeow_version"
	return &authStore{
		sqlDB:     sqlDB,
		db:        wrapped,
		container: sqlstore.NewWithWrappedDB(wrapped, nil),
	}, nil
}

func (a *authStore) Close() error { return a.sqlDB.Close() }

func (a *authStore) upgrade(ctx context.Context) error {
	return sanitizeDBError("Upgrade del esquema whatsmeow", a.container.Upgrade(ctx))
}

// probe informa, sin escribir, si ya hay dispositivo con ese JID y cuántos
// otros dispositivos hay del mismo número (evolution-go, ForceUpdateJid, elige
// entre ellos por LIKE '%<número>%').
func (a *authStore) probe(ctx context.Context, jid types.JID) (exists bool, sameUser []string, schema bool, err error) {
	var reg sql.NullString
	if err = a.sqlDB.QueryRowContext(ctx, `SELECT to_regclass('whatsmeow_device')::text`).Scan(&reg); err != nil {
		return false, nil, false, sanitizeDBError("consultar esquema", err)
	}
	if !reg.Valid {
		return false, nil, false, nil
	}
	rows, err := a.sqlDB.QueryContext(ctx, `SELECT jid FROM whatsmeow_device WHERE jid = $1 OR jid LIKE $2 OR jid = $3`,
		jid.String(), jid.User+":%", jid.User+"@"+types.DefaultUserServer)
	if err != nil {
		return false, nil, true, sanitizeDBError("consultar whatsmeow_device", err)
	}
	defer rows.Close()
	for rows.Next() {
		var j string
		if err = rows.Scan(&j); err != nil {
			return false, nil, true, sanitizeDBError("leer whatsmeow_device", err)
		}
		if j == jid.String() {
			exists = true
		} else {
			sameUser = append(sameUser, j)
		}
	}
	return exists, sameUser, true, sanitizeDBError("leer whatsmeow_device", rows.Err())
}

const preKeyBatch = 500

// write importa todo en UNA transacción: o entra completo o no entra nada.
func (a *authStore) write(ctx context.Context, conv *converted, replace bool) error {
	jid := *conv.Device.ID
	existing, err := a.container.GetDevice(ctx, jid)
	if err != nil {
		return sanitizeDBError("buscar dispositivo existente", err)
	}
	if existing != nil && !replace {
		return errDeviceExists
	}

	return a.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		if existing != nil {
			// PutDevice hace ON CONFLICT(jid) DO UPDATE sólo de lid/platform/
			// nombres: NO sustituiría las llaves. Hay que borrar primero. El
			// borrado arrastra (ON DELETE CASCADE) prekeys, sesiones,
			// identidades, app-state... pero no whatsmeow_privacy_tokens,
			// que no tiene FK.
			if err := a.container.DeleteDevice(ctx, existing); err != nil {
				return sanitizeDBError("borrar dispositivo existente", err)
			}
			if _, err := a.db.Exec(ctx, `DELETE FROM whatsmeow_privacy_tokens WHERE our_jid=$1`, jid.String()); err != nil {
				return sanitizeDBError("borrar privacy tokens existentes", err)
			}
		}

		src := conv.Device
		dev := a.container.NewDevice()
		dev.NoiseKey = src.NoiseKey
		dev.IdentityKey = src.IdentityKey
		dev.SignedPreKey = src.SignedPreKey
		dev.RegistrationID = src.RegistrationID
		dev.AdvSecretKey = src.AdvSecretKey
		jidCopy := jid
		dev.ID = &jidCopy
		dev.LID = src.LID
		dev.Account = src.Account
		dev.Platform = src.Platform
		dev.BusinessName = src.BusinessName
		dev.PushName = src.PushName
		dev.LIDMigrationTimestamp = src.LIDMigrationTimestamp
		if err := dev.Save(ctx); err != nil {
			return sanitizeDBError("guardar whatsmeow_device", err)
		}

		// Pre-keys: no hay método público para insertar una con id dado, así
		// que se usa el mismo INSERT que sqlstore (store.go, insertPreKeyQuery)
		// por lotes.
		for start := 0; start < len(conv.PreKeys); start += preKeyBatch {
			end := min(start+preKeyBatch, len(conv.PreKeys))
			chunk := conv.PreKeys[start:end]
			args := make([]any, 0, 1+len(chunk)*3)
			args = append(args, jid.String())
			ph := make([]string, len(chunk))
			for i, pk := range chunk {
				args = append(args, int64(pk.ID), pk.Priv[:], pk.Uploaded)
				ph[i] = fmt.Sprintf("($1, $%d, $%d, $%d)", 2+i*3, 3+i*3, 4+i*3)
			}
			q := `INSERT INTO whatsmeow_pre_keys (jid, key_id, key, uploaded) VALUES ` + strings.Join(ph, ",")
			if _, err := a.db.Exec(ctx, q, args...); err != nil {
				return sanitizeDBError("insertar whatsmeow_pre_keys", err)
			}
		}

		for _, k := range conv.AppStateKeys {
			if err := dev.AppStateKeys.PutAppStateSyncKey(ctx, k.ID, k.Key); err != nil {
				return sanitizeDBError("insertar whatsmeow_app_state_sync_keys", err)
			}
		}

		for _, id := range conv.Identities {
			if err := dev.Identities.PutIdentity(ctx, id.Address, id.Key); err != nil {
				return sanitizeDBError("insertar whatsmeow_identity_keys", err)
			}
		}
		// La de la cuenta principal va la última: es la autoritativa.
		if err := dev.Identities.PutIdentity(ctx, conv.MainIdentity.Address, conv.MainIdentity.Key); err != nil {
			return sanitizeDBError("insertar identidad de la cuenta principal", err)
		}

		if len(conv.LIDMappings) > 0 {
			if err := a.container.LIDMap.PutManyLIDMappings(ctx, conv.LIDMappings); err != nil {
				return sanitizeDBError("insertar whatsmeow_lid_map", err)
			}
		}

		for start := 0; start < len(conv.PrivacyTokens); start += 200 {
			end := min(start+200, len(conv.PrivacyTokens))
			if err := dev.PrivacyTokens.PutPrivacyTokens(ctx, conv.PrivacyTokens[start:end]...); err != nil {
				return sanitizeDBError("insertar whatsmeow_privacy_tokens", err)
			}
		}
		return nil
	})
}
