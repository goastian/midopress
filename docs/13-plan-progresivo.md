# 13. Plan progresivo de tareas y lanzamientos

Actualizado: **17 de noviembre de 2026**. Este es el plan operativo consolidado
de los documentos 05, 06, 08, 10 y 11. Los números de entrega expresan orden,
no fechas prometidas. Cada entrega debe poder probarse y publicarse por separado.

## 1. Estados y forma de trabajar

| Marca | Significado | Cuándo cambiarla |
|---|---|---|
| `[X]` / X | Completado en el alcance descrito | Código, revisión y evidencia disponibles |
| `[-]` / - | Parcial o validación pendiente | Hay una base, pero falta parte del resultado |
| `[ ]` / [] | Sin implementar | Solo hay propuesta o no se ha iniciado |

La marca de una tarea no cierra automáticamente la entrega. Publicar, desplegar
y medir son tareas distintas de implementar. Al trabajar una tarea, añadir su
responsable, enlace a cambio y evidencia junto a su identificador.

Secuencia recomendada:

```text
E0 Seguridad -> E1 Base medible -> E2 /img -> E3 Cloudflare opcional
                         |           |
                         |           +----> E6 Midori Desktop y Android
                         +----> E4 Lectura IA -> E5 MCP y contexto -> E8 Asistente
                         +----> E7 Menos descargas y vídeo
E3/E5/E6/E7/E8 validados -> E9 Consolidación 1.0
```

E4 no necesita esperar la integración completa de Midori. E3 no bloquea el
autoalojamiento ni la API de IA. E7 puede limitarse a una función pequeña por
lanzamiento; no es necesario implementar todo para distribuir E2 o E4.

## 2. Base que ya está completada

- [X] B01. Proxy HTTP/HTTPS, CONNECT, TLS opcional, HTTP/2 y WebSocket.
- [X] B02. CA, certificados por SNI, caché y exclusiones de interceptación.
- [X] B03. Brotli/gzip/zstd, minificación granular y decodificación de entrada.
- [X] B04. Motor de imágenes, formatos modernos, métricas y perfiles.
- [X] B05. Fuentes a WOFF2 y comprobación del resultado.
- [X] B06. Caché de conversiones, memoria y disco; tareas de segundo plano.
- [X] B07. Configuración YAML, variables, CLI, reglas y recarga en caliente.
- [X] B08. Autenticación, dispositivos, controles de red y timeouts.
- [X] B09. Panel, API, SSE, métricas, flujos voluntarios y HAR.
- [X] B10. PAC, descubrimiento y página/descarga de certificados.
- [X] B11. Benchmarks, corpus, pruebas y workflows de compilación.
- [-] B12. Cierre de las metas de las fases A/B del doc. 11: hay implementación,
  pero la meta de ahorro general de `saver` no está demostrada.

Los límites de estas marcas están detallados en [el inventario](12-analisis-actual.md).

## 3. E0 — Corrección de seguridad y dependencias

**Objetivo:** una actualización de mantenimiento que pueda reconstruirse.
**Depende de:** nada. **Responsabilidad sugerida:** backend y mantenimiento.

- [X] E0.1. Identificar Brotli afectado, corrección mínima y rutas que decodifican datos.
- [X] E0.2. Actualizar Brotli y las demás dependencias de Go con versiones nuevas.
- [X] E0.3. Actualizar dependencias compatibles del panel y su archivo de bloqueo.
- [X] E0.4. Fijar Go 1.27.4 y Node 22.23.5 para corregir avisos de los runtimes;
  alinear Docker, CI y documentación de desarrollo.
- [X] E0.5. Añadir revisión semanal de Go, npm, Docker y Actions; añadir auditoría
  npm a CI. La configuración está escrita; su ejecución remota aún debe observarse.
- [-] E0.6. Cerrar validaciones locales y registrar resultados, límites y pruebas
  pendientes en [el documento 15](15-seguridad-dependencias.md).
- [ ] E0.7. Reconstruir binarios e imágenes con estos requisitos; inventariar
  libbrotli y códecs nativos/WASM realmente incluidos. Un `go.mod` corregido no
  actualiza un binario o contenedor ya desplegado.
- [ ] E0.8. Publicar la versión de mantenimiento, reiniciar un entorno de prueba
  y comprobar navegación, panel, certificados y reversión.

