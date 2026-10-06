# Plan de acción — Adaptive Email Categorizer

Documento derivado de `adaptive-email-categorizer-spec.md`. Resume qué se construye, en qué orden, qué
decisiones se tomaron donde la especificación es ambigua y qué mejoras se añadieron.

## 1. Alcance de esta iteración

| Fase de la spec | Estado | Notas |
|---|---|---|
| 1 — MVP (parser `.eml`, SQLite, jerarquía, features, reglas, score, preguntas, feedback, aprendizaje) | **Implementada** | Sin LLM |
| 2 — Adaptive Questions (Information Gain, sesiones, explicaciones, reglas aprendidas) | **Implementada** | |
| 3 — Similarity (TF-IDF, kNN) | **Implementada** | |
| 4 — Semantic Intelligence (LLM opcional) | **Implementada como componente opcional** | Proveedor compatible con OpenAI/Ollama, extracción de intención y generación de preguntas con validación. Apagado por defecto. Embeddings: solo la interfaz. |
| 5 — Production | **Parcial** | Hecho: métricas, backups (`VACUUM INTO`), controles de privacidad (purga), fuente `.eml`. Pendiente: IMAP / Gmail / Graph y workers en background (la interfaz `source.Source` ya existe). |

Además: API REST (§27), CLI con modo interactivo tipo Akinator, evaluación offline (§35) y generador de datos de muestra.

## 2. Arquitectura y orden de construcción

Se construye de abajo hacia arriba; cada paquete es testeable sin red ni proveedor de correo (§2.3, §38).

```
 1. email        modelo + parser .eml (enmime) + normalización
 2. features     Feature/Set, extractores por capas (headers, remitente, contenido, intención, adjuntos, hilo)
 3. categories   árbol jerárquico, rutas, ancestros
 4. rules        motor de reglas (AND/OR/NOT, 10 operadores) → Evidence
 5. classifier   agregación de evidencia + clasificación jerárquica top-down + decisión (know/predict/ask/unknown)
 6. questions    modelo, verosimilitudes, Information Gain, utilidad, selector, actualización bayesiana
 7. similarity   TF-IDF + kNN → Evidence
 8. learning     reglas aprendidas (positivas/negativas), pesos incrementales, generalización por niveles
 9. explain      explicaciones legibles
10. storage      SQLite (modernc, sin CGO), migraciones versionadas, repositorios
11. config       YAML (categorías, preguntas, reglas, umbrales) + validación
12. llm          proveedor opcional + extractor + generador de preguntas validado
13. engine       orquestación: Process / NextQuestion / Answer / Feedback / Explain, sesiones
14. metrics      §34 + métrica de esfuerzo de §40
15. eval         dataset, split train/val/test determinista, oráculo simulado, curva de aprendizaje
16. api / cmd    REST + CLI `mailcat`
17. docs         README, config de ejemplo, datos de muestra
```

## 3. Decisiones donde la spec es ambigua

1. **Combinación de evidencia (§8, §9).** La spec suma pesos y "normaliza". Se usa *noisy-OR*
   (`1 − Π(1 − wᵢ)`) por categoría: es monótona, acotada en [0,1] y trata señales independientes
   correctamente. La evidencia negativa (reglas negativas, §21) multiplica por `(1 − |w|)`.
2. **El "no sé" es una hipótesis explícita.** Las categorías compiten entre sí *y contra `unknown`* con
   `P(c) = o_c / (1 + Σ o)`, `o = E/(1−E)`. Una categoría sola conserva `P = E` (evidencia débil y aislada
   de 0.3 *no* se convierte en 100 %), la evidencia fuerte gana a la débil (0.97 vs 0.43 → 0.95, no 0.69 como
   con normalización proporcional) y un empate genuino deja mucha masa en `unknown`. Implementa §41
   ("I know / good prediction / need to ask / don't know"). *Se descubrió al evaluar: la normalización
   proporcional inicial dejaba al sistema "dudando" de casos ya muy aprendidos.*
3. **Jerarquía (§10).** La evidencia se acumula hacia los ancestros. Los hijos reciben la parte de `P(padre)`
   que su evidencia explica (`cobertura = noisyOR(hijos)/E(padre)`, ≤ 1) repartida por odds; el remanente se
   queda en "padre (sin refinar)". La evidencia dirigida a un padre nunca compite con sus propios hijos. El
   resultado puede ser un nodo interno ("Finance") y la suma total sigue siendo 1.
4. **Cuatro estados de decisión (§15, §41).** `know` (≥ umbral alto y margen suficiente), `predict`
   (zona media sin pregunta útil), `ask`, `unknown`.
5. **Verosimilitud de las preguntas (§12).** Para calcular Information Gain hace falta `P(respuesta | categoría)`.
   La spec no la define. Se obtiene de (a) `affinity` opcional declarada en la config (resuelve el arranque
   en frío) mezclada con (b) conteos reales de respuestas confirmadas por el usuario (suavizado bayesiano).
6. **Respuesta ⇒ actualización bayesiana** de los candidatos, además de añadir la feature (fuente `user`)
   para que las reglas que la referencien se activen.
7. **Qué se aprende y de dónde.** Solo de señales del usuario (correcciones, confirmaciones, respuestas que
   resuelven la sesión). Nunca de clasificaciones automáticas propias (evita el sesgo de auto-confirmación).
   Solo se aprenden condiciones *extraíbles* del correo (no las respuestas del usuario), para que apliquen a
   correos futuros.
8. **Peso incremental (§18).** `w = cap · (1 − e^(−0.25·conf)) · (conf+1)/(conf+rej+2)` → 1 conf ≈ 0.20,
   3 ≈ 0.45, 10 ≈ 0.80 con `cap = 0.9`, y un rechazo aislado solo atenúa, no destruye.
