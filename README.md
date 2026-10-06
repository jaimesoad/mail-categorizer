# Adaptive Email Categorizer (`mailcat`)

Clasificador de correo inspirado en Akinator: **clasifica solo cuando sabe, pregunta solo cuando una
respuesta aporta información, aprende de cada respuesta y, con el tiempo, pregunta menos.**
Implementa [`adaptive-email-categorizer-spec.md`](adaptive-email-categorizer-spec.md); las decisiones y mejoras
sobre la spec están en [`docs/PLAN.md`](docs/PLAN.md) y la trazabilidad por sección en
[`docs/COVERAGE.md`](docs/COVERAGE.md).

- Go 1.26+, SQLite embebido (sin CGO), sin servicios externos. **Local-first**: el LLM es opcional y está apagado.
- Cuatro estados explícitos (§41): **sé** · **buena predicción** · **necesito preguntar** · **no sé** (`unknown`).
- Explicable: cada clasificación responde "¿por qué quedó aquí?".

## Inicio rápido

```bash
go build -o mailcat ./cmd/mailcat

./mailcat sample-data -out sample            # buzón sintético reproducible (12 categorías)
./mailcat eval -data sample                  # ¿aprende a preguntar menos? (curva de aprendizaje)

./mailcat init                               # escribe mailcat.yaml editable
./mailcat classify -i sample/finance.purchase   # modo interactivo tipo Akinator
./mailcat classify sample/                   # sin preguntas: SEGURO / PREDICCIÓN / PREGUNTA / DESCONOCIDO
./mailcat rules                              # lo que ha aprendido
./mailcat metrics                            # calidad y esfuerzo del usuario
./mailcat serve                              # API REST en 127.0.0.1:8080
```

Sin `mailcat.yaml` se usa la configuración de ejemplo embebida (categorías, preguntas y reglas en español).

## Cómo decide

```
.eml / API ─► normalizar ─► features (headers, remitente, contenido, intención, adjuntos, historial, hilo)
                                 │
              reglas YAML ──────┤ evidencia ponderada (noisy-OR, negativa = descarta)
              reglas aprendidas ┤
              similitud TF-IDF ─┘
                                 ▼
              agregación jerárquica (categorías compiten contra "unknown")
                                 │
     ≥ umbral y margen ──► clasifica          zona media/baja ──► pregunta con mayor Information Gain
                                                      │ respuesta ⇒ actualización bayesiana + feature del usuario
                                                      ▼
                                      aprende reglas (positivas y negativas)
```

- **Preguntas**: utilidad = `IG · confianza · fiabilidad − repetición − complejidad`; nunca repite, no pregunta
  lo que ya sabe, descuenta las que el usuario suele omitir, máx. `max_questions_per_email`.
- **Aprendizaje**: solo de señales del usuario (respuestas que resuelven, confirmaciones, correcciones, "mover a…").
  Nunca de sus propias clasificaciones automáticas. Se generaliza remitente → lista → dominio → dominio+intención
  → intención; los proveedores gratuitos (gmail.com…) no generalizan por dominio. Una corrección crea
  **reglas negativas** y solo atenúa, no borra, lo aprendido.
- **Determinista**: mismo correo + misma base + misma config ⇒ mismo resultado.

## Configuración (`mailcat.yaml`)

Categorías jerárquicas, preguntas (con `affinity` para el arranque en frío), reglas con `all/any/not` y operadores
`equals not_equals contains starts_with ends_with regex exists gt gte lt lte in`, umbrales, privacidad, API y LLM.
Se valida al iniciar (ciclos, padres inexistentes, regex inválidas, categorías desconocidas…). Ver
[`internal/config/default.yaml`](internal/config/default.yaml), comentado.

```yaml
rules:
  - id: my-bank
    category: finance.bank
    weight: 0.7
    reason: "El remitente coincide con un dominio bancario conocido."
    when:
      all:
        - {feature: sender.domain, op: in, value: [mybank.example]}
        - {feature: content.contains_transaction, op: equals, value: "true"}
```