**Para liberar:** checks aplicables aprobados, reconstrucción reproducible y
ausencia de avisos conocidos sin resolver en los componentes distribuidos. Las
rutas nativas opcionales requieren revisión propia; no basta `govulncheck`.
**Probar:** texto Brotli válido, inválido y truncado, límites, fuentes, imágenes,
administración y reenvío local. **Revertir:** desactivar la función afectada o
volver a un artefacto seguro; no reintroducir Brotli vulnerable.

## 4. E1 — Base confiable, medible y apta para pilotos

**Objetivo:** distinguir ahorro, velocidad y privacidad antes de ampliar tráfico.
**Depende de:** E0. **Responsabilidad sugerida:** backend, QA y operación.

- [-] E1.1. Repetir el corpus fijo con caché fría y caliente; hay herramientas,
  falta una línea base con las dependencias nuevas y hardware documentado.
- [ ] E1.2. Añadir TLS/HTTP/2, WebSocket, SSE, SRI dinámico y sitios con rangos a
  la matriz explícita de compatibilidad. Registrar el fallback observado.
- [ ] E1.3. Resolver la política de `no-transform`: respetarlo en el proxy y
  documentar cualquier transformación explícita en un modo separado.
- [ ] E1.4. Excluir almacenamiento de respuestas `no-store`; definir tratamiento
  de `private`, cookies y Authorization antes de compartir cachés.
- [ ] E1.5. Verificar invalidación de caché al cambiar perfiles, algoritmo,
  opciones o backend; incluir versión de transformación cuando corresponda.
- [ ] E1.6. Añadir cuotas persistentes de bytes/peticiones/concurrencia por
  dispositivo y límites de alta; mantener separados tokens de usuario y administrador.
- [ ] E1.7. Documentar TLS del origen, CT, revocación, HSTS y límites del MITM;
  priorizar una política conservadora y errores entendibles.
- [ ] E1.8. Añadir `SECURITY.md`, proceso de actualizaciones, inventario de
  dependencias/SBOM y guía de operación y recuperación de CA.
- [-] E1.9. Mostrar al operador el backend real, versiones nativas y motivo de
  fallback; no depender solo de la etiqueta `wasm`. Ya se muestran encoder y
  decoder reales, Go generado, motivo y ruta JPEG→JXL en Service. Las rutas del
  servicio usan aislamiento de permisos verificado. Falta inventariar versiones
  de bibliotecas nativas para cada artefacto distribuido. Ver docs. 16 y 17.

### Comprobaciones locales de navegación, panel y perfiles

Trabajo del **16 de noviembre de 2026**, backend, cliente web y análisis paralelo:

- [X] Distinguir tráfico HTTPS en túneles de respuestas HTTP inspeccionadas en
  Overview; mostrar bypass automáticos tras rechazo del certificado. El tráfico
  sin inspección ya no se presenta como ausencia de tráfico.
- [X] Excluir respuestas incompletas del ahorro, conservando sus bytes reales.
  Aplicado a totales, tipos, perfiles, series, dominios, dispositivos y flujos;
  cubierto por regresiones de backend y navegador.
- [X] Hacer que Brotli estático respete el límite de CPU y continúe en segundo
  plano cuando no hay un slot libre, sin rebajar la calidad de los perfiles.
- [X] Contener una caída observada con el codificador dinámico de píxeles JXL:
  esa ruta se deshabilita y se registra como `encoder=disabled`. AVIF/WebP,
  decodificación JXL y recomposición JPEG→JXL reversible siguen independientes.
- [X] Añadir `web/scenarios/popular.mjs`: navegación pública por 16 sitios,
  modo sobre el servicio de localhost y comparación aislada de perfiles con
  cachés fría/caliente, límites y diagnóstico de desafíos, sesiones, errores y
  reinicios. El navegador de prueba no modifica la confianza del sistema.
- [-] Demostrar una mejora general considerable de carga. El ahorro de cuerpos
  completos y los tiempos de navegador se informan por separado; una visita
  con CAPTCHA, imágenes rotas o carga incompleta no acredita aceleración.
- [X] Recuperar codificación de píxeles JXL mediante un módulo libjxl WASI propio,
  con alfa, orientación y animación probados. El cursor nativo de la dependencia
  que causaba la caída ya no se usa; no se presenta como corregido ese binding.

