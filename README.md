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
   | **¿En qué nivel?** | Foto o video | Se completa de a niveles (10, 25, 50, 100… formas, hasta cinco niveles y luego todas), cada uno sostenido con su etiqueta "Nivel {nivel} · {formas} formas", y al final aparece el original. Pregunta y cierre invitan a comentar en qué nivel la adivinaste. En video, cada nivel muestra un tramo con esa cantidad de formas |
   | **Comparativa** | Foto o video | Arriba el original, abajo las formas armándose, con una línea entre los dos. Cada mitad es un recorte 9:8, así que las formas se calculan sobre ese recorte |
   | **Cortina** | Foto o video | Una línea va y viene: a un lado el original, al otro las formas. Es periódica, así que también empalma |
   | **Contador** | Foto | Las formas se suman con un contador grande ("{n} {tipo}") y al final aparece la foto |
   | **Mosaico de niveles** | Foto o video | Grilla 2×2 con cada vez más formas (por ejemplo 10, 50, 100 y 500), los cuadros aparecen uno tras otro y al final todo pasa al original. Sale de un solo cálculo: cada cuadro usa las primeras N formas |
   | **¿Cuál preferís?** | Foto | La misma foto con cuatro tipos de forma (triángulos, círculos, rectángulos y polígonos) armándose a la vez, numerados para que comenten. Cada tipo es un cálculo propio: mientras este template está elegido corren tres sesiones extra (la del tipo elegido en los controles se reutiliza), y el estado muestra cuántas están listas |

   *Automático* elige Revelado para fotos y Video en formas para videos.

   Cada template tiene su **configuración base** (duración, cantidad y tipo de formas, animación y frases), que se carga al elegirlo. Lo que cambiaste a mano se respeta al pasar de un template a otro, y *Volver a la configuración base del template* lo restaura todo (los textos escritos se mantienen). Revelado y Video en formas arrancan en 15 s; el Loop, en 10 s.
3. **Duración.** Atajos de 10, 15, 30 y 60 s, o libre (3 a 90 s). En los videos fija el largo de la porción (la mitad en el loop, que va y vuelve). Si elegís la porción a mano, pasa a ser libre.
4. **Encuadre 9:16.**
   - *Llenar* recorta: zoom, posición, y también arrastrar la vista previa o usar la rueda del mouse.
   - *Ajustar* muestra la fuente completa sobre una copia desenfocada.
   - Las zonas rojas marcan lo que tapan TikTok, Reels y Shorts (barra superior, texto de la cuenta, botones). Conviene dejar lo importante fuera de ellas.
5. **Formas, en vivo como en el editor.** Cantidad y tipo; las formas aparecen en la vista previa mientras se buscan y cada cambio se ve al instante, igual que en el editor principal (con *Pausar / Continuar*).
   - Fotos: la sesión trabaja sobre la foto ya recortada a 9:16, así las formas cubren justo el cuadro. Cuando están todas, el reel las usa solo; si cambiás algo, se buscan de nuevo y el reel se actualiza al terminar.
   - Videos: se ajusta sobre **frames de prueba** del tramo (también recortados), como en el editor: con el deslizador elegís un momento y con **Agregar este frame** lo guardás en la lista. Cada frame tiene su propia sesión; al pasar a otro, el anterior se pausa y, al volver, sigue con las formas donde estaban. Los frames de prueba se guardan con el reel.
6. **Algoritmo.** Los mismos controles que el editor: opacidad (fija o automática por forma), repetición, resolución de trabajo y fondo (color promedio o elegido).
7. **Animación.**
   - Fotos: las opciones de la grabación del editor, con sus tarjetas animadas: orden de aparición, velocidad (curva de easing, en Revelado), efecto y duración del efecto. Se aplican al instante, sin recalcular.
   - Videos: keyframes por segundo, seguimiento del movimiento, flujo óptico e interpolación. *Calcular el video* procesa el tramo con el recorte elegido (pausa la sesión de prueba para liberar la CPU); si después cambiás algo que afecta el cálculo, avisa que hay que calcular de nuevo. La interpolación se aplica al instante.
