# 17. Cierre técnico de imágenes y validación local

Trabajo del **17 de noviembre de 2026**, con agentes de calidad/color, JXL
portable y aislamiento, y verificación de integración. Alcance: servicio local
Linux amd64, API pública de imágenes y extensión Chrome MV3. Las marcas X del
documento 13 describen los contratos y límites de este documento, no una
certificación de todos los observadores, archivos, sistemas o sitios posibles.

## Contratos implementados

| Punto | Implementación | Prueba revisable |
|---|---|---|
| Fidelidad | Proxy y `/img` verifican a resolución completa. Las calidades fijas no evitan el umbral. Animación: todos los fotogramas, tiempos y repeticiones. Política `pixels`: píxeles decodificados exactos o recurso original. | `internal/img/verification_test.go`, `color_quality_test.go`, `engine_test.go`; `internal/pipeline/quality_policy_test.go` |
| Color amplio | LittleCMS 2.19.1 WASM incluido: ICC RGB, GRAY, CMYK, matrices/TRC y LUT; orientación y presentación; PNG gAMA/cHRM/cICP y AVIF NCLX SDR. | Corpus de 16 bits en `internal/colorcms/testdata`; comparación con LittleCMS nativo; JPEG CMYK LUT y AVIF real con ICC, recorte, rotación y espejo |
| Tamaño visible | Extensión real: `currentSrc`, `picture/srcset`, estilos computados, `object-fit`, fondos CSS, DPR, viewport y cambios de JavaScript. | `web/scenarios/extension.mjs`, Chrome MV3 con perfil temporal y servidor sintético; cambios durante una respuesta lenta de fondo |
| Permisos | Worker descartable con seccomp TSYNC y límites del kernel. Procesa imágenes, dimensiones, comparación, fuentes, compresión, minificación y pistas HTML después de aplicar la política. | `internal/sandbox/sandbox_linux_test.go`, `internal/codecworker/worker_test.go`, `internal/pipeline/isolated_test.go` |
| JXL de entrada | Inspector libjxl, alfa, ICC, orientación, profundidad, tiempos, repeticiones, codestream y contenedor fragmentado; salida RGBA16 gestionada. | `internal/jxlcodec/codec_test.go`, `internal/imgmeta/jxl_general_test.go`, `internal/img/color_hdr_test.go` |
| JPEG→JXL reversible | Coeficientes JPEG y metadatos de reconstrucción; comprobación de cada byte. Ruta WASM portable además de binding nativo opcional. | `CGO_ENABLED=0`, `-tags nodynamic` y sin `LD_LIBRARY_PATH`; baseline, progresivo, EXIF, gris, submuestreo y restart |

La equivalencia perceptual universal no es un resultado que una métrica pueda
demostrar. Se sustituyó esa promesa por contratos comprobables: umbral medido o
píxeles exactos, con original ante resultados no admitidos. No se rebajaron los
umbrales para mejorar porcentajes de ahorro.

`color_policy: preserve` es el valor predeterminado: conserva recursos que
requerirían cambiar de espacio de color. `srgb` autoriza conversión SDR explícita
y la informa como `appearance_change`, sin publicar una métrica engañosa contra
el original de color amplio. El corpus de P3 saturado demuestra que unos valores
RGB numéricamente iguales pueden representar colores diferentes después de
recortar el gamut. HDR PQ/HLG conserva el original incluso con `srgb`; el motor
no habilita el tone mapping de bajo nivel. Perfiles rotos, recortes ambiguos o
fraccionarios, transferencias JXL no clasificables y canales JXL auxiliares fuera
de RGBA también conservan el original.

La extensión cubre el documento principal y los elementos descritos en su
[README](../extension/README.md). Iframes, shadow DOM, pseudoelementos, canvas,
`image-set` y transformaciones no interpretables quedan originales. Puede
necesitar la primera descarga original para obtener dimensiones naturales; sus
estadísticas de variantes no demuestran que esa descarga inicial se haya evitado.

## Aislamiento y rutas reales