Evidencia local y límites en
[`artifacts/popular-final-analysis.md`](../artifacts/popular-final-analysis.md),
con JSON/CSV regenerables y capturas. Estas comprobaciones no cierran el corpus
completo de E1.1, la matriz de E1.2 ni una garantía perceptual universal.
La pasada final registra **16 sitios**, **36 visitas** de comparación entre cuatro
sitios y cuatro perfiles, cuatro visitas del ensayo de texto y tres del control
a 5 Mbps: **59 visitas de navegador**. Ocho solicitudes adicionales de imagen
comprueban los cuatro perfiles en el servicio del panel con su CA local.
La captura `artifacts/dashboard-done.png` contrasta **2.128 solicitudes HTTP**,
**48,1 MB recibidos** y **5,7 MB de ahorro acreditado** con la API. El servicio
no se reinició en esta pasada. Pasan las 24 pruebas E2E de escritorio/móvil,
21 pruebas del servidor de demostración y la suite Go completa del estado final
con bibliotecas nativas; hay regresiones específicas de cancelaciones y JXL.
En este equipo el proxy aún descarga la respuesta del origen antes de consultar
su caché de transformaciones: ahorrar bytes hacia localhost no significa evitar
esa descarga de Internet.

**Para liberar:** ningún cruce de datos entre dos dispositivos de prueba; no
almacenamiento de `no-store`; cuotas efectivas bajo concurrencia; corpus sin
nuevas roturas. **Medir:** bytes por tipo, LCP/TTFB, p50/p95, CPU, RAM y errores.
**Revertir:** desactivar la opción nueva por perfil y purgar entradas incompatibles.

## 5. E2 — Imágenes sin instalar una CA

**Objetivo:** primera función nueva utilizable por clientes y futuros Midori.
**Depende de:** E1. **Responsabilidad sugerida:** backend y cliente web.

- [X] E2.1. Implementar `/img` como API HTTPS separada de la administración,
  reutilizando el motor actual y conservando una instalación sin Cloudflare.
- [X] E2.2. Firmar solicitud con HMAC, caducidad, recurso y parámetros de
  transformación; firma en servidor o credencial de alcance limitado, sin poner
  un secreto global en una extensión.
- [X] E2.3. Impedir SSRF: solo HTTP/HTTPS permitidos, control de puertos, IPs
  privadas/locales y metadatos, resolución final y cada redirección revalidadas.
- [X] E2.4. No reenviar cookies ni Authorization de navegación; excluir imágenes
  de sesión y devolver al cliente la alternativa original cuando corresponda.
- [X] E2.5. Limitar bytes, píxeles, tiempo, concurrencia, formatos y ancho/DPR;
  devolver diagnóstico útil sin filtrar secretos ni URL privadas.
- [X] E2.6. Definir clave de variantes: recurso, versión, perfil, formato,
  tamaño y DPR. Autenticar antes de consultar cualquier caché compartida.
- [X] E2.7. Crear cliente de prueba con alternancia original/optimizada y
  estadísticas; documentar contrato y ejemplos.

**Para liberar:** mismas imágenes y calidad que el motor existente; solicitudes
manipuladas y destinos internos rechazados; sin contaminación entre variantes.
**Lanzar:** beta limitada de imágenes públicas. **Revertir:** el cliente carga el
original y el endpoint se desactiva con una opción.

### Evidencia de implementación y prueba local de E2

Responsabilidad: **backend y cliente web**, trabajo local del 16 de noviembre de
2026. Contrato, configuración, ejemplos y límites en [el documento 16](16-api-imagenes.md).
Las X anteriores cierran la implementación y prueba local descritas; E1, la
publicación de una beta externa y una validación por plataformas siguen abiertas.