8. **Frases.** Las del template vienen cargadas (en los actuales, título y cierre) y se pueden agregar más. Cada una tiene texto, posición (arriba, centro, abajo), estilo (título, subtítulo, pregunta o etiqueta con fondo) y desde/hasta en segundos, que se reajustan solos si cambia la duración. Aparecen con un fundido y un leve movimiento, con sombra, y quedan fuera de las zonas que tapan las apps; si no entran, se achican. Pueden usar `{formas}` y `{tipo}` ("La Mona Lisa con {formas} {tipo}" → "La Mona Lisa con 150 triángulos"); algunos templates suman las suyas: `{nivel}` y `{niveles}` (¿En qué nivel?, donde `{formas}` es la cantidad del nivel) y `{n}` (Contador: las formas que van apareciendo). Posiciones: arriba, mitad (debajo de la línea de la comparativa), centro, abajo y al pie; estilos: título, subtítulo, pregunta, etiqueta y contador. La **marca** se ve durante todo el reel.
9. **Audio.** Sin sonido, el sonido del video (el tramo elegido) o un archivo propio, desde el segundo que elijas. Con volumen y fundido de salida. Se escucha al reproducir la vista previa.
10. **Exportar.** El navegador dibuja cada cuadro a 1080×1920 y 30 fps y lo codifica en H.264 (WebCodecs); el servidor le agrega el audio en AAC (recortado a la duración del reel, rellenado con silencio si es más corto y con el fundido) y lo guarda en la biblioteca. El MP4 se descarga solo y está listo para subir a TikTok, Reels o Shorts. Si el navegador solo puede codificar VP9, el servidor lo convierte a H.264.

**Biblioteca.** Al pie del panel están los reels exportados, del más nuevo al más viejo, para verlos, descargarlos, **editarlos** o borrarlos. **Editar** vuelve a abrir el reel con todos sus ajustes (el reel guarda una copia de su foto o video de origen en `sources/`, compartida entre los reels hechos de la misma fuente) y al guardar lo reemplaza en la biblioteca. Los reels exportados antes de esta función no guardaron su origen, así que no se pueden editar. Cada uno guarda un registro con lo que lo generó: template, duración, fuente, tramo, encuadre, formas, textos, audio y datos de la exportación. Sirve para comparar después qué combinación funciona mejor en las redes.

Exportar un reel de 10 s de una foto con 100 formas tarda unos 9 s; uno de 2,5 s de video, unos 4 s (más el cálculo de las formas).

La vista previa reproduce el reel o el original. Si cambiás el encuadre, la porción o los ajustes, avisa que hay que generar de nuevo. Los templates viven en `web/templates.js`, cada uno como una receta: su configuración base, el recorte sobre el que se calculan las formas, el contenido como función del tiempo (así la exportación dibuja exactamente los mismos cuadros que la vista previa) y sus frases, con tiempos relativos a la duración.

Próximas etapas: una cola de publicación con una regla configurable (por ejemplo, un reel por día a cierta hora) y la publicación automática.



## Generar reels automáticamente

`/generar.html` (enlace "generar" arriba de los editores). Cada foto o video que dejes en la **carpeta de entrada** se convierte en varios reels, **uno por template elegido** (los que correspondan: algunos son solo para fotos o solo para videos), con la configuración base de cada uno. Los reels van a la **carpeta de generados**, cada uno con su JSON (título y descripción sacados de sus frases, más el registro de cómo se hizo), aparte de la carpeta de publicación. Cuando están todas las versiones, la fuente pasa a `procesados/` dentro de la entrada.

- **Subir desde la página:** arrastrá fotos o videos a la página (o usá **Elegir archivos…**, se pueden varios a la vez). Se guardan en la carpeta de entrada. Si el nombre ya se usó (en la entrada o en `procesados/`), se guarda como `nombre (2).png`. Dejarlos directamente en la carpeta sigue funcionando igual.
- **Elegir qué templates probar:** cada foto o video nuevo (subido o dejado en la carpeta) aparece en **Elegí qué templates probar**, con una vista previa y solo los templates que le corresponden (cada uno con su descripción). Marcás los que quieras, ajustás los idiomas si hace falta (por defecto los de la configuración) y **Generar** hace un reel por template e idioma; la página dice cuántas versiones son. **Descartar** no genera nada y pasa el archivo a `descartados/` dentro de la entrada (no se borra). Si destildás **Preguntar qué templates probar**, vuelve el modo totalmente automático: cada fuente nueva genera los **templates por defecto** apenas aparece.
- En la página se puede pausar el generador (si está pausado, lo que subas espera y la página ofrece **Activar**). **En proceso** muestra una línea por fuente: cuántas versiones están listas, cuál se está haciendo y las que fallaron (con **Reintentar**). Los reels generados se revisan en **Revisar** (ver abajo).
- Las fuentes se toman cuando terminan de copiarse (el tamaño deja de cambiar) y se procesan de a una. Si el servidor se reinicia, lo que estaba a medio hacer vuelve a la cola.
- **Cómo funciona:** para que los reels salgan idénticos a los del editor, el servidor abre el mismo editor de reels en un **Chromium sin pantalla** (`reels.html?auto=<trabajo>`). Esa página carga la fuente, elige el template con su base, espera las formas (o calcula el video), exporta y devuelve el MP4. Si el navegador no puede reproducir el video (Chromium sin H.264), el servidor le da una copia en WebM. La imagen de Docker trae Chromium y las fuentes (con emoji). Sin Docker hace falta tener Chromium o Chrome instalado (`CHROME_BIN` para indicar cuál).
- Cada reel tarda aproximadamente 1 minuto (30 s a 1 min con 150 formas; más con Mosaico, de 500, o con ¿Cuál preferís?, que calcula cuatro tipos de forma).

