# To-Primitive

Editor web local para reconstruir imágenes con formas geométricas en vivo, usando
[fogleman/primitive](https://github.com/fogleman/primitive) como motor.

Subís una imagen, movés los parámetros en el panel izquierdo y el resultado se va
dibujando forma por forma en el panel principal.

## Uso

```sh
docker compose up --build
```

Abrí <http://localhost:8080>.

Sin Docker (Go 1.24+):

```sh
go run .            # o: go run . -addr :9000
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

- **Backend** (Go, `main.go`, `session.go`, `render.go`):
  - Usa `primitive` como librería.
  - Cada imagen subida es una *sesión* con una corrida en segundo plano.
  - Las formas se envían al navegador por **Server-Sent Events** a medida que se generan.
  - Los parámetros se cambian con `PUT /api/sessions/{id}/params`.
- **Frontend** (`web/`): HTML, CSS y JS plano, sin build ni dependencias. Va embebido en el binario.
- **Dependencias Go**:
  - `primitive` y las suyas: `gg`, `freetype`, `resize`, `x/image`.
  - Nada más.

| Endpoint | Descripción |
|---|---|
| `POST /api/sessions` | Sube una imagen (multipart, campo `image`) |
| `GET /api/sessions/{id}/events` | Stream SSE: `snapshot`, `shape`, `status` |
| `PUT /api/sessions/{id}/params` | Cambia los parámetros (JSON; `restart: true` fuerza una corrida nueva) |
| `GET /api/sessions/{id}/export?format=png\|svg\|gif&size=1024&n=200` | Descarga el resultado |

## Próximos pasos

- **Video**:
  - En el editor se eligen algunos frames para ajustar los parámetros rápido.
  - Después se renderiza el video completo frame a frame (con ffmpeg), mostrando el progreso y exportando a MP4.