| Tarea | Cambio revisable | Evidencia |
|---|---|---|
| E2.1 | `internal/server/imageapi.go`, `internal/config/imageapi.go` | Integración HTTPS en listener separado, sin MITM, en `internal/server/imageapi_test.go` |
| E2.2 | `internal/imageapi/request.go`, `key.go`, `credential.go`, `internal/admin/handlers_images.go` | Manipulación de parámetros, caducidad, clave persistente 0600, firma administrativa y credenciales limitadas por host/perfil/tamaño; 200 solicitudes concurrentes permiten 120 firmas y rechazan 80 |
| E2.3 | `internal/imageapi/fetch.go` | IPs privadas/reservadas/IPv4 mapeadas, DNS final y redirecciones peligrosas rechazadas en `service_test.go` |
| E2.4 | `internal/imageapi/fetch.go`, `service.go` | Sin reenvío de credenciales; exclusión de respuestas de sesión y cabeceras privadas; alternativa original del cliente |
| E2.5 | `internal/imageapi/service.go`, `worker.go` | Bytes declarados/transferidos, píxeles, slots, ancho/DPR y cancelación de un proceso lento; errores sin destinos ni secretos |
| E2.6 | `internal/imageapi/service.go` | Firma antes de caché; variantes por contenido, recurso/versión, perfil completo, formato, tamaño, DPR, Accept y backend; invalidación al cambiar perfil |
| E2.7 | `web/src/views/ImagesView.vue`, `web/e2e/images.spec.ts` | Comparador, estadísticas y alternativa original; 20 pruebas Playwright aprobadas entre escritorio y móvil |

Administración desplegada en `http://localhost:9998/#/images`; `/img` en
`http://127.0.0.1:9997`, exclusivamente loopback. Servicio de usuario `midopress`
reiniciado con Go 1.27.4 y panel compilado con Node 22.23.5. Se conservó el token
existente. El soporte HTTPS directo se probó con certificado de servidor en la
integración; el entorno local usa HTTP y no requiere instalar una CA.

Validación: `go test ./...`, `go vet ./...`, compilación del panel y pruebas
Playwright aprobadas. `go test -race` aprobado para API, configuración, motor,
servidor y pipeline. Regresión encontrada durante el cierre: el minificador nuevo
alteraba variables CSS; el modo safe ahora conserva el original cuando eso ocurre.
Resultados en `artifacts/e2-go-test.log`, `e2-go-vet.log`, `e2-race.log` y
`e2-playwright.log`.

Prueba real por el servicio: una PNG pública de 375229 bytes pasó a WebP de
48350 bytes (**87,11 %** en esa imagen), DSSIM de resolución completa **0,0019473**;
la segunda petición dio acierto de caché y una firma manipulada dio 403.
Una JPEG pública pasó de 44891 a 42392 bytes (**5,57 %**), DSSIM **0,00187225**.
El original se conserva cuando no hay mejora admitida. Son muestras
funcionales, no una nueva media del corpus ni una promesa de ahorro general.
La evidencia está en `artifacts/e2-live-smoke.json` y `e2-live-jpeg.json`.

### Cierre de imágenes y fuentes relacionado con esta entrega

| Estado | Punto | Resultado y alcance comprobado |
|---|---|---|
| X | Política de fidelidad comprobable — sustituye la promesa perceptual universal | Proxy y `/img` verifican a resolución completa; calidades fijas, todos los fotogramas, tiempos y repeticiones. `quality_policy: pixels` exige píxeles decodificados exactos o conserva el original. Prevalidación de resize y reintento acotado sin reducir el umbral. Una métrica no demuestra percepción idéntica para todos los observadores; esa afirmación universal no se da por probada. |
| X | Color amplio SDR | LittleCMS WASM incluido convierte ICC RGB/GRAY/CMYK, matrices/TRC y LUT; corpus de 16 bits contrastado con LittleCMS nativo, CMYK JPEG y AVIF real con ICC/recorte/orientación. `preserve` predeterminado; `srgb` explícito informa cambio de apariencia. HDR, transferencias desconocidas y metadatos ambiguos conservan el original, sin inventar tone mapping ni equivalencia de gamut. |
| X | Tamaño visible medido por el navegador | Extensión Chrome MV3 funcional: CSS/layout/JavaScript reales, `currentSrc`, `picture/srcset`, object-fit, fondos, DPR y observadores de cambios. Seis escenarios en Chrome real. Casos no interpretables conservan el original; alcance y primera descarga original documentados en `extension/README.md`. |
| X | Aislamiento de permisos de las rutas del servicio | Linux: seccomp TSYNC para hilos existentes/futuros, sin archivos/red/exec, con límites del kernel y deadline. Imágenes, dimensiones, fuentes, texto, minificación, pistas HTML, comparación y segundo plano pasan por el worker; cada respuesta exige aislamiento aplicado. Otras plataformas conservan original si no hay política. Service distingue Go, WASM, Go generado y nativo, y muestra verificación/límites/slots. |
| X | Escala de grises y placeholders Lo-Fi | Controles `grayscale` y `placeholder_width` en perfiles, YAML y Policy; pruebas de alfa, tamaño y rechazo en lossless. Son cambios voluntarios de apariencia, con opción original, sin promesa de fidelidad al color/detalle original. |
| X | JXL de entrada | Inspector y códec libjxl WASI propio: alfa, ICC, orientación, 16 bits, fotogramas/tiempos/repeticiones y contenedores fragmentados probados. Ya no depende solo del parser de cabeceras sRGB sin alfa. HDR, transferencia no clasificable, canales auxiliares fuera de RGBA y límites excedidos conservan el original. |
| X | JPEG a JXL reversible portable | Módulo incluido en el binario, sin cgo ni bibliotecas dinámicas; reconstrucción comprobada byte por byte. Pruebas con EXIF, progresivo, gris, submuestreo y restart; `CGO_ENABLED=0 -tags nodynamic` y sin `LD_LIBRARY_PATH`. Se informa la ruta real nativa opcional o WASM. |
| X | Regresión del motor de fuentes existente | Suite `internal/fonts` aprobada; conversión y verificación WOFF2 existentes conservadas. Esta X no incluye nuevos controles, subsetting ni bloqueo de fuentes. |

