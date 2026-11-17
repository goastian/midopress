# 16. API de imágenes públicas sin CA — E2

Implementación y validación local: **17 de noviembre de 2026**. Responsabilidad:
backend y cliente web. La ruta pública es `GET /img`, en un listener independiente
del proxy y de `/api/v1`. No requiere interceptación TLS, instalación de una CA ni
Cloudflare. El panel incorpora **Images** como cliente de prueba.

## Configuración

```yaml
image_api:
  enabled: true
  addr: 127.0.0.1:9997
  public_url: http://127.0.0.1:9997
  # key_file: /ruta/privada/image-api.key
  max_bytes: 8MiB
  max_pixels: 2000000
  max_width: 4096
  max_dpr: 3
  concurrency: 2
  timeout: 30s
  signature_ttl: 5m
  cache_ttl: 1h
```

HTTP se admite únicamente en loopback para pruebas locales o detrás de un
terminador TLS. Para exposición directa, configurar `public_url` con HTTPS y
`image_api.tls.cert` / `image_api.tls.key` con un certificado de servidor válido.
El listener fuera de loopback exige ese par. No son certificados de una CA MITM.
Con terminación externa, mantener el listener HTTP ligado a loopback y publicar
solo `/img` y `/img/sign`. Nunca publicar la administración junto a ellos.

La clave HMAC es distinta del token administrativo. Si `key_file` está vacío,
se genera en el directorio de estado como `image-api.key`, con 32 bytes aleatorios
codificados en hexadecimal y permisos 0600. No se devuelve por ninguna API.
Cambiar listener, clave o límites requiere reiniciar. Desactivar `enabled`
y reiniciar revierte la función; los clientes deben cargar su original.

## Obtener solicitudes firmadas

`GET /api/v1/images` devuelve estado y límites. `POST /api/v1/images/sign` necesita
el token administrativo y está pensado para el operador o un servidor de confianza.
No se entrega ese token a una extensión ni a los visitantes de un sitio.

```sh
curl -fsS http://127.0.0.1:9998/api/v1/images/sign \
  -H "Authorization: Bearer $MIDOPRESS_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"url":"https://www.gstatic.com/webp/gallery/1.jpg","profile":"balanced","format":"auto","width":600,"dpr":1,"version":"1"}'
```

La respuesta contiene `optimized_url`, `original_url`, `fallback_url` y `expires`.
Las dos primeras son URLs firmadas de `/img`. La alternativa es la URL original
que aportó el cliente. No redirigir automáticamente el navegador a una URL tomada
de un mensaje de error: el cliente conserva su propia referencia original.

| Campo de la solicitud JSON | Contrato |
|---|---|
| `url` | Imagen pública HTTP/HTTPS, hasta 8192 caracteres; sin usuario, fragmento ni credenciales de sesión en query |
| `profile` | Perfil existente; `balanced` por defecto; se aplican sus valores completos |
| `format` | `auto`, `jpeg`, `png`, `webp`, `avif` o `jxl`; preferencia de salida, con original como alternativa si no hay mejora segura |
| `width` | Ancho CSS solicitado, 0 conserva las decisiones del perfil; sin ampliación; `width × dpr` no supera `max_width` |
| `dpr` | Entre 1 y el máximo configurado; por defecto 1 |
| `version` | Revisión del recurso aportada por el cliente; por defecto `1`, máximo 128 caracteres |

La representación servida informa su MIME real. El original puede conservarse
aunque se haya pedido otro formato o tamaño. `lossless` mantiene su contrato y no
reduce dimensiones. Una anchura ausente no demuestra el tamaño real del elemento
en la página. La extensión descrita abajo mide el layout del navegador.

La query firmada contiene exactamente una aparición de cada campo:
`url`, `v`, `profile`, `format`, `w`, `dpr`, `mode`, `exp`, `sig`.
`mode` es `optimized` u `original`. `exp` es Unix UTC en segundos. La firma es:

```text
hex(HMAC-SHA256(key, "GET\n/img\n" + canonical_query_without_sig))
```

La query canónica usa `url.Values.Encode()` de Go: claves ordenadas y escape de
formulario (espacio como `+`). Repeticiones, parámetros desconocidos, firma
alterada y caducidad se rechazan antes de consultar la caché. La firma de un modo
no autoriza el otro. No ampliar la caducidad desde un cliente.

## Credencial limitada y extensión

`POST /api/v1/images/credentials`, autenticado como administrador, emite una
credencial temporal con este alcance:

```json
{
  "hosts": ["www.gstatic.com", "*.cdn.example.com"],
  "profiles": ["balanced"],
  "max_width": 1024,
  "max_dpr": 2,
  "ttl_seconds": 1800
}
```

La respuesta 201 contiene `token`, `scope` y `public_url`. `scope` añade un
identificador aleatorio y `expires` Unix. Se admiten 1–32 hosts, 1–16 perfiles,
caducidad de 60–3600 segundos y límites dentro de la configuración de la API.
Un wildcard solo permite subdominios de ese host; no autoriza el dominio raíz
ni sufijos públicos como `*.com`. Se rechazan hosts locales y direcciones privadas.

El cliente envía **esa credencial limitada** como `Authorization: Bearer …` a
`POST /img/sign`, con el JSON de transformación de la tabla anterior. Necesita
`width >= 1` y un host, perfil, ancho y DPR autorizados. La respuesta contiene
`optimized_url`, `fallback_url` y `expires`; la firma no supera la caducidad de la
credencial. El GET firmado se solicita sin Authorization ni cookies. El token
administrativo no es una credencial válida de `/img/sign`.

La credencial usa HMAC-SHA256 con separación de dominio respecto a las firmas
GET. El secreto global permanece en el servidor. Hay un límite de 120 firmas
por credencial y minuto, 1024 ventanas activas y cuerpos JSON acotados. Errores:
403 `invalid_credential`/`outside_scope`, 409 `session_resource`, 429
`credential_rate_limit`. La comprobación SSRF completa sigue aplicándose al GET.

El panel **Images** emite y copia estas credenciales. La extensión MV3 está en
[`extension/`](../extension/README.md). Mide `currentSrc`, CSS computado,
`object-fit`, fondos, DPR, viewport y cambios de JavaScript mediante observadores
de layout y DOM; el navegador vuelve a seleccionar `picture`/`srcset` al cambiar
el viewport. No transmite credenciales de navegación y se activa por pestaña.
La prueba usa Chrome real con un perfil temporal. Su alcance y los casos que
conservan el original están documentados en el README; no se afirma cobertura de
iframes, shadow DOM, pseudoelementos, canvas, `image-set` ni transformaciones 3D.
Puede requerir la primera descarga original para conocer dimensiones naturales.

## Descarga, privacidad y límites

Solo se conectan puertos 80 para HTTP y 443 para HTTPS. Se rechazan direcciones
privadas, loopback, enlace local, multicast, CGNAT, rangos reservados y rutas de
metadatos conocidas, incluyendo direcciones IPv4 mapeadas y rangos de transición
IPv6. La resolución DNS termina en un dial a la IP concreta, con comprobación en
la conexión. Cada salto de hasta cinco redirecciones se valida de nuevo. El
transporte ignora proxies de entorno y verifica el certificado TLS del origen.

Las peticiones al origen son nuevas: no incluyen Cookie, Authorization,
Proxy-Authorization ni Referer de navegación. No existe cookie jar. El endpoint
rechaza credenciales entrantes; los clientes usan `credentials: 'omit'`.
Respuestas con Set-Cookie, private, no-store, no-cache, no-transform, Vary por
cookie/Authorization o `*`, o estados 401/403, solicitan volver al original.
También se revisan las respuestas intermedias de redirección. No se procesa SVG.