En Linux amd64/arm64 el worker instala `no_new_privs` y un filtro seccomp
sincronizado para hilos existentes y futuros. Deniega apertura de archivos,
sockets, exec, ptrace, io_uring y creación de otros procesos. Cierra descriptores
heredados ajenos al runtime. Aplica límites de memoria, CPU, descriptores y
tamaño de archivos/core; el padre impone deadline y espera al proceso cancelado.
El padre exige `Sandbox.Enforced` en cada respuesta. La ejecución se probó en
Linux amd64; en plataformas sin implementación se conserva el original.

La inicialización de bindings dinámicos ocurre antes del filtro y no recibe el
recurso. La interpretación del recurso comienza después. Hay cuatro slots
globales de workers, además de las cuotas propias de `/img`. Service muestra
la política comprobada, fecha, límites y ocupación. La opción de benchmark
`-unsafe-in-process` es explícita y no se expone como configuración del servidor.

| Ruta | Ejecución |
|---|---|
| JPEG decode, PNG, GIF, fuentes, texto y pistas HTML | Go en worker |
| JPEG encode | Wazero; Go generado en arm64 o `-tags wasm2go` |
| WebP | Biblioteca nativa si se carga; en otro caso Go generado desde WASM |
| AVIF | Biblioteca nativa si se carga; en otro caso Wazero o Go generado con `wasm2go` |
| JXL píxeles e inspector | Módulo libjxl WASI incluido, incluso con libjxl nativa instalada |
| JPEG→JXL reversible | Binding nativo compatible o módulo WASI incluido |
| ICC | LittleCMS WASI incluido |

Los módulos WASM no tienen directorios ni red montados. Sus fuentes de
construcción, versiones y licencias acompañan los artefactos en
`internal/jxlcodec` y `internal/colorcms`. La etiqueta histórica del backend
`wasm` no se usa como prueba de aislamiento ni como inventario de ejecución.

Texto sin longitud conocida se reenvía inmediatamente como llegó. Se encontró
y corrigió una regresión que esperaba EOF al intentar almacenarlo en un buffer:
la prueba ahora exige headers y primer fragmento antes de que el origen cierre.
No se usa compresión o parsing fuera del worker para evitar ese bloqueo.

## Calidad y coste

Antes de codificar, el motor reconstruye la geometría reducida a resolución
original y mide su fidelidad. Si ya incumple el umbral, codifica a resolución
original desde el inicio. Si la geometría pasa pero el encoder daña demasiado el
resultado, puede probar una sola vez la resolución original, con el presupuesto
restante y el mismo umbral. El coste de preparación descuenta presupuesto. No
registra un fallo geométrico como imposibilidad global de un formato.

Muestra reproducible del test `TestResizePreflightSkipsDamagingGeometryAndStillCompresses`:
JPEG de 480×320, **98.747 bytes**, pasó a WebP de las mismas dimensiones,
**33.944 bytes (65,6 % de ahorro)**, con **SSIM 0,95288 ≥ 0,95**, usando `saver`
y métrica SSIM. La geometría de 120×80 se rechazó antes de codificar. Con presupuesto agotado no hay
segundo intento. Es una muestra de regresión, no la media de los sitios visitados.

La primera compilación JXL WASM cuesta aproximadamente tres segundos en las
pruebas locales del módulo. El código compilado se comparte dentro del proceso;
un worker nuevo vuelve a pagar ese coste. Las conversiones se cachean, pero no
se presenta la ruta portable fría como más rápida que la nativa. La API y el
proxy descargan el recurso antes de consultar su caché de transformaciones.
Ahorro hacia el cliente, coste de cómputo y tiempo de carga se miden por separado.

## Tráfico continuo y evidencia

El servicio de usuario `midopress-traffic` mantiene solicitudes de imagen cada
cuatro segundos, alternando balanced, saver, extreme y lossless. En paralelo
visita páginas públicas de YouTube, Amazon, Alibaba, AliExpress, Google,
Facebook, Pinterest, Wikipedia, GitHub y otros sitios. No inicia sesión ni evade
desafíos. Las respuestas fallidas o incompletas no acreditan ahorro.

`midopress-observe` registra snapshots del dashboard, ocupación, backend y logs.
Los flujos y dominios están activados; la captura de cuerpos está desactivada.
Ambos procesos continúan durante compilaciones y pruebas. El reinicio necesario
para sustituir el binario se registra; no se presenta como despliegue sin corte.

