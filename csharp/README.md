# mailcat (C# / .NET 10)

Port a C# del clasificador de correo adaptativo del directorio padre (Go). Es una **app de línea de
comandos**: clasifica solo cuando sabe, pregunta solo cuando una respuesta aporta información, aprende de
cada respuesta y, con el tiempo, pregunta menos. Misma spec ([`../adaptive-email-categorizer-spec.md`](../adaptive-email-categorizer-spec.md)),
mismo algoritmo, mismas decisiones de diseño ([`../docs/PLAN.md`](../docs/PLAN.md)).

## Qué incluye y qué no

| Incluido | Omitido a propósito |
|---|---|
| Parser `.eml` (MimeKit), normalización, features por capas, reglas YAML (`all/any/not`, 12 operadores) | Ollama / LLM (enriquecimiento, generación de preguntas, "maestro", `pretrain`, `audit`) |
| Agregación jerárquica noisy-OR con "unknown" como hipótesis, 4 estados (sé / predicción / pregunto / no sé) | Gmail (etiquetas `X-Gmail-Labels`, reglas `gmail.*`, sync de etiquetas) |
| Preguntas por *Information Gain*, utilidad, tasa de respuesta, sesiones persistentes | n8n (`n8n/`, `docs/GMAIL-N8N.md`) |
| Aprendizaje: reglas positivas/negativas, generalización remitente → lista → dominio → dominio+intención → intención | Bot de Telegram |
| Similitud TF-IDF k-NN, explicaciones, métricas, evaluación offline con curva de aprendizaje | API REST (`serve`/`health`) y Docker: esto es un CLI |
| SQLite embebido, privacidad (`purge`, `store_body: false`), `backup` | Importación `mbox`, `export` de conocimiento (ligadas a Gmail/pretrain) |

Si algún día los quieres, las costuras siguen ahí: `ISignal` (nuevas fuentes de evidencia),
`IFeatureExtractor`, `IIndex` (embeddings).

## Uso

```bash
cd csharp
dotnet build -c Release                       # → src/MailCat.Cli/bin/Release/net10.0/mailcat(.dll)
alias mailcat='dotnet src/MailCat.Cli/bin/Release/net10.0/mailcat.dll'   # o publica un ejecutable (abajo)

mailcat sample-data -out sample               # buzón sintético reproducible (12 categorías)
mailcat eval -data sample                     # ¿aprende a preguntar menos? (curva de aprendizaje)

mailcat init                                  # escribe mailcat.yaml editable
mailcat classify -i sample/finance.purchase   # modo interactivo tipo Akinator
mailcat classify sample/                      # sin preguntas: SEGURO / PREDICCIÓN / PREGUNTA / DESCONOCIDO
mailcat list -n 10                            # ids de lo procesado
mailcat feedback <id> -correct                # o: -category finance.bank
mailcat move <id> finance.bank                # "Move to…": peso máximo
mailcat explain <id>                          # ¿por qué quedó ahí?
mailcat rules                                 # lo que ha aprendido
mailcat metrics [-json]                       # calidad y esfuerzo del usuario
```

Opciones globales (antes del comando): `--config archivo.yaml` (o `$MAILCAT_CONFIG`; por defecto `./mailcat.yaml`
y, si no existe, la configuración de ejemplo embebida) y `--db archivo.db`.
Comandos: `init classify list feedback move explain categories questions rules metrics eval sample-data purge backup`
(`mailcat help` los describe).

Ejecutable único autocontenido (incluye el runtime y SQLite):

```bash
dotnet publish src/MailCat.Cli -c Release -r osx-arm64 --self-contained -p:PublishSingleFile=true -p:IncludeNativeLibrariesForSelfExtract=true -o dist
```

## Configuración

`mailcat.yaml` tiene las mismas secciones que la versión Go **menos** `api`, `llm`, `telegram` y `sync`
(las claves desconocidas se rechazan al iniciar, así que un YAML con esas secciones debe limpiarse):
`database`, `thresholds`, `max_questions_per_email`, `similarity`, `learning`, `confirmation`,
`question_selection`, `privacy`, `categories`, `questions`, `rules`. Se valida al arrancar (ciclos, padres
inexistentes, regex inválidas, categorías desconocidas…). Ver [`src/MailCat.Core/Resources/default.yaml`](src/MailCat.Core/Resources/default.yaml), comentado.

## Estructura

```
src/MailCat.Core   biblioteca (todo el motor)
  Emails Text Features Categories Rules Classification Questions Similarity Learning
  Explain Storage Config Engine Metrics Eval Samples
src/MailCat.Cli    el comando `mailcat`
tests/MailCat.Tests  xUnit: unitarias + integración + evaluación end-to-end + CLI
tools/parity.sh    compara esta versión con la original en Go sobre el mismo corpus
```

Dependencias: `Microsoft.Data.Sqlite`, `MimeKit`, `YamlDotNet`. La lista de sufijos públicos
(`public_suffix_list.dat`, para el dominio registrable) va embebida, así que no hay nada que descargar en
tiempo de ejecución.

## Pruebas y paridad con la versión Go

```bash
dotnet test                 # 146 pruebas
tools/parity.sh 42 40       # requiere Go; compara `classify` y `eval` byte a byte
```

`parity.sh` genera un corpus, lo pasa por ambos binarios y compara decisión, categoría, puntuación y método de
cada correo, y la salida completa de `eval` (precisión, curva de aprendizaje, errores). Sobre varias semillas
sale idéntico. La única diferencia observada viene del parser de la versión Go (enmime), que en contadas ocasiones
decodifica mal UTF-8 válido («revisiÃ³n»); MimeKit lo hace bien, y el script lo avisa.

## Notas de portado

* **Plegado de acentos** con tabla propia (`Text/Diacritics.cs`, generada desde la descomposición NFD de Unicode):
  `string.Normalize` depende de ICU / del modo de globalización y falla en modo invariante.
* **Sin concurrencia**: un CLI es secuencial, así que no hay `context`, locks ni async; el motor es síncrono.
* **Un solo esquema SQLite** (v1) sin las tablas/columnas de LLM, Telegram y sincronización con Gmail. La base
  de datos de Go **no** es compatible.
* `sample-data` produce un corpus distinto al de Go para la misma semilla (otro generador de números
  aleatorios); es igual de reproducible por sí mismo.