Bytes declarados y transferidos están limitados; se cuentan píxeles antes de
decodificar. En GIF también se limita la suma de los lienzos de todos los
fotogramas. Los códecs se ejecutan en un proceso hijo sin las credenciales de
entorno del servicio, con entrada y salida acotadas. El deadline incluye origen
y conversión; al vencer se mata y espera el proceso. Los slots limitan descarga
y trabajo juntos.

En Linux el worker aplica `no_new_privs`, seccomp con sincronización de todos
los hilos y límites del kernel antes de interpretar los recursos. No puede abrir
archivos, crear sockets, ejecutar procesos ni usar ptrace/io_uring. Los hilos
del runtime conservan la misma política. La memoria tiene límites de espacio
de direcciones/datos con 1,5 GiB de margen sobre el runtime, CPU acotada al
deadline (máximo 120 s), 32 descriptores y tamaño de archivo/core cero. La API,
el proxy (incluido segundo plano), fuentes, compresión y comparación de flujos
usan el worker. Texto sin límites para procesamiento se conserva original.

El padre exige una respuesta con aislamiento aplicado; si la plataforma no
dispone de la política, la transformación falla y conserva el original. El
benchmark permite `-unsafe-in-process` explícito y lo informa; no es el modo del
servicio. Service muestra política verificada, fecha, límites y slots reales.
Un proceso separado por sí solo no se presenta como una sandbox.

## Caché y diagnóstico

Se almacena únicamente el resultado de la transformación, en memoria con un
máximo de 64 MiB. Cada petición descarga de nuevo el recurso y comprueba su
política de privacidad, incluso cuando ya existe una conversión. Así no se sirve
un resultado antiguo de un origen que ahora exige sesión. Se ahorra cómputo; esta
versión no anuncia ahorro de la descarga del origen.

La clave SHA-256 incluye contenido, URL y versión del recurso, versión del
algoritmo, perfil completo, formato, ancho, DPR, modo, capacidades Accept y backend.
Un cambio de contenido o de valores de un perfil produce otra clave. La respuesta
HTTP pública lleva `Cache-Control: no-store` incluso en aciertos: una CDN no debe
saltarse la autenticación ni conservar URLs firmadas. El borrado de caché del panel
también purga estos resultados. La integración CDN pertenece a E3.

| Estado HTTP | `code` de error | Acción del cliente |
|---|---|---|
| 403 | `invalid_signature`, `destination_refused` | Conservar original; volver a firmar solo si procede |
| 409 | `session_resource` | Cargar el original con la sesión propia del navegador |
| 413 | `byte_limit` | Original |
| 422 | `unsupported_or_pixel_limit`, `invalid_result` | Original |
| 429 | `busy` | Original; reintento opcional después de `Retry-After` |
| 502 | `origin_failed` | Original |
| 504 | `timeout` | Original |

Los errores incluyen `fallback: "original"`, sin URL, IP, credenciales ni errores
internos. Los resultados incluyen `X-MidoPress-Original-Bytes`, `Result-Bytes`,
`Cache`, `Decision`, `Metric`, `Measure`, `Backend` y `Server-Timing`, con prefijo
`X-MidoPress-` salvo la última cabecera. CORS permite lectura sin credenciales.

## Calidad, color, efectos y fuentes

El motor aplica obligatoriamente la misma política al proxy y a `/img`:

- `quality_policy: metric` verifica a resolución completa contra el original
  decodificado. Un resultado redimensionado se reconstruye al tamaño original
  para la comparación; no se compara únicamente con una referencia reducida.
  La calidad fija no elude el umbral. Se comprueban todos los fotogramas, sus
  tiempos y el número de repeticiones. El campo legado `animated_to_still` no
  permite descartar fotogramas. Un presupuesto agotado conserva el original.
  La geometría reducida se comprueba antes de gastar encodes; si no cumple, se
  intenta comprimir a resolución original con el mismo umbral.
- `quality_policy: pixels` exige píxeles decodificados exactos, dimensiones y
  tiempos de animación exactos o devuelve el original. Desactiva reducción de
  tamaño, cuantización y efectos voluntarios. Es el valor de `lossless`.