Evidencia local regenerable en `artifacts/`:

- `closure-go-test-final.log`, `closure-go-vet.log`, `closure-build.log` y
  `closure-admin-build.log`: verificación del estado final.
- `closure-playwright.log`: panel, autenticación, tráfico, imágenes y políticas
  en escritorio/móvil; `extension-browser-tests.log`: extensión real.
- `jxl-portable-nodynamic-tests.log`, `jxl-portable-final-tests.log` y
  `codec-execution-*-tests.log`: portabilidad, metadatos y rutas reales.
- `codec-sandbox-tests.log`, `codec-integration-focused.log` y
  `local/quality-color-*.log`: permisos y regresiones de calidad/color.
- `continuous/status.json`, `observer.json` y `observations.jsonl`: tráfico,
  contadores y observación continua; los eventos por imagen están en el journal
  de `midopress-traffic`.

El contrato y la instalación de la extensión están en el
[documento 16](16-api-imagenes.md) y su [README](../extension/README.md).
Esto cierra implementación y aceptación local de estas funciones. La matriz
general de compatibilidad, publicación, cuotas persistentes y demostración de
aceleración general de E1/B12 siguen siendo tareas distintas.

## Resultado del despliegue y prueba real

Servicio actualizado a las **05:51:50 UTC**. `midopress-traffic` conservó PID
621898 y `midopress-observe` PID 724142: no se detuvieron ni reiniciaron durante
la sustitución del proxy. Los tres servicios quedaron activos. La observación
guardó el reinicio y los nuevos contadores. Capturas reales del panel y de
Service en `artifacts/extension-live/dashboard.png` y `service.png`.

La extensión real recibió una credencial temporal, llamó a la API desplegada y
reemplazó una JPEG pública de 550 píxeles sin cambiar su ancho. Ahorró **12.694
bytes**, con **0 errores** de extensión. Service confirmó **seccomp TSYNC
aplicado**, permisos de archivos/red/procesos denegados y cuatro slots. El valor
de CPU del probe mostrado en Service es el de esa verificación inicial; cada
operación recibe su límite según el deadline, hasta el máximo de la política.
Evidencia: `extension-live/results.json` y `extension-live-tests.log`.

La API real produjo estos resultados sobre la misma JPEG pública de **44.891
bytes**, a resolución original y sin cambiar los perfiles predeterminados:

| Perfil | Resultado | Ahorro | DSSIM / máximo | Variante fría | Caché de conversión caliente |
|---|---:|---:|---|---:|---:|
| balanced | AVIF, 32.197 B | 28,3 % | 0,00184875 / 0,002 | 1.310 ms | 141 ms |
| saver | AVIF, 20.866 B | 53,5 % | 0,00541594 / 0,006 | 890 ms | 73 ms |
| extreme | AVIF, 10.382 B | 76,9 % | 0,0138992 / 0,015 | 679 ms | 113 ms |
| lossless | JPEG original, 44.891 B | 0 % | Píxeles exactos | 154 ms | 81 ms |

Las tres variantes usan el AVIF nativo dentro del worker aislado. La segunda
solicitud dio `hit` en los cuatro perfiles y devolvió los mismos bytes. La firma
alterada devolvió 403, una cookie de navegación 409 y un destino privado fue
rechazado al firmar con 422. Prueba reproducible desde `web/`:
`node scenarios/image-live.mjs`; evidencia en `image-live-results.json`.

Estos tiempos son observaciones de una imagen, no LCP ni una comparación causal
de velocidad entre sitios. Las variantes frías métricas tardaron más que obtener
el original: el coste de codificación existe. Las solicitudes calientes siguen
descargando el origen; no se atribuye ese tráfico a ahorro de Internet.

El journal posterior al despliegue registró **61 solicitudes de imagen al proxy,
61 con HTTP 200 y TLS verificado**, alternando los cuatro perfiles. Las últimas
cinco por perfil dieron medianas de 62–70 ms y los mismos tamaños de la tabla.
La generación continuó después de registrar esa muestra. Fuentes:
`continuous/post-deploy-summary.json`, `post-deploy-journal.jsonl`,
`closure-service-status.txt` y `closure-service.log`.