## API REST

| Método | Ruta | Descripción |
|---|---|---|
| POST | `/v1/emails/classify` | clasifica (`id, sender, subject, body…` o `raw_eml`); idempotente |
| POST | `/v1/emails/{id}/question` | pregunta pendiente |
| POST | `/v1/emails/{id}/answer` | `{question_id, answer}` → nueva clasificación, `learned`, siguiente pregunta |
| POST | `/v1/emails/{id}/skip` | el usuario omite la pregunta |
| POST | `/v1/emails/{id}/feedback` | `{correct, category?}` — corrección con peso máximo |
| GET | `/v1/emails/{id}` · `/explain` | estado actual · explicación |
| DELETE | `/v1/emails/{id}[?content_only=true]` | purga contenido (conserva lo aprendido) o borra todo |
| GET | `/v1/categories` · `/v1/questions` · `/v1/metrics` | consulta |
| POST | `/v1/questions/{id}/approve\|reject` | aprobar preguntas generadas por LLM |

```bash
curl -s localhost:8080/v1/emails/classify -d '{"id":"email-123","sender":"billing@example.com",
  "subject":"Your monthly statement","body":"Your statement is ready..."}'
```

Escucha en loopback por defecto. Con `MAILCAT_TOKEN` exige `Authorization: Bearer …`; `serve` se niega a
escuchar fuera de loopback sin token.

## Gmail, n8n, Telegram y LLM local

`mailcat` puede (1) **preentrenarse** con tu buzón existente usando un LLM local (Ollama) como "maestro" de peso
reducido y verificable (`pretrain`, `audit`, `export`), (2) **preguntarte por Telegram** con botones cuando duda,
y (3) **etiquetar Gmail** a través de workflows de n8n listos para importar en [`n8n/`](n8n).
Guía completa y paso a paso: [`docs/GMAIL-N8N.md`](docs/GMAIL-N8N.md). Docker: `Dockerfile` y `docker-compose.yml`.

## Privacidad (§24)

Todo es local. Separación raw / features / conocimiento: `mailcat purge -all` borra asunto, cuerpo y features
derivadas del contenido, **conservando reglas aprendidas** y clasificaciones. `privacy.store_body: false` no
persiste cuerpos. El LLM (opcional) solo recibe features abstractas y fragmentos *redactados* (sin correos,
URLs ni números largos); un endpoint no-loopback exige `llm.allow_external: true`. Las preguntas que genera
quedan **sin validar** hasta `mailcat questions approve <id>`.

## Evaluación y métricas

`mailcat eval` divide el corpus (60/20/20 determinista), aprende online en *train* con un usuario simulado y
mide en *validation/test* **sin aprendizaje**: acierto, tasa de auto-clasificación, preguntas por correo,
desconocidos y, sobre todo, **acierto cuando el sistema dice "estoy seguro"**. Imprime la curva de §40 y la lista
de errores. `mailcat metrics` calcula §34 desde la base (incluida la métrica *correctas / esfuerzo del usuario*).

## Desarrollo

```bash
go test ./...            # unitarias + integración + evaluación end-to-end
go test -race ./...
```

Estructura (`internal/`): `email` · `features` · `categories` · `rules` · `classifier` · `questions` ·
`similarity` · `learning` · `explain` · `storage` · `config` · `llm` · `engine` · `metrics` · `eval` · `samples` ·
`source` · `api`. Los extractores, señales, proveedores de correo y LLM se enchufan por interfaz sin tocar el núcleo.

## Fuera de alcance por ahora

IMAP / Gmail / Microsoft Graph y workers en background (la interfaz `source.Source` está lista), embeddings
reales (hay interfaz `similarity.Index`), decaimiento temporal de reglas e interfaz gráfica.