### Títulos, descripciones, tags e idiomas

Cada reel generado sale **en cada idioma configurado** (por defecto español e inglés): un video por idioma, con sus frases en pantalla traducidas, y su título, descripción (con `#Shorts` y hashtags) y tags en el JSON (`"lang"`, `"title"`, `"hook"`, `"description"`, `"tags"`). Los archivos se llaman `<fuente> · <template> · <idioma>.mp4`.

- **El título base sale del nombre del archivo**, limpio de lo que no dice nada (`mona_lisa-final (2).png` → "Mona lisa"; capturas de pantalla o `IMG_20240101…` no dan título). Con ese título, el template y un cuadro de la fuente, un modelo escribe todo junto, una vez por fuente y template, para todos los idiomas: título del video, frase de entrada en pantalla, cierre, descripción, tags y las frases propias del template (como "¿En qué nivel la adivinaste?").
- **Quién escribe** (en la página Generar o con variables de entorno):
  - **Claude** (por defecto si hay `ANTHROPIC_API_KEY` en el `.env`): mira también la imagen. Modelo `claude-opus-5-5` salvo que pongas otro en `CLAUDE_MODEL` (o en el campo Modelo de la página); un nombre que no empieza con `claude-` se ignora para Claude, así el `LLM_MODEL` del modelo local no le llega.
  - **Un modelo local** con API compatible con OpenAI (Ollama, LM Studio…): `LLM_BASE_URL` (por ejemplo `http://host.docker.internal:11434/v1` para Ollama) y `LLM_MODEL` con el modelo que tengas descargado. Solo recibe texto.
  - **Reglas**, sin modelo: textos armados a partir del título (en español e inglés; otros idiomas usan el inglés).
  Si el modelo falla (sin conexión, key inválida…), ese reel usa las reglas y el registro lo dice, así nunca se queda sin textos.
- Los idiomas son códigos de dos letras (`es, en, pt…`); cada uno suma una versión de cada reel, o sea más tiempo de generación.

**Carpetas con Docker** (en `.env`, ver `.env.example`): `INPUT_HOST_DIR` (entrada, se monta en `/entrada`) y `GENERATED_HOST_DIR` (generados, en `/generados`). Sin Docker: `-input` / `INPUT_DIR` (por defecto `./entrada`) y `-generated` / `GENERATED_DIR` (por defecto `./generados`).


## Revisar: elegir, refinar y mandar a la cola

`/revisar.html`. Lo generado no va directo a publicar: pasa por vos.

1. **Elegir.** Las versiones de cada fuente aparecen agrupadas por idioma. **▶** (o un clic en el video) las abre en **pantalla completa**, con sonido, para recorrerlas con el teclado: ← → otra versión, **Enter** elegir, **X** descartar, Espacio pausa, **F** pantalla completa del navegador, Esc cerrar. Elegir una deja **una sola por idioma**: las demás de esa fuente e idioma pasan a `descartados/` dentro de Generados (se pueden **restaurar**). En los idiomas que todavía no tienen elegida se elige **el mismo template**, porque un reel es una pieza con una versión por idioma (igual se puede elegir otro para un idioma).
2. **Refinar, una vez para todos los idiomas.** La elegida se abre en el editor de reels (`reels.html?refine=<versión>`) con todo lo que la generó: template, encuadre, formas, algoritmo, animación, duración o tramo, frases, marca y audio. Arriba aparece una pestaña por idioma del mismo reel: los ajustes son compartidos y las frases se escriben en cada idioma (cambiando de pestaña se ven y se editan). **Guardar versión refinada** vuelve a exportar todos los idiomas con esos cambios y, si está marcado "mandar todos los idiomas a la cola" (por defecto sí), los pasa a la carpeta de publicación. Título, descripción y tags de cada versión se editan en Revisar (**Textos**).
3. **A la cola.** Al guardar el refinado, o a mano desde Revisar. Solo las refinadas pueden ir a la cola; si se desmarca "Pedir el refinado antes de la cola", alcanza con elegirlas.