### Aceptación local del cierre — 17 de noviembre de 2026

Las seis implementaciones anteriores cierran su contrato técnico explícito. El
alcance, límites, costes, fuentes de construcción y pruebas están detallados en
[el documento 17](17-validacion-imagenes.md). No se sustituye una validación
humana universal por una marca X ni se promete acelerar cualquier sitio.

- [X] Suite Go completa: **31 paquetes con pruebas**, incluidos servidor, API,
  pipeline, fuentes, calidad/color, JXL y restricciones del kernel.
- [X] `go vet ./...`, compilación del binario y del panel.
- [X] **28 pruebas E2E** del panel en escritorio/móvil; credencial separada del
  token administrativo y edición persistente de políticas.
- [X] **Seis escenarios de extensión real** con respuesta lenta para comprobar
  cambios de layout durante peticiones; se corrigió una carrera que dejaba
  fondos originales tras invalidar el trabajo anterior.
- [X] Credencial con cuota concurrente comprobada con `-race`; portabilidad JXL
  sin bibliotecas y WebP/AVIF/fuentes/texto/pistas HTML bajo seccomp sin dinámicas.
- [X] Corregir bloqueo de streams: primer fragmento antes de EOF; conservarlos
  originales cuando no existe longitud acotada para procesamiento aislado.
- [X] Desplegar en localhost y probar la extensión contra la API real: 12.694
  bytes ahorrados en una JPEG, ancho preservado y aislamiento verificado.
- [X] Comprobar los cuatro perfiles en la API real: ahorro de 28,3 %, 53,5 % y
  76,9 % en balanced/saver/extreme; lossless conserva el original. Caché,
  métricas, firma alterada, cookie y destino privado comprobados.
- [X] Registrar tráfico continuo tras el despliegue: muestra de 61 imágenes,
  todas HTTP 200 y TLS verificado, sin detener generador ni observador.

El generador `midopress-traffic` y el observador `midopress-observe` permanecen
activos durante pruebas y despliegue. Overview, Traffic y Service muestran el
tráfico y la ruta real. Los contadores de memoria se reinician al sustituir el
binario; la observación registra ese evento. La prueba de carga no se pausa ni
se convierte en una afirmación de aceleración general. Evidencia en
`artifacts/closure-*`, `extension-*` y `continuous/`, descrita en el documento 17.

## 6. E3 — Piloto opcional con Cloudflare

**Objetivo:** medir aceleración real del endpoint HTTPS.
**Depende de:** E2; investigación terminada en el doc. 14.

- [X] E3.1. Investigar CDN, Workers, variantes, Cache Reserve, Images y AI Gateway.
- [ ] E3.2. Crear configuración reproducible para un dominio HTTPS de pruebas,
  origen con TLS y recursos públicos de prueba.