Una métrica no demuestra percepción idéntica para todos los observadores. La
política `pixels` proporciona un contrato comprobable más estricto; no se
rebajan los umbrales para obtener una cifra de ahorro mayor.

Los perfiles personalizados aceptan `grayscale: true` y `placeholder_width: 8..128`
(`0` desactiva). También se editan desde Policy. Son cambios de apariencia
voluntarios: quedan excluidos de perfiles `lossless`, preservan transparencia y
no publican una métrica de equivalencia con el original. El cliente puede alternar
al original. Imágenes animadas o con color no interpretado conservan su original.

```yaml
profiles:
  gray:
    base: balanced
    grayscale: true
  lofi:
    base: saver
    placeholder_width: 32
```

`color_policy: preserve` conserva el recurso original cuando una recodificación
requiere cambiar su espacio de color o curva. `color_policy: srgb` autoriza una
conversión real mediante LittleCMS WASM incluido en el binario: perfiles ICC RGB,
GRAY y CMYK, matrices/TRC y LUT, con salida sRGB y alfa preservado. Se interpretan
ICC, orientación y transformaciones de presentación admitidas, PNG gAMA/cHRM/
cICP y AVIF NCLX SDR. El corpus compara la salida de 16 bits con LittleCMS nativo.
Colores fuera de sRGB pueden recortarse; esa conversión se informa como
`appearance_change` y no como equivalencia al original de color amplio. HDR
PQ/HLG, metadatos ambiguos y recortes fraccionarios no interpretables conservan
el original; esta versión no inventa una política de tone mapping.

La ruta JXL portable incluye libjxl WASM, con inspección de metadatos, alfa,
perfiles, orientación y animaciones. La recomposición JPEG→JXL dispone de ruta
WASM sin cgo ni bibliotecas dinámicas y comprueba reconstrucción byte por byte.
Las rutas admitidas se verifican con fixtures; límites de bytes/píxeles/frames y
la política de color siguen aplicándose. Los recursos que no se pueden verificar
conservan el original.

Service informa encoder/decoder reales por formato, el motivo de selección y
disponibilidad y ruta de recomposición reversible; cada salida `/img` informa la
ruta que la produjo. La denominación `wasm` puede seleccionar AVIF/WebP nativos;
la información distingue bibliotecas nativas, WASM y Go generado desde WASM
(`go-wasm2go`). El inventario exhaustivo de versiones
nativas para una distribución sigue siendo una tarea separada de publicación.

Los [límites internos de JXL](../internal/jxlcodec/README.md) siguen aplicándose
aunque se aumenten las cuotas HTTP: 64 MiB de entrada, 16 millones de píxeles
acumulados entre fotogramas, 256 fotogramas, ICC de 4 MiB y 20 segundos por
invocación del módulo.

Las fuentes siguen en el pipeline WOFF2 existente, con verificación de tablas y
glifos; `/img` no modifica ni sirve fuentes. Sus pruebas forman parte de la suite
de regresión. Subconjuntos tipográficos y nuevos controles de fuentes no se dan
por implementados con esta entrega.

## Evidencia y alcance de publicación

Pruebas de backend: `internal/imageapi/service_test.go`, `internal/config/imageapi_test.go`,
`internal/server/imageapi_test.go` y `internal/img/effects_test.go`. La integración
comprueba HTTPS en listener separado sin MITM y firma administrativa autenticada.
La prueba de cancelación ejecuta un proceso lento y verifica que termina al vencer
el deadline. El cliente web se comprueba en escritorio y móvil con Playwright;
sus respuestas preparadas prueban UI y omisión de credenciales, sin depender de
sitios externos. La descarga real se comprueba además en el despliegue local.

La evidencia final y resultados de ejecución se registran junto a E2 en el
[documento 13](13-plan-progresivo.md). El despliegue de localhost no certifica
producción, otras plataformas, un piloto CDN ni todos los pendientes de E1.
