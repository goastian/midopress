# MidoPress Images para Chrome

Extensión Manifest V3 para probar `/img` sin instalar una CA. Se activa por
pestaña y utiliza una credencial temporal limitada; nunca necesita el token de
administración ni la clave HMAC del servidor.

## Prueba local

1. Mantener MidoPress y su API de imágenes en ejecución.
2. Abrir `http://localhost:9998/#/images`. En **Browser extension credential**,
   elegir los hosts públicos de imágenes, el perfil, ancho CSS máximo, DPR y
   caducidad. Emitir y copiar la credencial y el origen de la API.
3. Abrir `chrome://extensions`, habilitar el modo desarrollador y cargar esta
   carpeta con **Load unpacked**.
4. En las opciones de la extensión, guardar el origen de la API, la credencial
   limitada y el mismo perfil. Para una API remota se requiere HTTPS y permiso
   explícito para su origen. HTTP solo se admite en localhost.
5. Abrir una página con imágenes públicas de esos hosts. Activar la extensión
   desde su botón. El popup muestra las sustituciones y los bytes ahorrados.
   **Restaurar** recupera los atributos y las fuentes originales.

La credencial dura de 1 a 60 minutos y permite hasta 120 firmas por minuto.
Los límites se comprueban en el servidor. Al caducar o superar el alcance se
conserva la imagen original. Borrar la credencial de las opciones elimina su
copia local; rotar la clave del servidor invalida todas las credenciales.

## Medición y alcance

Se usan el layout real, `currentSrc`, estilos computados, dimensiones naturales,
`object-fit`, fondos CSS, DPR, `ResizeObserver`, `MutationObserver` e
`IntersectionObserver`. Los cambios de viewport vuelven a dejar que el navegador
seleccione `picture`/`srcset` antes de medir. Solo se procesan elementos visibles.

Esta versión cubre el documento principal, imágenes HTML y fondos `url()` de
elementos observados. Iframes, shadow DOM, pseudoelementos, canvas, `image-set`,
transformaciones 3D y composiciones CSS no interpretables conservan el original.
No se presenta una pista HTML como una medición del navegador.

La extensión puede necesitar que el original haya cargado para conocer sus
dimensiones naturales; esta beta demuestra sustitución y ahorro de la variante,
no que elimine esa primera descarga. El perfil `pixels` conserva los píxeles y
no redimensiona. El perfil métrico también puede conservar el original si el
tamaño solicitado incumple el umbral.

Las solicitudes a la API omiten cookies, Referer y credenciales de navegación.
La credencial limitada solo se envía a `/img/sign`; el recurso firmado se obtiene
sin Authorization. El servidor solo descarga recursos públicos. Activar esta
beta en páginas públicas y autorizar sus CDN de imágenes, sin imágenes privadas
dependientes de la sesión. Los fallos de red, formato, permisos, CSP o límites
conservan el original.

## Pruebas reproducibles

Desde `web/`, con Chrome para Testing y Playwright instalados:

```sh
node scenarios/extension.mjs
```

La prueba usa un perfil temporal y carga la extensión real. Comprueba medición,
DPR, object-fit, cambios de JavaScript, picture responsive, inserciones dinámicas,
fondos CSS y restauración contra un servidor sintético. Las pruebas Go y E2E
validan por separado HMAC, caducidad, alcance, SSRF y códecs de la API real.
