# To-Primitive

Editor web local para reconstruir imágenes y videos con formas geométricas en vivo, usando
[fogleman/primitive](https://github.com/fogleman/primitive) como motor.

Subís una imagen o un video, movés los parámetros en el panel izquierdo y el resultado se va
dibujando forma por forma en el panel principal. Los videos se ajustan sobre algunos frames de
prueba; después el servidor calcula *keyframes* y el navegador los anima como motion graphics
vectoriales, a cualquier resolución, y los exporta a MP4, Lottie o JSON.

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

## Grabar la construcción de una imagen

En la pestaña **Grabación** se reproduce la imagen armándose forma por forma, y se exporta a MP4 (en el navegador, a 24/30/60 fps y de 720p a 4K). Todo se ve al instante, sin recalcular nada.

Cada opción se elige con una **tarjeta con vista previa animada**: una mini grabación en bucle con tu imagen que muestra esa opción combinada con el resto de lo que elegiste. Las curvas de velocidad se muestran como gráficos, con un punto que las recorre y una barra de progreso. Hasta que el editor tiene formas, las tarjetas usan una imagen de demostración. Las opciones son:

- **Dirección:** construir, deconstruir, o construir y deconstruir.
- **Orden de aparición:**
  - como las encontró el algoritmo (grandes primero) o al revés;
  - aleatorio;
  - por tamaño;
  - de arriba hacia abajo o de izquierda a derecha;
  - del centro hacia afuera o de afuera hacia el centro.
- **Velocidad (curva de *easing*):** cómo se reparten las formas en el tiempo. Un gráfico muestra la curva elegida.

  | Curva | Ritmo |
  |---|---|
  | Constante | La misma cantidad de formas por segundo |
  | Acelera (suave, normal, fuerte) | Empieza lento y va cada vez más rápido |
  | Frena (suave, normal, fuerte) | Empieza rápido y va frenando |
  | Lento → rápido → lento | Velocidad en forma de campana |
  | Rápido → lento → rápido | Campana invertida |
  | Por impacto visual | Cada forma dura según cuánto mejora la imagen: las que cambian mucho se ven con calma y los detalles pasan rápido |
- **Efecto de aparición:** de golpe, fundido, crecer, rebote o caer desde arriba, con su duración.
- **Duración**, pausa al inicio y pausa al final.

El orden de aparición nunca cambia el orden de dibujo, así que el último frame es siempre exactamente la imagen del editor. Lo verifiqué en todas las combinaciones de orden y efecto.

### Escena 3D

Con **Estilo → Escena 3D**, la grabación pone cada forma como una lámina flotando en el espacio:

- **Corrección anamórfica:** cada forma está a su propia profundidad y agrandada en proporción a su distancia. Así, **desde la cámara frontal se ve exactamente la imagen**, y desde cualquier otro ángulo se ven las capas con parallax.
- **Profundidad de cada forma:** por orden (las primeras al fondo), por tamaño, por brillo o aleatoria, con separación ajustable.
- **Cómo se arman:** llegan desde el fondo, explotan desde todas partes (con giro), caen desde arriba, o solo aparecen con fundido. La distancia del recorrido es ajustable.
- **Escalonado:** de todas juntas a una detrás de otra, en el orden de aparición elegido. La curva de velocidad y la dirección se comparten con el modo 2D.
- **Cámara:**
  - Revelar: termina de frente justo cuando la imagen se completa.
  - Vaivén.
  - Barrido de lado a lado.
  - Vuelta completa.
  - Fija.

  El ángulo es ajustable.

Implementación sin dependencias (`web/scene3d.js`):

- Proyección en perspectiva propia sobre Canvas 2D: cada forma se convierte en polígono y se proyectan sus vértices, lo que es exacto para piezas planas.
- Las elipses se muestrean con 48 puntos y las Bézier con 20.
- De frente, las formas se dibujan en el orden de primitive, así que colores y transparencias quedan iguales; desde atrás, en orden inverso, para que la oclusión sea correcta.
- Verificado: con la cámara de frente, la escena coincide con la imagen del editor (diferencia media 0.04 sobre 255) en todos los modos y separaciones de profundidad. Un frame 1080p tarda ~1–2 ms.

## En vivo (webcam)

Con **Usar cámara (en vivo)**, la webcam se transforma en formas en tiempo real. Se puede grabar el resultado (MP4 en Chrome/Edge/Safari, WebM en otros) o capturar la ventana con OBS para transmitirlo.

**Cómo funciona:**

- **Sin cola de frames:** el servidor pide un frame cuando termina el anterior (evento `ready`), así siempre trabaja sobre el más reciente y no se acumula atraso.
- **Cada keyframe tiene un presupuesto de tiempo** (por defecto 120 ms, ajustable):
  - Las formas sobre las **zonas que cambiaron** se re-optimizan primero. La zona se mide con un mapa de diferencias entre frames que se consulta en tiempo constante.
  - Si sobra tiempo, se refinan por turnos las de las zonas quietas.
  - El resto solo recalcula su color, que es casi gratis.
- **Arranque progresivo:** la imagen arranca con pocas formas y suma más en cada keyframe hasta llegar a la cantidad pedida. Las formas nuevas usan una búsqueda rápida: la de primitive tarda ~100 ms por forma.
- **Dibujo en el navegador:** se dibuja un instante un poco atrasado (latencia más un intervalo entre keyframes), interpolando con el mismo motor que el video. Todos los cuadros de pantalla cambian, aunque los keyframes lleguen a 10–15 por segundo.
- **Parámetros en vivo:** formas, tipo, opacidad, resolución, fondo, seguimiento e interpolación se pueden cambiar mientras corre.

### Flujo óptico

Activado por defecto, en vivo y en video. Antes de cada keyframe se estima **hacia dónde se movió cada zona** de la imagen, y las formas del keyframe anterior se trasladan con ese movimiento: cada vértice según el flujo en su posición, así que también pueden estirarse o girar.

- **Estimación** (`flow.go`, sin dependencias):
  - *Block matching* jerárquico: celdas de 8 px buscadas primero a 1/4 de tamaño (movimientos grandes) y refinadas en cada nivel, con precisión subpíxel y filtro de mediana.
  - Las zonas lisas toman el movimiento de sus vecinas.
  - Cuesta ~3 ms por keyframe a 128 px.
  - Con desplazamientos conocidos (`TestFlowFindsShifts`) acierta hasta 12 px, con error menor a 0.02 px.
- **Refinamiento que no deshace el seguimiento:** después de moverlas con el flujo, el hill climbing refina las formas, pero alejarlas de donde las puso el flujo tiene un costo, proporcional a la distancia al cuadrado. Sin ese costo, las mutaciones al azar de primitive deshacían buena parte del seguimiento.
- **Medido** (`TestLiveFlowQuality`, paneo rápido de ~8 px por keyframe, mismo presupuesto, promedio de 3 corridas):

  | Configuración | Error contra el frame | Distancia de cada forma al movimiento real |
  |---|---|---|
  | 30 pasos, 100 ms, sin flujo | 5.56 | 20.2 px |
  | 30 pasos, 100 ms, con flujo | 5.62 | 7.4 px |
  | 5 pasos, 40 ms, sin flujo | 7.29 | 17.4 px |
  | 5 pasos, 40 ms, con flujo | 7.39 | 8.7 px |

  La fidelidad queda prácticamente igual (la variación entre corridas es de ±0.3), y **las formas siguen el movimiento 2–2.7 veces mejor**: se deslizan con el objeto en vez de saltar.
- **Se puede apagar** con la casilla "Flujo óptico", tanto en vivo como en la sección de video.

**Medido en 4 núcleos**, solo el cálculo del servidor, con frames de video real (`LIVE_BENCH=video.mp4 go test -run TestLiveThroughput -v`):

| Formas | Resolución | Keyframes/s |
|---|---|---|
| 50 | 96 px | ~32 |
| 100 | 128 px | ~14 |
| 200 | 128 px | ~10 |
| 150 | 192 px | ~8 |

En el navegador, con cámara simulada en Chrome:

- 100 formas a 128 px: ~11 keyframes/s, con ~200 ms de retraso en pantalla.
- 50 formas: ~16 keyframes/s, con ~140 ms de retraso.

## Editor de reels

`/reels.html` (enlace "editor de reels" arriba del panel) arma un reel vertical a partir de una foto o un tramo de video.

1. **Fuente.** Subí o arrastrá una foto o un video. En los videos elegís la porción con los dos extremos de la barra: hasta 60 s, como mínimo 0,5 s.
2. **Template.** Cada tarjeta muestra el reel animado con tus formas y tus textos. Sus tiempos son proporcionales a la duración.

   | Template | Fuente | Qué hace |
   |---|---|---|
   | **Revelado** | Foto | Las formas arman la imagen, se sostienen y hacen un fundido a la foto real. El cierre aparece al final. |
   | **Loop hipnótico** | Foto o video | La foto se simplifica hasta las formas grandes y se vuelve a armar, con un zoom que respira; el video va y vuelve. El último cuadro es idéntico al primero, así la repetición automática de las apps no tiene corte. |
   | **Video en formas** | Video | La porción en formas y, en el tramo final, un fundido al video original. |

   *Automático* elige Revelado para fotos y Video en formas para videos.

   Cada template tiene su **configuración base** (duración, cantidad y tipo de formas, animación y frases), que se carga al elegirlo. Lo que cambiaste a mano se respeta al pasar de un template a otro, y *Volver a la configuración base del template* lo restaura todo (los textos escritos se mantienen). Revelado y Video en formas arrancan en 15 s; el Loop, en 10 s.
3. **Duración.** Atajos de 10, 15, 30 y 60 s, o libre (3 a 90 s). En los videos fija el largo de la porción (la mitad en el loop, que va y vuelve). Si elegís la porción a mano, pasa a ser libre.
4. **Encuadre 9:16.**
   - *Llenar* recorta: zoom, posición, y también arrastrar la vista previa o usar la rueda del mouse.
   - *Ajustar* muestra la fuente completa sobre una copia desenfocada.
   - Las zonas rojas marcan lo que tapan TikTok, Reels y Shorts (barra superior, texto de la cuenta, botones). Conviene dejar lo importante fuera de ellas.
5. **Formas, en vivo como en el editor.** Cantidad y tipo; las formas aparecen en la vista previa mientras se buscan y cada cambio se ve al instante, igual que en el editor principal (con *Pausar / Continuar*).
   - Fotos: la sesión trabaja sobre la foto ya recortada a 9:16, así las formas cubren justo el cuadro. Cuando están todas, el reel las usa solo; si cambiás algo, se buscan de nuevo y el reel se actualiza al terminar.
   - Videos: se ajusta sobre un **frame de prueba** del tramo (también recortado); cambiarlo arma otra sesión en ese frame.
6. **Algoritmo.** Los mismos controles que el editor: opacidad (fija o automática por forma), repetición, resolución de trabajo y fondo (color promedio o elegido).
7. **Animación.**
   - Fotos: las opciones de la grabación del editor, con sus tarjetas animadas: orden de aparición, velocidad (curva de easing, en Revelado), efecto y duración del efecto. Se aplican al instante, sin recalcular.
   - Videos: keyframes por segundo, seguimiento del movimiento, flujo óptico e interpolación. *Calcular el video* procesa el tramo con el recorte elegido (pausa la sesión de prueba para liberar la CPU); si después cambiás algo que afecta el cálculo, avisa que hay que calcular de nuevo. La interpolación se aplica al instante.
8. **Frases.** Las del template vienen cargadas (en los actuales, título y cierre) y se pueden agregar más. Cada una tiene texto, posición (arriba, centro, abajo), estilo (título, subtítulo, pregunta o etiqueta con fondo) y desde/hasta en segundos, que se reajustan solos si cambia la duración. Aparecen con un fundido y un leve movimiento, con sombra, y quedan fuera de las zonas que tapan las apps; si no entran, se achican. Pueden usar `{formas}` y `{tipo}` ("La Mona Lisa con {formas} {tipo}" → "La Mona Lisa con 150 triángulos"). La **marca** se ve durante todo el reel.
9. **Audio.** Sin sonido, el sonido del video (el tramo elegido) o un archivo propio, desde el segundo que elijas. Con volumen y fundido de salida. Se escucha al reproducir la vista previa.
10. **Exportar.** El navegador dibuja cada cuadro a 1080×1920 y 30 fps y lo codifica en H.264 (WebCodecs); el servidor le agrega el audio en AAC (recortado a la duración del reel, rellenado con silencio si es más corto y con el fundido) y lo guarda en la biblioteca. El MP4 se descarga solo y está listo para subir a TikTok, Reels o Shorts. Si el navegador solo puede codificar VP9, el servidor lo convierte a H.264.

**Biblioteca.** Al pie del panel están los reels exportados, del más nuevo al más viejo, para verlos, descargarlos o borrarlos. Cada uno guarda un registro con lo que lo generó: template, duración, fuente, tramo, encuadre, formas, textos, audio y datos de la exportación. Sirve para comparar después qué combinación funciona mejor en las redes.

Exportar un reel de 10 s de una foto con 100 formas tarda unos 9 s; uno de 2,5 s de video, unos 4 s (más el cálculo de las formas).

La vista previa reproduce el reel o el original. Si cambiás el encuadre, la porción o los ajustes, avisa que hay que generar de nuevo. Los templates viven en `web/templates.js`, cada uno como una receta: su configuración base, el recorte sobre el que se calculan las formas, el contenido como función del tiempo (así la exportación dibuja exactamente los mismos cuadros que la vista previa) y sus frases, con tiempos relativos a la duración.

Próximas etapas: una cola de publicación con una regla configurable (por ejemplo, un reel por día a cierta hora) y la publicación automática.

## Video

1. **Subí un video.** Se abre el editor sobre un frame de la mitad.
2. **Agregá frames de prueba:**
   - Con la línea de tiempo y "Agregar este frame" elegís momentos representativos.
   - Los parámetros son los mismos para todos.
   - Al cambiar de frame se pausa el anterior, para no gastar CPU.
3. **Calculá la animación.** El servidor calcula *keyframes*: algunos frames por segundo (4–30, por defecto 8).
   - Durante el cálculo, la pestaña **Animación** ya reproduce lo que va saliendo.
   - El progreso indica keyframes hechos, velocidad y tiempo restante, y se puede cancelar.
4. **Reproducí y ajustá** en la pestaña **Animación**:
   - El navegador interpola las formas entre keyframes y las dibuja en un `<canvas>` a la resolución de tu pantalla.
   - El audio original suena sincronizado.
   - La *interpolación* se cambia al instante, sin recalcular: suave (curva continua), lineal o sin interpolar.
5. **Exportá** (en el navegador, a 24/30/60 fps y de 720p a 4K):
   - **MP4**: codificado con WebCodecs en H.264, o en VP9 si el navegador o la resolución no permiten H.264. Si pedís audio, el servidor le agrega la pista original con ffmpeg.
   - **Lottie**: JSON para After Effects (Bodymovin) o para reproducir en la web con `lottie-web`. Cada forma es una capa vectorial animada.
   - **JSON**: los keyframes crudos, para llevarlos a otra herramienta.

### Por qué se ve nítido y fluido

El cálculo y el dibujo están separados:

- **El cálculo** (`primitive`, en el servidor) trabaja a la *resolución de trabajo* (por ejemplo 256 px) y solo decide qué forma va, dónde y de qué color.
- **El dibujo** (Canvas en el navegador) usa esas formas como vectores, a la resolución de salida. Una salida 4K no cuesta más cálculo.
- **La interpolación** entre keyframes reparte el movimiento en todos los frames, con posiciones fraccionarias.
  - Así desaparecen los "escalones": primitive guarda los vértices como enteros de la resolución de trabajo.
  - Medido a 60 fps sobre el video de prueba:
    - Sin interpolar, la imagen queda congelada 151 de 167 frames y salta de golpe en cada keyframe (salto máximo 21.7).
    - Con interpolación cambia en todos los frames, con saltos ~4 veces más chicos (5–6).
- **Formas reemplazadas:** cuando una forma se reemplaza entre dos keyframes, la vieja y la nueva hacen un fundido en vez de un corte.

**Navegadores:**

| Navegador | Soporte |
|---|---|
| Chrome / Edge | Todo |
| Safari | H.264 por WebCodecs desde la versión 16.4 |
| Firefox | WebCodecs desde la versión 130; si no hay H.264, el MP4 sale en VP9 |

### Suavizado entre keyframes

Si cada keyframe se generara desde cero, el resultado titilaría: `primitive` es aleatorio y elige formas distintas cada vez. Por eso:

- **Solo el primer keyframe** usa la búsqueda completa de `primitive`.
- **Cada keyframe siguiente** parte de las formas del anterior, en el mismo orden:
  1. Cada forma se re-optimiza con unos pocos pasos de *hill climbing* sobre el frame nuevo, en paralelo en todos los núcleos, y se queda con el mejor resultado.
  2. Su color se recalcula.
  3. Si una forma ya no mejora la imagen (por ejemplo, en un cambio de escena), se reemplaza en el mismo lugar por una buscada desde cero, y queda marcada como nueva.

El slider *Seguimiento del movimiento* regula cuántos pasos de re-optimización recibe cada forma:

| Valor | Efecto |
|---|---|
| 0 | Las formas quedan fijas; solo cambia su color. Lo más estable y rápido. |
| Más alto | Las formas se desplazan para seguir el movimiento. Más fiel, un poco más lento. |

Además es mucho más rápido que buscar de cero: con 150 formas a 256 px, en 4 núcleos, ~1 s por keyframe contra ~10 s.

Los tests (`temporal_test.go`, `encode_test.go`) lo verifican con datos:

- **Movimiento chico:** el cambio entre frames suavizados queda cerca del cambio real de la imagen, menos de la mitad que generando de cero.
- **Movimiento grande:** adaptar las formas sigue el movimiento mejor que dejarlas fijas.
- **Codificación:** las formas se codifican bien y las rotaciones giran por el camino corto.

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

- **Backend** (Go: `main.go`, `session.go`, `render.go`, `video.go`, `temporal.go`, `encode.go`, `ffmpeg.go`):
  - Usa `primitive` como librería.
  - Cada imagen subida es una *sesión* con una corrida en segundo plano.
  - Las formas se envían al navegador por **Server-Sent Events** a medida que se generan.
  - Los parámetros se cambian con `PUT /api/sessions/{id}/params`.
- **Frontend** (`web/`): HTML, CSS y JS plano, sin build ni dependencias. Va embebido en el binario.
- **Video:**
  - En el servidor, `ffmpeg` decodifica los frames por un pipe y agrega el audio al MP4 exportado.
  - En el navegador (`web/motion.js`, `web/mp4.js`): interpolación, dibujo, WebCodecs, un muxer MP4 propio y la generación de Lottie, sin librerías.
- **Grabación de imágenes** (`web/build.js`): línea de tiempo con curvas de easing, orden de aparición y efectos. Reutiliza el dibujo y el exportador MP4.
- **Selectores visuales** (`web/picker.js`): reemplazan un `<select>` por tarjetas animadas, que solo se dibujan mientras están en pantalla y a 24 fps. El `<select>` queda oculto como fuente del valor.
- **Escena 3D** (`web/scene3d.js`): usa la misma línea de tiempo con cámara en perspectiva y piezas en profundidad.
- **En vivo:**
  - `live.go` maneja la sesión y el bucle de frames.
  - `budget.go` arma los keyframes con presupuesto de tiempo, mapa de cambios y búsqueda rápida.
  - `flow.go` estima el flujo óptico y mueve las formas con él (también se usa en video).
  - `web/live.js` se encarga de la cámara, el dibujo con retraso y la grabación.
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
| `POST /api/videos/{id}/frames` | Crea una sesión de edición sobre el frame `t` (opcional `crop`, como en `compute`) |
| `GET /api/videos/{id}/events` | Stream SSE: `job` (progreso), `animation` (dimensiones y fondo) y `keyframe` (cada keyframe calculado) |
| `POST /api/videos/{id}/compute` | Calcula los keyframes (parámetros + `kfps`, `adapt`, `flow`; opcionalmente una porción con `start` y `length` en segundos, y un recorte `crop: {x, y, w, h}` en fracciones del cuadro) |
| `DELETE /api/videos/{id}/compute` | Cancela el cálculo |
| `GET /api/videos/{id}/keyframes` | Todos los keyframes calculados hasta el momento (JSON) |
| `GET /api/videos/{id}/source` | El video original (audio y reloj del reproductor) |
| `POST /api/videos/{id}/mux` | Recibe un MP4 sin audio y lo devuelve con el audio original |
| `POST /api/live` | Crea una sesión en vivo (parámetros + `adapt`, `budget` en ms, `flow`) |
| `GET /api/live/{id}/events` | Stream SSE: `ready` (mandá un frame), `animation`, `keyframe` y `stats` |
| `POST /api/live/{id}/frame` | Un frame de la cámara (JPEG); header `X-Capture-Time` con el reloj del navegador |
| `PUT /api/live/{id}/params` | Cambia los parámetros (`restart: true` empieza de nuevo) |
| `DELETE /api/live/{id}` | Cierra la sesión (también se cierra sola a los 10 s sin nadie mirando) |
| `POST /api/reels/mux` | Agrega el audio a un reel codificado en el navegador. Campos: `video`; opcionales `audio` (archivo propio) o `source` (id de un video subido, con `sourceStart`), más `audioStart`, `volume` y `fadeOut`. El audio se recorta a la duración del video, se rellena con silencio si es más corto y termina con un fundido. Si el video no es H.264 se convierte. Con `save=1` (y `meta`) lo guarda en la biblioteca y responde con su entrada en vez del archivo |
| `POST /api/reels` | Guarda un reel terminado en la biblioteca (campos `video` y `meta`, un JSON libre: template, duración, ajustes…) |
| `GET /api/reels` | Lista la biblioteca, del más nuevo al más viejo |
| `GET /api/reels/{id}/video` | El MP4 de un reel (`?download=1` para descargarlo) |
| `DELETE /api/reels/{id}` | Borra un reel de la biblioteca |

La biblioteca de reels vive en `REELS_DIR` (o `-reels`), por defecto `./reels`. A diferencia de los archivos subidos, no se borra al reiniciar. En Docker es el volumen `reels` (montado en `/reels`).

### Formato de los keyframes

`GET /api/videos/{id}/keyframes` devuelve `{width, height, background, kfps, duration, keyframes}`.

- `keyframes[k][i]` es la forma `i` del keyframe `k`, como un array plano: `[tipo, r, g, b, a, nueva, ...parámetros]`.
- Las coordenadas están en píxeles de la resolución de trabajo.
- La forma `i` de un keyframe es la misma forma `i` del anterior, movida, salvo que `nueva` sea `1`.

| Tipo | Forma | Parámetros |
|---|---|---|
| 0 | Triángulo / polígono | `x1, y1, x2, y2, …` |
| 1 | Rectángulo (rotado o no) | `cx, cy, ancho, alto, ángulo°` |
| 2 | Elipse / círculo | `cx, cy, rx, ry, ángulo°` |
| 3 | Bézier cuadrática | `x1, y1, cx, cy, x2, y2, grosor` |
