package main

// Decodificación del formato BufferJSON de Baileys y de las capas de
// serialización que Evolution API v2.3.x añade encima.
//
// Referencias (Baileys 7.0.0-rc.9, la versión que fija Evolution 2.3.7):
//   - src/Utils/generics.ts, BufferJSON.replacer: todo Buffer/Uint8Array se
//     escribe como {"type":"Buffer","data":"<base64>"}.
//   - BufferJSON.reviver acepta además objetos {"0":n,"1":n,...} (un
//     Uint8Array serializado sin replacer) y convierte a Buffer.
//   - Los mensajes protobufjs (creds.account, app-state-sync-key) tienen
//     toJSON(), que JSON.stringify llama ANTES del replacer, con
//     util.toJSONOptions {bytes: String, longs: String}: sus campos bytes
//     salen como cadena base64 y los int64 como cadena decimal.
//
// Aquí se aceptan las cuatro formas. NUNCA se incluye el contenido en los
// errores: sólo se nombra el campo.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// bjBytes es un []byte que se decodifica desde cualquiera de las formas en
// que Baileys/protobufjs dejan un binario en JSON.
type bjBytes []byte

var errBadBinary = errors.New("valor binario con formato no reconocido")

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errBadBinary
}

func bytesFromNumberArray(arr []json.Number) ([]byte, error) {
	out := make([]byte, len(arr))
	for i, n := range arr {
		v, err := strconv.ParseUint(n.String(), 10, 8)
		if err != nil {
			return nil, errBadBinary
		}
		out[i] = byte(v)
	}
	return out, nil
}

func (b *bjBytes) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*b = nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	switch data[0] {
	case '"':
		// protobufjs toJSON (bytes: String) o advSecretKey: base64 plano.
		var s string
		if err := dec.Decode(&s); err != nil {
			return errBadBinary
		}
		out, err := decodeB64(s)
		if err != nil {
			return err
		}
		*b = out
		return nil
	case '[':
		var arr []json.Number
		if err := dec.Decode(&arr); err != nil {
			return errBadBinary
		}
		out, err := bytesFromNumberArray(arr)
		if err != nil {
			return err
		}
		*b = out
		return nil
	case '{':
		var obj map[string]json.RawMessage
		if err := dec.Decode(&obj); err != nil {
			return errBadBinary
		}
		if t, ok := obj["type"]; ok {
			var typ string
			if json.Unmarshal(t, &typ) != nil || typ != "Buffer" {
				return errBadBinary
			}
			raw := bytes.TrimSpace(obj["data"])
			if len(raw) == 0 {
				return errBadBinary
			}
			var inner bjBytes
			if err := inner.UnmarshalJSON(raw); err != nil {
				return err
			}
			*b = inner
			return nil
		}
		// {"0":n,"1":n,...}: Uint8Array serializado sin replacer.
		idx := make([]int, 0, len(obj))
		for k := range obj {
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 {
				return errBadBinary
			}
			idx = append(idx, i)
		}
		sort.Ints(idx)
		out := make([]byte, len(idx))
		for pos, i := range idx {
			if i != pos {
				return errBadBinary
			}
			var n json.Number
			d := json.NewDecoder(bytes.NewReader(obj[strconv.Itoa(i)]))
			d.UseNumber()
			if d.Decode(&n) != nil {
				return errBadBinary
			}
			v, err := strconv.ParseUint(n.String(), 10, 8)
			if err != nil {
				return errBadBinary
			}
			out[pos] = byte(v)
		}
		*b = out
		return nil
	}
	return errBadBinary
}

// flexUint acepta número JSON o cadena decimal (protobufjs longs: String).
type flexUint uint64

func (f *flexUint) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*f = 0
		return nil
	}
	s := string(data)
	if data[0] == '"' {
		if err := json.Unmarshal(data, &s); err != nil {
			return errors.New("entero con formato no reconocido")
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*f = 0
		return nil
	}
	// Admite "123" y también "123.0" que algunos JSON producen.
	if v, err := strconv.ParseUint(s, 10, 64); err == nil {
		*f = flexUint(v)
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 && v == float64(uint64(v)) {
		*f = flexUint(uint64(v))
		return nil
	}
	return errors.New("entero con formato no reconocido")
}

// unwrapJSONString: si el documento es una cadena JSON cuyo contenido es a
// su vez JSON (objeto o arreglo), devuelve ese contenido. Evolution guarda la
// columna Session.creds como JSON.stringify(JSON.stringify(creds, replacer))
// (use-multi-file-auth-state-prisma.ts: writeData → saveKey), así que el
// volcado de la columna es una cadena JSON con el objeto dentro.
func unwrapJSONString(data []byte) ([]byte, error) {
	data = bytes.TrimSpace(data)
	for i := 0; i < 3 && len(data) > 0 && data[0] == '"'; i++ {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, errors.New("el documento no es JSON válido")
		}
		data = bytes.TrimSpace([]byte(s))
	}
	if len(data) == 0 || (data[0] != '{' && data[0] != '[') {
		return nil, errors.New("el documento no contiene un objeto JSON")
	}
	return data, nil
}