- [ ] E3.3. Validar firma y caducidad en el borde antes de atender aciertos;
  no almacenar tokens en claves, métricas ni logs.
- [ ] E3.4. Configurar variantes, exclusiones de caché, TTL y purga por versión.
  Probar explícitamente que un recurso privado nunca se comparte.
- [ ] E3.5. Comparar directo/CDN con caché fría/caliente desde las regiones de
  usuarios; separar tiempo de transformación, origen y red.
- [ ] E3.6. Evaluar Tiered Cache; considerar Cache Reserve solo con repetición
  suficiente y coste conocido. No asumir que `cache.put()` activa Tiered Cache.
- [ ] E3.7. Probar Images como alternativa opcional en una muestra; no anunciar
  la garantía DSSIM propia si ese proveedor no la verifica.
- [ ] E3.8. Documentar gasto máximo del piloto y funcionamiento sin proveedor.

**Para liberar:** mejora de p95/LCP respecto a E2 en la muestra, variantes y
caducidad correctas, gasto dentro del límite aprobado. Si no mejora, mantener E2
directo. **Revertir:** ruta directa preparada y purga de variantes del piloto.

## 7. E4 — Primera beta de IA: leer páginas con menos tokens

**Objetivo:** producto pequeño para desarrolladores, sin requerir inferencia.
**Depende de:** E1; puede avanzar antes de E2/E3.

- [ ] E4.1. Implementar `POST /v1/read`: URL pública o contenido enviado por el
  cliente, respuesta Markdown/texto/JSON, URL final y fecha de obtención.
- [ ] E4.2. Solicitar Markdown al origen cuando lo soporte; convertir HTML como
  alternativa, preservando títulos, tablas, listas, enlaces y bloques de código.
- [ ] E4.3. Aplicar protección SSRF, límites y señales del sitio. `llms.txt` es
  una pista opcional, no autorización ni sustituto de robots/políticas del sitio.
- [ ] E4.4. Añadir tokens de desarrollador, cuotas y contrato independiente de
  `/api/v1`. No dar permisos administrativos al cliente de IA.
- [ ] E4.5. Devolver trazabilidad: origen, hash, método de extracción, límites,
  truncamiento y advertencias de contenido incompleto.
- [ ] E4.6. Medir tokens con tokenizer identificado; marcar estimaciones como
  estimaciones. No convertir ahorro de bytes directamente en ahorro facturado.
- [ ] E4.7. Corpus de español/inglés: artículos, documentación, tablas, código y
  páginas con poco contenido; comparar extracción con referencias revisadas.

**Para liberar:** extrae contenido principal y referencias sin inventar texto;
rechaza destinos internos; respeta límites y comunica truncamiento. **Medir:**
tokens, cobertura de datos, p95, fallos y coste por mil lecturas. **Revertir:**
deshabilitar la API de lectura sin afectar el proxy.

## 8. E5 — MCP, presupuesto de contexto y reutilización

**Objetivo:** que agentes consuman la API de forma sencilla y económica.
**Depende de:** E4.

- [ ] E5.1. Exponer herramienta MCP de lectura; empezar por transporte local
  stdio y añadir HTTP autenticado solo con aislamiento y permisos definidos.
- [ ] E5.2. Implementar `budget` de tokens y selección de fragmentos por
  consulta; conservar negaciones, números, unidades y referencias.
- [ ] E5.3. Deduplicar por sesión/usuario sin compartir contenido privado.
- [ ] E5.4. Caché exacta con claves por contenido, versión, tarea, idioma y
  permisos. Para resultados de modelo, incluir modelo/parámetros y prompt.
- [ ] E5.5. Evitar prompt injection: tratar la página como datos, sin ejecutar
  instrucciones ni disparar herramientas a partir de lo descargado.
- [ ] E5.6. Publicar ejemplos MCP y métricas de bytes/tokens/calidad por separado.

**Para liberar:** dos usuarios de prueba no pueden leer sesiones/cachés privadas
ajenas; un presupuesto reduce contexto con cobertura medida frente a E4. Meta
inicial propuesta: conservar al menos 95 % de la calidad de respuestas del
corpus completo; no es un resultado alcanzado.

## 9. E6 — Integración gradual con Midori

