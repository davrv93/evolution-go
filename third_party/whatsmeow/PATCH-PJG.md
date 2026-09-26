# Parche pjgfactsalud sobre whatsmeow

Copia de go.mau.fi/whatsmeow@v0.0.0-20260630180629-b572e5bcb92b con UN cambio:

- `send.go`: variable `OwnDevicesMessage` que permite sustituir la copia que
  reciben los otros dispositivos del remitente (DeviceSentMessage). Evolution-go
  la usa para que el teléfono principal vea el texto de respaldo de un
  mensaje con botones/lista, que la app personal no pinta.

Al actualizar whatsmeow: copiar la versión nueva y reaplicar el bloque marcado
«PATCH pjgfactsalud» en `marshalMessage`.