La etapa de cada versión (generada, elegida, refinada) se guarda en su JSON (`"stage"`).

## Publicar

`/publicar.html` (enlace "publicar" arriba de los editores) maneja la cola de publicación.

**La carpeta.** Los videos que dejes en la carpeta de publicación (MP4, MOV, M4V o WebM) forman la cola: del más viejo al más nuevo, salvo que los reordenes. Cada uno puede tener al lado un `.json` con el mismo nombre:

```json
{ "title": "La Mona Lisa con 150 triángulos", "description": "¿Cuál hago mañana? #Shorts", "tags": ["arte", "lowpoly"] }
```

Sin JSON, el título sale del nombre del archivo y la descripción es `#Shorts`. Desde la página se pueden editar con **Textos** (se guardan en ese JSON). Al publicarse, el video y su JSON pasan a `publicados/`, con la fecha y el resultado. La página avisa si un video no sirve como Short (horizontal o de más de 60 s). Un clic en un video (de la cola o de los publicados) abre la **vista previa de lo que se va a publicar**: el reel grande, con sonido, y al costado título, descripción, tags, cuándo sale y los avisos; por defecto se ve **como en Shorts**, con el nombre del canal (la marca del reel), el título y la primera línea de la descripción encima del video (se puede quitar). ← → recorre la cola, Esc cierra.

El editor de reels manda reels de la biblioteca a la cola con **A la cola**: el título sale de la frase de título (o de la pregunta) con `{formas}` y `{tipo}` completados, y la descripción del cierre más `#Shorts`.

**Editar reel.** Cada reel de la cola que salió del generador (o de Revisar), o de la biblioteca del editor de reels, se puede volver a abrir en el editor de reels con **Editar reel**, con todos sus ajustes y con sus otros idiomas que estén en la cola (mismo origen y template). Igual que al refinar, los cambios valen para todos los idiomas y las frases se editan en cada uno. **Guardar cambios en la cola** vuelve a exportar todos los idiomas y los reemplaza en su mismo lugar de la cola, sin tocar título, descripción ni tags (esos se cambian con **Textos**).

**Duplicar en otro idioma.** Cada reel de la cola que salió del generador tiene **Duplicar en otro idioma**: elegís el idioma (es, en, pt, fr, it, de u otro código) y un **LLM local** traduce todos los textos —título, descripción, tags y cada frase que aparece en pantalla—, manteniendo `{formas}`, `{tipo}`, los emojis y `#Shorts`. Después se vuelve a renderizar el reel con los mismos ajustes (Chromium en el contenedor) y la copia entra a la cola justo después del original, ya refinada. El avance se ve en "Duplicando en otros idiomas", con **Reintentar** si falla. Usa siempre el modelo local (el de la página Generar, o `LLM_BASE_URL` / `LLM_MODEL`), nunca Claude. No se puede duplicar a un idioma que ya está en la cola, ni un video que no tenga sus ajustes (por ejemplo, uno dejado a mano en la carpeta).

**La regla.** Horarios (uno o varios por día), días de la semana y zona horaria (por defecto Buenos Aires); por ejemplo, "todos los días a las 19:00". En cada horario se publica el siguiente video de la cola. Los videos se pueden subir o bajar, **saltear** (quedan en la carpeta sin publicarse), **publicar ahora** y, ya publicados, **volver a la cola**. Si el servidor estuvo apagado durante varios horarios, al volver publica uno solo (no una ráfaga). La regla, el orden y el registro se guardan en la propia carpeta (`.cola.json`).

**Por ahora la publicación es simulada:** marca el video y lo mueve a `publicados/`, sin subirlo a ninguna plataforma. Así se prueban la cola y la regla; la subida a YouTube (y después TikTok e Instagram) se conecta en el mismo lugar.

**Con Docker**, la carpeta de tu computadora se monta en `/publicar`. Copiá `.env.example` a `.env` y poné ahí la ruta:

```sh
cp .env.example .env   # PUBLISH_HOST_DIR=/Users/.../Reels
docker compose up --build
```