**Objetivo:** activación sencilla, ahorro visible y navegación recuperable.
**Depende de:** E2 para modo sin CA; E1 y revisión de TLS para modo completo.

- [-] E6.1. Completar contrato de descubrimiento: servidor actual, cliente aún
  pendiente; añadir altas públicas limitadas, sin exponer el token administrativo.
- [ ] E6.2. WebExtension compartida: activación, perfil, exclusiones, estadísticas,
  restauración del original y conexión directa tras fallos del servicio.
- [ ] E6.3. Desktop: integración de ajustes, instalación/empaquetado y pruebas
  contra el navegador real, no solo Chromium.
- [ ] E6.4. Android: extensión integrada, canal Kotlin/extensión, ajustes de
  red móvil/Wi-Fi y pruebas en cambios de conectividad.
- [ ] E6.5. Empezar con imágenes sin CA. Ofrecer MITM como elección explícita,
  con importación/revocación limitada de confianza y exclusiones sensibles.
- [ ] E6.6. Probar móvil real, emulador y Desktop: servicio caído, cautivo,
  autenticación caducada, sitios con pinning y restauración de navegación.
- [ ] E6.7. Piloto interno, beta opt-in y documentación en español; medir uso,
  fallos y abandonos antes de ampliar distribución.

**Para liberar:** activar/desactivar no deja configuración residual; el servicio
caído recupera navegación directa; el usuario distingue ahorro de datos y MITM.
Desktop y Android cierran sus pruebas por separado.

## 10. E7 — Evitar descargas y mejorar sesiones con vídeo

**Objetivo:** ahorrar antes de transferir y comprimir.
**Depende de:** E1; cada bloque siguiente se libera de forma independiente.

- [ ] E7.1. Añadir acciones `block`, `respond` y `drop_params`, con reglas de
  prueba, excepciones y explicación visible de su efecto.
- [ ] E7.2. Piloto de listas por dominio para rastreo/amenazas, con procedencia,
  integridad, licencia comprobada, actualización y reversión.
- [ ] E7.3. Informes por dispositivo de bloqueos y bytes evitados, claramente
  separados de los bytes ahorrados por compresión.
- [ ] E7.4. Caché HTTP pública con variantes, revalidación condicional y políticas
  de privacidad; después, `stale-while-revalidate` y `stale-if-error`.
- [ ] E7.5. Carga diferida y preferencias de fuentes como experimentos opt-in;
  proteger imagen LCP, iconos y accesibilidad. No cambiar `defer` globalmente.
- [ ] E7.6. Limitar variantes HLS/DASH por perfil conservando audio y una opción
  reproducible; excluir DRM, firmas y manifiestos no comprendidos.
- [ ] E7.7. Control de autoplay/resolución desde el cliente de Midori, con botón
  para restaurar calidad. No depender de APIs privadas de YouTube para liberar.
- [ ] E7.8. Evaluar HTTP/3 hacia el origen con fallback a HTTP/2 y métricas de
  mejora; no asumir que HTTP/3 del sitio equivale a HTTP/3 del forward proxy.

**Para liberar:** un piloto por función, sin nuevas roturas del corpus; vídeo
con opción explícita y sin empeorar buffering. Limitar resolución sí reduce
calidad; no incluirlo bajo una promesa de compresión sin pérdida.

## 11. E8 — Asistente y funciones de IA de mayor valor

**Objetivo:** convertir lectura y contexto medidos en funciones para usuarios.
**Depende de:** E5; interfaz de navegador depende también de E6.

- [ ] E8.1. Resumen y preguntas sobre la página bajo acción explícita, con citas
  y posibilidad de revisar el texto usado.
- [ ] E8.2. BYOK o proveedor administrado: cuotas, límite de gasto, cancelación,
  streaming y errores comprensibles; claves fuera de logs.
- [ ] E8.3. Extracción a JSON con esquema y validación; devolver evidencia por
  campo y señalar lo que no aparece en la fuente.
- [ ] E8.4. Búsqueda con paquete de fuentes y consulta relevante, presupuesto
  total y deduplicación. Medir coste del proveedor de búsqueda e inferencia.
- [ ] E8.5. Traducción y comparación de varias páginas como ampliaciones opt-in.
- [ ] E8.6. Piloto de AI Gateway/Workers AI detrás de interfaz sustituible,
  retención controlada y sin caché compartida de conversaciones privadas.
