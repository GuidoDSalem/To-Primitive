# To-Primitive

Editor web local para reconstruir imágenes y videos con formas geométricas en vivo, usando
[fogleman/primitive](https://github.com/fogleman/primitive) como motor.

Subís una imagen o un video, movés los parámetros en el panel izquierdo y el resultado se va
dibujando forma por forma en el panel principal. Los videos se ajustan sobre algunos frames de
prueba y después se renderizan completos a MP4.

## Uso

```sh
docker compose up --build
```

Abrí <http://localhost:8080>.

Sin Docker (Go 1.24+; para video hace falta `ffmpeg` en el `PATH`):

```sh
go run .            # o: go run . -addr :9000
go test ./...
```

## Funciones

- **Edición en vivo**: cada cambio de parámetro se refleja al instante.
  - *Cantidad de formas*: no reinicia la corrida. Si la bajás, oculta formas; si la subís, sigue agregando desde donde estaba.
  - *Tipo de forma, opacidad, repetición, resolución y fondo*: reinician la corrida con los valores nuevos.
- **Pausar / Continuar** y **Regenerar** (una corrida nueva con los mismos parámetros; el algoritmo es aleatorio).
- **Ver original**: mantené apretado el botón o la barra espaciadora para comparar.
- **Exportar** lo que ves en pantalla:
  - **PNG** al tamaño que elijas.
  - **SVG** vectorial.
  - **GIF** animado del proceso.

## Video

1. **Subí un video.** Se abre el editor sobre un frame de la mitad.
2. **Agregá frames de prueba:**
   - Con la línea de tiempo y "Agregar este frame" elegís momentos representativos.
   - Los parámetros son los mismos para todos.
   - Al cambiar de frame se pausa el anterior, para no gastar CPU.
3. **Renderizá** con estas opciones:
   - *Cuadros por segundo*: el original o menos. Menos fps = render más rápido.
   - *Seguimiento del movimiento*: ver abajo.
   - *Tamaño de salida*.
   - *Mantener audio*.
4. **Mientras renderiza:**
   - La pestaña **Render** muestra en vivo el último frame generado.
   - El progreso indica frames hechos, velocidad y tiempo restante.
   - Se puede cancelar.
5. **Al terminar** se reproduce el MP4 (H.264) y se puede descargar.

### Suavizado entre frames

Si cada frame se generara desde cero, el resultado titilaría: `primitive` es aleatorio y elige formas distintas cada vez. Por eso:

- **Solo el primer frame** usa la búsqueda completa de `primitive`.
- **Cada frame siguiente** parte de las formas del anterior, en el mismo orden:
  1. Cada forma se re-optimiza con unos pocos pasos de *hill climbing* sobre el frame nuevo, en paralelo en todos los núcleos, y se queda con el mejor resultado.
  2. Su color se recalcula.
  3. Si una forma ya no mejora la imagen (por ejemplo, en un cambio de escena), se reemplaza por una buscada desde cero.

El slider *Seguimiento del movimiento* regula cuántos pasos de re-optimización recibe cada forma:

| Valor | Efecto |
|---|---|
| 0 | Las formas quedan fijas; solo cambia su color. Lo más estable y rápido. |
| Más alto | Las formas se desplazan para seguir el movimiento. Más fiel, un poco más lento. |

Además es mucho más rápido que buscar de cero: con 150 formas a 256 px, en 4 núcleos, ~1 s por frame contra ~10 s.

Los tests (`temporal_test.go`) lo verifican con datos:

- **Movimiento chico:** el cambio entre frames suavizados queda cerca del cambio real de la imagen, menos de la mitad que generando de cero.
- **Movimiento grande:** adaptar las formas sigue el movimiento mejor que dejarlas fijas.

## Parámetros

| Parámetro | Equivalente en `primitive` | Descripción |
|---|---|---|
| Cantidad | `-n` | Número de formas |
| Tipo | `-m` | Triángulo, rectángulo, elipse, círculo, rect. rotado, Bézier, elipse rotada, polígono o combinado |
| Opacidad | `-a` | 1–255, o automática (`0`): el algoritmo elige la opacidad de cada forma |
| Repetición | `-rep` | Formas extra por iteración con búsqueda reducida |
| Resolución de trabajo | `-r` | Tamaño al que se reduce la imagen para optimizar. Más chica = más rápido |
| Fondo | `-bg` | Color promedio de la imagen, o uno elegido |

## Arquitectura

- **Backend** (Go: `main.go`, `session.go`, `render.go`, `video.go`, `temporal.go`):
  - Usa `primitive` como librería.
  - Cada imagen subida es una *sesión* con una corrida en segundo plano.
  - Las formas se envían al navegador por **Server-Sent Events** a medida que se generan.
  - Los parámetros se cambian con `PUT /api/sessions/{id}/params`.
- **Frontend** (`web/`): HTML, CSS y JS plano, sin build ni dependencias. Va embebido en el binario.
- **Video:** `ffmpeg` decodifica los frames y codifica el MP4 por pipes, sin archivos intermedios.
- **Dependencias Go**:
  - `primitive` y las suyas: `gg`, `freetype`, `resize`, `x/image`.
  - Nada más.

| Endpoint | Descripción |
|---|---|
| `POST /api/sessions` | Sube una imagen (multipart, campo `image`) |
| `GET /api/sessions/{id}/events` | Stream SSE: `snapshot`, `shape`, `status` |
| `PUT /api/sessions/{id}/params` | Cambia los parámetros (JSON; `restart: true` fuerza una corrida nueva) |
| `GET /api/sessions/{id}/export?format=png\|svg\|gif&size=1024&n=200` | Descarga el resultado |
| `POST /api/videos` | Sube un video (multipart, campo `video`) |
| `GET /api/videos/{id}/frame?t=1.5&size=320` | Frame crudo en JPEG (previsualización) |
| `POST /api/videos/{id}/frames` | Crea una sesión de edición sobre el frame `t` |
| `GET /api/videos/{id}/events` | Stream SSE: `job` (progreso) y `preview` (último frame) |
| `POST /api/videos/{id}/render` | Inicia el render (parámetros + `fps`, `adapt`, `size`, `audio`) |
| `DELETE /api/videos/{id}/render` | Cancela el render |
| `GET /api/videos/{id}/output` | El MP4 resultante (`?download=1` para descargarlo) |