9. **Determinismo (§38).** Sin aleatoriedad ni dependencia del reloj en la clasificación; desempates por ID;
   el muestreo de confirmaciones usa un hash del ID del correo.

## 4. Mejoras añadidas sobre la spec (áreas de oportunidad)

| # | Mejora | Motivo |
|---|---|---|
| M1 | **Margen mínimo** sobre el segundo candidato para auto-clasificar | Un umbral bajado por el usuario no debe permitir empates disfrazados |
| M2 | **Dominio registrable (eTLD+1)** y lista de **proveedores gratuitos** | `alerts.mybank.com` y `mybank.com` son la misma entidad; aprender "gmail.com → Banco" sería una mala generalización (§19) |
| M3 | **Aprendizaje por niveles de generalización**: remitente → lista (`List-Id`) → dominio → dominio+intención → intención | Implementa §19 con topes de peso por nivel (lo muy general no puede dominar) |
| M4 | **`affinity` en preguntas** para arranque en frío | Sin datos históricos, la entropía no puede elegir pregunta |
| M5 | **Tasa de respuesta** por pregunta (acción "omitir") dentro de la utilidad | Cumple §2.2: "alta probabilidad de ser respondidas" |
| M6 | **Ancestro como fallback** al agotar `max_questions` (en vez de ir directo a `unknown`) | Si se sabe que es Finance aunque no si Bank/Purchase, es mejor que nada |
| M7 | **Muestreo determinista de confirmaciones** que decrece con la fuerza de la regla | §16: pedir feedback sin molestar |
| M8 | **Sesiones persistentes** (`sessions`) con evidencia congelada | §30; la pregunta/respuesta es reproducible aunque la base cambie |
| M9 | **Preguntas LLM nunca se activan solas**: se guardan `validated=0` y requieren aprobación | §14 "validarse antes de incorporarse" |
| M10 | **Redacción de contenido para el LLM**: por defecto solo se envían features, no el cuerpo; proveedor externo requiere `allow_external` | §24 |
| M11 | **API local por defecto** (`127.0.0.1`), token Bearer opcional, límites de tamaño/timeouts | Local-first y superficie mínima |
| M12 | **Métrica de esfuerzo** `correctas / (preguntas + correcciones + etiquetados manuales)` y curva de aprendizaje en `eval` | §40: la métrica principal no es accuracy |
| M13 | **Oráculo simulado** en evaluación y datos sintéticos reproducibles | Permite verificar de forma automática el criterio de éxito §40 |
| M14 | **Purga de contenido** que conserva el conocimiento aprendido; **backup** consistente | §24, §36 Fase 5 |
| M15 | **Validación de config** (ciclos, padres inexistentes, regex inválidas, afinidades a categorías inexistentes) | Fallar al inicio, no a media clasificación |
| M17 | **Reglas aprendidas con "reparto de confirmaciones"**: si una misma condición (p. ej. un dominio) se confirmó para varias categorías, cada regla conserva solo su parte | Descubierto en `eval`: un dominio que envía "work" y "meetings" hacía que todo fuera "meetings" con 96 % de confianza |
| M18 | **Descuento por evidencia dependiente**: las reglas aprendidas de un mismo correo (remitente ⊂ dominio ⊂ dominio+intención) cuentan al 50 % salvo la más fuerte | No son señales independientes; sin esto el noisy-OR se sobreconfía |
| M19 | **Análisis de errores en `eval`** (lista de fallos con decisión y confianza) y métrica "acierto cuando está seguro" | Cuantifica §41: el sistema no debe equivocarse *con seguridad* |
| M16 | **Features de hilo** (`thread.previous_category`) usando `In-Reply-To`/`References` | §4/§6 las listan; mejora respuestas encadenadas |

## 5. Extensiones al esquema SQL

Solo aditivas respecto a §5: `classifications.is_final`; `questions.source/validated/definition`;
`interactions.session_id/kind/expected_gain/realized_gain/label_source`; `learned_rules.key/kind/negative`
(índice único `category_id,key,negative`); tablas nuevas `sessions` y `schema_meta`.

## 6. Resultados medidos

Corpus sintético reproducible (`mailcat sample-data`, 12 categorías × 40, split 60/20/20, usuario simulado):

- La tasa de preguntas baja de ≈ 40 % en las primeras ventanas a ≈ 0–10 % al final del entrenamiento (§40).
- En *test* (sin aprendizaje): acierto ≈ 95–98 %, y **100 % de acierto cuando el sistema dice "estoy seguro"**.
- Latencia de clasificación p95 ≈ 2.5 ms (objetivo §38: < 100 ms).
- Los fallos restantes son predicciones *honestas* ("predict", 0.65–0.80, se pide confirmación), no errores seguros.

## 7. Verificación

- Tests unitarios por paquete (reglas, entropía, jerarquía, pesos, parser, tokenización, storage).
- Test de integración end-to-end del escenario de §39 (banco → auto; tienda → pregunta → aprende → ya no pregunta).
- Test de evaluación: la tasa de preguntas decae y el acierto sube con datos sintéticos (criterio §40).
- Test de API con `httptest`; test del cliente LLM con servidor falso.
- `go vet`, `go test -race ./...`.

## 8. Fuera de alcance (siguiente iteración)

IMAP/Gmail/Microsoft Graph, workers en background, embeddings reales, decaimiento temporal de reglas,
UI gráfica. Las interfaces (`source.Source`, `similarity.Index`, `llm.Provider`) están preparadas para ellos.