- [ ] E8.7. Medir calidad, coste real por tarea, satisfacción y retención antes
  de fijar planes de pago. Los precios del doc. 08 son hipótesis históricas.

**Para liberar:** citas verificables, datos privados aislados, coste limitado y
calidad medida en español; sin envío automático de navegación al modelo.

## 12. E9 — Consolidación y versión estable

- [ ] E9.1. Cerrar solo los compromisos seleccionados para 1.0; las ideas de
  laboratorio no bloquean una versión estable con alcance explícito.
- [ ] E9.2. Validar matriz Linux/macOS/Windows y ARM donde se distribuye, backends
  opcionales y certificados, con pruebas de carga y recuperación.
- [ ] E9.3. Guías de usuario, operador y desarrollador; política de privacidad y
  documentación de límites coherentes con las funciones entregadas.
- [ ] E9.4. Publicar binarios, contenedores, checksums y notas; evaluar `.deb`
  según usuarios reales. Mantener canal beta y restauración documentada.
- [ ] E9.5. Observar una semana de uso interno sin incidencias pendientes y
  repetir controles de seguridad sobre el artefacto final, no solo el código.

## 13. Ideas adicionales conservadas, fuera del camino inicial

| Estado | Idea del plan anterior o nueva | Condición para retomarla |
|---|---|---|
| [] | Resolver upstream DoH/DoT, DNSSEC y comprobación de CNAME | Demanda de privacidad y medición de disponibilidad; antes que operar un DNS público propio |
| [] | Servidor DoH/DoT y categorías/SafeSearch | Producto familiar o empresarial con políticas y excepciones claras |
| [] | CA jerárquica, rotación de intermedia y clave en TPM/KMS | Antes de ampliar a un servicio MITM público de gran alcance |
| [] | Cifrado de caché en disco | Política de retención cerrada y gestión/rotación de claves |
| [] | Análisis de descargas y desarmado SVG | Modo de seguridad explícito; revisar límites y política ante fallo del escáner |
| [] | Dominios sospechosos y anomalías por dispositivo | Datos suficientes y falsos positivos medidos, sin registrar navegación innecesaria |
| [] | ECH, diccionarios Brotli/Zstd, 103 y prefetch | Soporte real de clientes y beneficio frente al coste; no inferirlo de un RFC |
| [] | Addons, hooks y maplocal/mapremote | Dos o más extensiones reales que justifiquen un framework |
| [] | Upstream/SOCKS5/reverse/transparent/WireGuard | Necesidad concreta, pruebas por plataforma y alcance separado |
| [] | Caché distribuida y balanceo L4 | Saturación medida de un nodo y cuotas/estado resueltos |
| [] | WebAuthn o mTLS para administración | Requisitos operativos; acceso remoto protegido antes de exposición pública |
| [] | Lo-Fi, grises, bloqueo de fuentes | Solo perfiles explícitos y pruebas de legibilidad/accesibilidad |
| [] | LLMLingua, delta de plantillas y caché semántica | Superar E5 en calidad/coste con experimentos; evitar respuestas aproximadas compartidas de inicio |
| [] | Gateway empresarial multiproveedor | Clientes reales que necesiten algo que los proveedores existentes no cubran |
| [] | Experimentos SABR, estrangulamiento o vídeo en el borde | Beneficio demostrado y reproducción estable; no compromisos de la primera versión |

## 14. Plantilla mínima de cierre de cada entrega

- [ ] Implementación, documentación y pruebas del alcance elegido.
- [ ] Comparación con la línea base usando los mismos recursos y configuración.
- [ ] Entorno interno, después piloto voluntario y finalmente ampliación gradual.
- [ ] Opción de desactivación/reversión comprobada antes de ampliar.
- [ ] Resultados: errores, latencias, calidad, consumo y coste; marcar X únicamente
  lo demostrado. Una reducción de bytes no justifica una regresión de navegación.

Umbrales iniciales propuestos: cero nuevas roturas en el corpus de aceptación y
no degradar p95/LCP más de 10 % salvo una decisión explícita del perfil. El tamaño
del corpus y los umbrales finales se fijan con la línea base de E1. No son cifras
alcanzadas ni sustituyen la observación de usuarios reales.