Sin Docker: `-publish <carpeta>` o `PUBLISH_DIR` (por defecto `./publicar`).

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
| `POST /api/reels/mux` | Agrega el audio a un reel codificado en el navegador. Campos: `video`; opcionales `audio` (archivo propio) o `source` (id de un video subido, con `sourceStart`), más `audioStart`, `volume` y `fadeOut`. El audio se recorta a la duración del video, se rellena con silencio si es más corto y termina con un fundido. Si el video no es H.264 se convierte. Con `save=1` (y `meta`) lo guarda en la biblioteca y responde con su entrada en vez del archivo; con `src` (la foto) o `srcVideo` + `srcName` (id de un video subido) guarda también su origen, para editarlo después. Con `lib=<id>` reemplaza ese reel de la biblioteca |
| `POST /api/reels` | Guarda un reel terminado en la biblioteca (campos `video` y `meta`, un JSON libre: template, duración, ajustes…) |
| `GET /api/reels` | Lista la biblioteca, del más nuevo al más viejo |
| `GET /api/reels/{id}/video` | El MP4 de un reel (`?download=1` para descargarlo) |
| `DELETE /api/reels/{id}` | Borra un reel de la biblioteca (y su origen, si ningún otro reel lo usa) |
| `GET /api/reels/{id}/spec` · `GET …/source` | Ajustes y origen, para reabrirlo en el editor (`reels.html?lib=`) |
| `POST /api/reels/{id}/queue` | Copia un reel de la biblioteca a la carpeta de publicación (con título y descripción) |
| `GET /api/publish` | Regla, cola (con el horario de cada video), publicados y registro |
| `PUT /api/publish/settings` | La regla: `enabled`, `times` (HH:MM), `days` (0 = domingo), `tz` |
| `PUT /api/publish/order` | Orden preferido de la cola (`names`) |
| `PUT /api/publish/queue/{name}/meta` | Título, descripción y etiquetas (en el JSON del video) |
| `POST /api/publish/queue/{name}/skip` | Saltear o incluir (`skip`) |
| `POST /api/publish/queue/{name}/now` | Publicar ya |
| `POST /api/publish/published/{name}/requeue` | Devolver un publicado a la cola (primero) |
| `POST /api/publish/queue/{name}/duplicate` | Duplicar en otro idioma (`lang`): traduce con el LLM local y renderiza la copia |
| `GET /api/publish/copies` | Copias en curso (estado, progreso, error) |
| `GET /api/publish/queue/{name}/spec` | Ajustes, fuente y otros idiomas en la cola, para reabrirlo en el editor (`reels.html?edit=`) |
| `GET /api/publish/queue/{name}/source` | La foto o video de origen |
| `GET /api/auto` | Generador: ajustes, trabajos, reels generados y registro |
| `POST /api/auto/inputs` | Sube fotos o videos (campo `file`, uno o varios) a la carpeta de entrada; responde los nombres con que quedaron |
| `GET /api/auto/inputs/{name}` | Una fuente que espera la elección de templates (para verla) |
| `POST /api/auto/inputs/{name}/start` | Genera los templates elegidos (`templates`) en los idiomas elegidos (`languages`, vacío = los de la configuración) |
| `POST /api/auto/inputs/{name}/dismiss` | Descarta una fuente que espera: pasa a `descartados/` de la entrada |
| `PUT /api/auto/settings` | `enabled`, `skipChoice` (no preguntar: los templates por defecto apenas aparece una fuente), `templates` (vacío = todos), `languages` y `llm` (`provider`: `anthropic`, `local`, `none` o vacío = automático; `model`; `baseURL`) |
| `GET /api/auto/jobs/{id}` · `/source` | Lo que la página automática necesita: template y fuente (`?compat` = video en WebM) |
| `POST /api/auto/jobs/{id}/progress` · `/fail` · `/retry` | Avance, error y reintento de un trabajo |
| `GET /api/auto/outputs/{name}/video` · `POST …/queue` · `DELETE` | Ver, mandar a la cola de publicación o borrar un reel generado |
| `POST /api/reels/mux` con `job` | El reel terminado de un trabajo (va a la carpeta de generados) |
| `POST /api/reels/mux` con `queue` | Un reel de la cola editado de nuevo: lo reemplaza en su lugar (con sus nuevos ajustes) |
| `GET /api/review` | Versiones generadas por fuente e idioma, con su etapa, y las descartadas |
| `POST /api/review/{name}/choose` · `/discard` · `/restore` | Elegir (descarta las otras del mismo idioma), descartar, restaurar |
| `PUT /api/review/{name}/meta` | Título, descripción y tags de una versión |
| `GET /api/review/{name}/spec` · `/source` | Lo que el editor necesita para refinarla: su registro y su fuente |
| `POST /api/reels/mux` con `refine` | La versión refinada (la reemplaza) |
| `POST /api/review/{name}/queue` | A la carpeta de publicación (refinada, o elegida si no se pide refinado) |

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
