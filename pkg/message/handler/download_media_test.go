package message_handler

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vincent-petithory/dataurl"
)

// La respuesta en streaming tiene que ser byte a byte la que producía
// ctx.JSON con gin.H (claves ordenadas, escapado HTML de encoding/json).
func TestWriteDownloadMediaJSONMatchesLegacyBody(t *testing.T) {
	cases := []struct {
		name string
		mime string
		data []byte
		ts   string
		// mimeInvalido: el caso solo comprueba el escapado JSON; un data-URI
		// con ese subtipo no es decodificable ni antes ni ahora.
		mimeInvalido bool
	}{
		{"jpeg", "image/jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, "0001-01-01 00:00:00 +0000 UTC", false},
		{"vacio", "application/pdf", nil, "", false},
		{"ogg opus", "audio/ogg", bytes.Repeat([]byte("OggS\x00\x02"), 700), "2026-09-25 12:00:00 -0500 -05", false},
		{"docx largo", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", bytes.Repeat([]byte{0x50, 0x4b, 0x03, 0x04, 0xfe}, 4099), "x", false},
		{"mime con chars html", "text/x-<script>&", []byte("hola"), "<&>", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			du := dataurl.New(tc.data, tc.mime)

			legacy, err := json.Marshal(gin.H{"message": "success", "data": gin.H{"base64": du.String(), "timestamp": tc.ts}})
			if err != nil {
				t.Fatalf("legacy marshal: %v", err)
			}

			var got bytes.Buffer
			if err := writeDownloadMediaJSON(&got, du, tc.ts); err != nil {
				t.Fatalf("writeDownloadMediaJSON: %v", err)
			}

			if !bytes.Equal(got.Bytes(), legacy) {
				t.Fatalf("cuerpo distinto\n got: %.200s\nwant: %.200s", got.String(), string(legacy))
			}

			// Y que lo que Laravel lee (data.base64) sea un data-URI válido.
			var parsed struct {
				Data struct {
					Base64 string `json:"base64"`
				} `json:"data"`
			}
			if err := json.Unmarshal(got.Bytes(), &parsed); err != nil {
				t.Fatalf("no es JSON válido: %v", err)
			}
			if tc.mimeInvalido {
				return
			}
			back, err := dataurl.DecodeString(parsed.Data.Base64)
			if err != nil {
				t.Fatalf("data-URI inválido: %v", err)
			}
			if !bytes.Equal(back.Data, tc.data) {
				t.Fatalf("los bytes no sobreviven la vuelta: %d vs %d", len(back.Data), len(tc.data))
			}
		})
	}
}