// decodeRedisValue convierte el valor crudo de un campo del hash de Redis en
// el JSON del objeto que Baileys guardó.
//
// Evolution 2.3.7 codifica DOS veces cada valor del hash:
//   - CacheService.hSet (src/api/services/cache.service.ts):
//     json = JSON.stringify(value, BufferJSON.replacer)
//   - RedisCache.hSet (src/cache/rediscache.ts):
//     client.hSet(key, field, JSON.stringify(json, BufferJSON.replacer))
//
// y lo lee con dos JSON.parse (RedisCache.hGet y CacheService.hGet). Aquí se
// replica exactamente eso: un primer parse; si da cadena, un segundo parse.
// Si el primero ya da objeto/arreglo/número (volcado de otra herramienta), se
// usa tal cual.
func decodeRedisValue(raw string) (json.RawMessage, error) {
	first := bytes.TrimSpace([]byte(raw))
	if len(first) == 0 {
		return nil, errors.New("valor vacío")
	}
	var s string
	if first[0] == '"' {
		if err := json.Unmarshal(first, &s); err != nil {
			return nil, errors.New("valor no es JSON válido")
		}
		second := bytes.TrimSpace([]byte(s))
		if len(second) == 0 {
			return nil, errors.New("valor vacío")
		}
		if !json.Valid(second) {
			// Cadena terminal que no es JSON (p. ej. volcado con una sola
			// capa de un valor string): se devuelve como cadena JSON.
			out, _ := json.Marshal(s)
			return out, nil
		}
		return json.RawMessage(second), nil
	}
	if !json.Valid(first) {
		return nil, errors.New("valor no es JSON válido")
	}
	return json.RawMessage(first), nil
}

// parseKeysDump lee el volcado del hash de llaves. Formatos admitidos:
//   - objeto JSON campo → valor, donde el valor es la cadena cruda de Redis
//     (lo normal) o ya el objeto decodificado;
//   - salida de `redis-cli --raw HGETALL <hash>`: líneas alternas campo/valor.
//     Los valores son JSON.stringify, que nunca emite saltos de línea crudos.
func parseKeysDump(data []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	out := make(map[string]json.RawMessage)
	if len(trimmed) == 0 {
		return out, nil
	}
	if trimmed[0] == '{' {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, errors.New("--keys: el archivo empieza por '{' pero no es un objeto JSON válido")
		}
		for field, v := range obj {
			v = bytes.TrimSpace(v)
			var val json.RawMessage
			var err error
			if len(v) > 0 && v[0] == '"' {
				var rawStr string
				if json.Unmarshal(v, &rawStr) != nil {
					return nil, fmt.Errorf("--keys: campo %q con valor ilegible", safeField(field))
				}
				val, err = decodeRedisValue(rawStr)
			} else {
				val, err = decodeRedisValue(string(v))
			}
			if err != nil {
				return nil, fmt.Errorf("--keys: campo %q: %v", safeField(field), err)
			}
			out[field] = val
		}
		return out, nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	// Quita la línea vacía final que deja la redirección.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines)%2 != 0 {
		return nil, fmt.Errorf("--keys: formato HGETALL con número impar de líneas (%d); ¿se cortó el volcado?", len(lines))
	}
	for i := 0; i < len(lines); i += 2 {
		field := strings.TrimSpace(lines[i])
		val, err := decodeRedisValue(lines[i+1])
		if err != nil {
			return nil, fmt.Errorf("--keys: campo %q (línea %d): %v", safeField(field), i+1, err)
		}
		if _, dup := out[field]; dup {
			return nil, fmt.Errorf("--keys: campo repetido %q", safeField(field))
		}
		out[field] = val
	}
	return out, nil
}

// safeField recorta el nombre de campo para mensajes. Los nombres de campo
// (pre-key-<n>, session-<jid>...) no son secretos, pero se limitan por si el
// archivo viniera desalineado y lo que se toma por nombre fuera un valor.
func safeField(field string) string {
	for _, p := range knownPrefixes {
		if strings.HasPrefix(field, p) {
			if len(field) > 80 {
				return field[:80] + "…"
			}
			return field
		}
	}
	return "<campo no reconocido>"
}
