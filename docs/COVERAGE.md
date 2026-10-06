# Trazabilidad spec → implementación

| § | Requisito | Dónde | Estado |
|---|---|---|---|
| 1, 3 | Pipeline y fuentes independientes del proveedor | `engine`, `source` | ✅ (`.eml`; IMAP/Gmail/Graph pendientes) |
| 2.1 | Clasificar antes que preguntar | `engine.advance` | ✅ |
| 2.2, 12, 13 | Preguntas por Information Gain + penalizaciones | `questions` | ✅ |
| 2.3 | Componentes separados y testeables | un paquete por responsabilidad | ✅ |
| 2.4, 16 | Explicabilidad; confirmación muestreada | `explain`, `engine.shouldConfirm` | ✅ |
| 4–6 | Modelo conceptual y features jerárquicas con `source` | `features` | ✅ |
| 5 | Esquema SQLite | `storage/migrations.go` | ✅ (+ columnas/tablas aditivas, ver PLAN §5) |
| 7 | Extracción en capas (headers, remitente, contenido, intención) | `features/*.go` | ✅ (entidades/direcciones: solo vía LLM) |
| 8 | Motor de reglas AND/OR/NOT, 10+ operadores, pesos | `rules` | ✅ |
| 9, 10 | Clasificador y jerarquía top-down | `classifier` | ✅ |
| 11 | Candidatos Akinator | `classifier.Candidate`, `questions.Selector` | ✅ |
| 14 | Preguntas predefinidas y generadas (validadas) | `config`, `llm.QuestionGenerator` | ✅ |
| 15, 41 | Umbrales; cuatro estados | `classifier.Thresholds`, `engine.Decision*` | ✅ |
| 17–21 | Aprendizaje incremental, generalización, correcciones, negativo | `learning` | ✅ |
| 22 | Similitud TF-IDF | `similarity` | ✅ (embeddings: interfaz) |
| 23 | LLM opcional | `llm` | ✅ apagado por defecto |
| 24 | Privacidad local-first, separación raw/features/conocimiento | `storage.Purge*`, `llm.Redact`, config | ✅ |
| 25, 26 | Arquitectura Go y pipeline | `internal/*`, `engine.Process` | ✅ |
| 27 | API REST | `api` | ✅ (+ skip, explain, métricas, purga) |
| 28, 29 | Categorías y preguntas en YAML | `config` | ✅ |
| 30, 31 | Sesión persistente; sin ciclos; `max_questions` | `engine/state.go`, `advance` | ✅ |
| 32, 33 | `unknown`; mover manualmente con peso 1.0 | `categories.UnknownID`, `engine.Move` | ✅ |
| 34 | Métricas | `metrics` | ✅ |
| 35 | Evaluación offline con split | `eval` | ✅ |
| 36 | Fases 1–4 | — | ✅ |
| 36 | Fase 5 (IMAP/Gmail/Graph, workers) | — | ⏳ métricas, backups y privacidad hechos |
| 37 | Requisitos funcionales | ver arriba | ✅ todos |
| 38 | p95 < 100 ms; determinismo; testabilidad | `engine/perf_test.go`, tests de determinismo | ✅ (≈ 2.5 ms) |
| 40 | Éxito = menos preguntas con acierto | `eval` (curva) | ✅ medido |
